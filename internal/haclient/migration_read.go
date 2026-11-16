package haclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxMigrationDefinitionBytes = 2 << 20

// ReadMigrationDefinition reads a config API definition for comparison only.
// The returned JSON may contain sensitive user configuration and must never be
// logged, placed in status, or included in a migration report. A successful
// read alone does not qualify the kind for import or adoption.
func (c *Client) ReadMigrationDefinition(
	ctx context.Context,
	token, kind, id string,
) (json.RawMessage, error) {
	switch kind {
	case "automation", "scene", "script":
	default:
		return nil, fmt.Errorf("unsupported migration definition kind")
	}
	id, err := normalizeMigrationID(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("migration read requires authentication")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+configPath(kind, id),
		nil,
	)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "invalid migration read request"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Type:    ErrorTypeNotReady,
			Message: "migration definition unavailable",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    "migration definition request rejected",
			StatusCode: resp.StatusCode,
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMigrationDefinitionBytes+1))
	if err != nil {
		return nil, &Error{
			Type:    ErrorTypeNotReady,
			Message: "migration definition read failed",
			Err:     err,
		}
	}
	if len(data) > maxMigrationDefinitionBytes {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "migration definition exceeds size limit",
		}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || len(object) == 0 {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "migration definition is invalid or incomplete",
		}
	}
	return json.RawMessage(data), nil
}

// MigrationObservation is a redacted, read-only representation of one HA object.
// It intentionally contains no credentials or configuration-flow input values.
type MigrationObservation struct {
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

func normalizeMigrationID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("migration object ID is required")
	}
	return id, nil
}

func newMigrationObservation(kind, id string, payload any) (*MigrationObservation, error) {
	id, err := normalizeMigrationID(id)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode migration observation: %w", err)
	}
	return &MigrationObservation{Kind: kind, ID: id, Payload: encoded}, nil
}

// ReadMigrationIntegration observes one config entry by its immutable HA entry ID.
// It never starts a flow or reads secret-bearing configuration data.
func (c *Client) ReadMigrationIntegration(
	ctx context.Context,
	token, entryID string,
) (*MigrationObservation, error) {
	entryID, err := normalizeMigrationID(entryID)
	if err != nil {
		return nil, err
	}
	entries, err := c.ListConfigEntries(ctx, token)
	if err != nil {
		return nil, err
	}
	var match *ConfigEntry
	for _, entry := range entries {
		if entry.EntryID == entryID {
			if match != nil {
				return nil, fmt.Errorf("migration integration identity is ambiguous")
			}
			entryCopy := entry
			match = &entryCopy
		}
	}
	if match != nil {
		if strings.TrimSpace(match.Domain) == "" {
			return nil, fmt.Errorf("migration integration observation is incomplete")
		}
		return newMigrationObservation("integration", match.EntryID, match)
	}
	return nil, fmt.Errorf("migration integration %q was not found", entryID)
}
