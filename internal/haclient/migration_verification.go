package haclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

type migrationEntityIdentity struct {
	EntityID      string  `json:"entity_id"`
	UniqueID      string  `json:"unique_id"`
	Platform      string  `json:"platform"`
	ConfigEntryID *string `json:"config_entry_id"`
}

// ReadMigrationVerification projects only the explicitly supported, non-secret
// identity and data fields. Raw HA responses must not be logged or persisted.
func (c *Client) ReadMigrationVerification(ctx context.Context, token, kind, id string) ([]byte, error) {
	if ctx == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("verification inputs unavailable")
	}
	switch kind {
	case "EntityRegistryIdentity":
		entity, err := c.readVerificationEntity(ctx, token, id)
		if err != nil {
			return nil, err
		}
		entry := ""
		if entity.ConfigEntryID != nil {
			entry = *entity.ConfigEntryID
		}
		return json.Marshal(migrationprotocol.EntityRegistryIdentity{EntityID: entity.EntityID, UniqueID: entity.UniqueID, Platform: entity.Platform, ConfigEntryID: entry})
	case "IntegrationEntryIdentity":
		entries, err := c.ListConfigEntries(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("integration verification unavailable")
		}
		var found *ConfigEntry
		for _, entry := range entries {
			if entry.EntryID == id {
				if found != nil {
					return nil, fmt.Errorf("integration identity ambiguous")
				}
				item := entry
				found = &item
			}
		}
		if found == nil || found.Domain == "" || found.Source == "" {
			return nil, fmt.Errorf("integration verification incomplete")
		}
		return json.Marshal(migrationprotocol.IntegrationEntryIdentity{EntryID: found.EntryID, Domain: found.Domain, Source: found.Source})
	case "TodoItems":
		entity, err := c.readVerificationEntity(ctx, token, id)
		if err != nil || entity.Platform != "local_todo" {
			return nil, fmt.Errorf("local todo verification unavailable")
		}
		if err := c.verifyEntityAvailable(ctx, token, id); err != nil {
			return nil, err
		}
		raw, err := c.sendBackupCommand(ctx, token, "todo/item/list", map[string]interface{}{"entity_id": id})
		if err != nil {
			return nil, fmt.Errorf("todo verification unavailable")
		}
		var result struct {
			Items []struct {
				UID     string `json:"uid"`
				Summary string `json:"summary"`
				Status  string `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Items == nil {
			return nil, fmt.Errorf("todo verification incomplete")
		}
		items := make([]migrationprotocol.TodoItem, 0, len(result.Items))
		for _, item := range result.Items {
			items = append(items, migrationprotocol.TodoItem{UID: item.UID, Summary: item.Summary, Status: item.Status})
		}
		if err := c.verifyEntityAvailable(ctx, token, id); err != nil {
			return nil, err
		}
		return json.Marshal(migrationprotocol.TodoItems{EntityID: id, Items: items})
	default:
		return nil, fmt.Errorf("unsupported verification kind")
	}
}

func (c *Client) readVerificationEntity(ctx context.Context, token, id string) (*migrationEntityIdentity, error) {
	raw, err := c.sendBackupCommand(ctx, token, "config/entity_registry/get", map[string]interface{}{"entity_id": id})
	if err != nil {
		return nil, fmt.Errorf("entity verification unavailable")
	}
	var entity migrationEntityIdentity
	if json.Unmarshal(raw, &entity) != nil || entity.EntityID != id || entity.UniqueID == "" || entity.Platform == "" {
		return nil, fmt.Errorf("entity verification incomplete")
	}
	return &entity, nil
}

func (c *Client) verifyEntityAvailable(ctx context.Context, token, id string) error {
	raw, err := c.sendBackupCommand(ctx, token, "get_states", nil)
	if err != nil {
		return fmt.Errorf("todo availability unavailable")
	}
	var states []struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	if json.Unmarshal(raw, &states) != nil {
		return fmt.Errorf("todo availability incomplete")
	}
	matches := 0
	for _, state := range states {
		if state.EntityID == id {
			matches++
			if state.State == "" || state.State == "unknown" || state.State == "unavailable" {
				return fmt.Errorf("todo unavailable")
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("todo availability ambiguous")
	}
	return nil
}
