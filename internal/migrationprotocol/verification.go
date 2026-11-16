package migrationprotocol

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

type EntityRegistryIdentity struct {
	EntityID      string `json:"entityId"`
	UniqueID      string `json:"uniqueId"`
	Platform      string `json:"platform"`
	ConfigEntryID string `json:"configEntryId"`
}

type IntegrationEntryIdentity struct {
	EntryID string `json:"entryId"`
	Domain  string `json:"domain"`
	Source  string `json:"source"`
}

type TodoItem struct {
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type TodoItems struct {
	EntityID string     `json:"entityId"`
	Items    []TodoItem `json:"items"`
}

func VerificationKind(kind string) bool {
	return kind == "EntityRegistryIdentity" || kind == "IntegrationEntryIdentity" || kind == "TodoItems"
}

// ProjectionDigest accepts only the closed, non-secret projections of protocol v1.
func ProjectionDigest(kind, id string, raw []byte) (string, error) {
	var data any
	bad := fmt.Errorf("invalid verification projection")
	if strings.TrimSpace(id) == "" {
		return "", bad
	}
	switch kind {
	case "EntityRegistryIdentity":
		var p EntityRegistryIdentity
		if Decode(raw, &p) != nil || p.EntityID != id || p.UniqueID == "" || p.Platform == "" || !hasNonNullFields(raw, "entityId", "uniqueId", "platform") {
			return "", bad
		}
		// Optional config entry is normalized to an empty string.
		data = p
	case "IntegrationEntryIdentity":
		var p IntegrationEntryIdentity
		if Decode(raw, &p) != nil || p.EntryID != id || p.Domain == "" || p.Source == "" || !hasNonNullFields(raw, "entryId", "domain", "source") {
			return "", bad
		}
		data = p
	case "TodoItems":
		var p TodoItems
		if Decode(raw, &p) != nil || p.EntityID != id || p.Items == nil || !hasNonNullFields(raw, "entityId", "items") {
			return "", bad
		}
		sort.Slice(p.Items, func(i, j int) bool { return p.Items[i].UID < p.Items[j].UID })
		for i, item := range p.Items {
			if item.UID == "" || item.Summary == "" || (item.Status != "needs_action" && item.Status != "completed") || (i > 0 && p.Items[i-1].UID == item.UID) {
				return "", bad
			}
		}
		data = p
	default:
		return "", bad
	}
	// Reject explicit nulls and omitted required fields, not merely zero-valued decoding.
	var fields map[string]json.RawMessage
	if Decode(raw, &fields) != nil {
		return "", bad
	}
	for _, v := range fields {
		if string(v) == "null" {
			return "", bad
		}
	}
	return Digest(struct {
		Version string `json:"version"`
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Data    any    `json:"data"`
	}{"v1", kind, id, data})
}

func hasNonNullFields(raw []byte, required ...string) bool {
	var fields map[string]json.RawMessage
	if Decode(raw, &fields) != nil {
		return false
	}
	for _, key := range required {
		if value, ok := fields[key]; !ok || string(value) == "null" {
			return false
		}
	}
	return true
}

func VerificationDigest(items []ha.MigrationVerificationItem) (string, error) {
	if len(items) == 0 || len(items) > 256 {
		return "", fmt.Errorf("invalid verification set")
	}
	items = append([]ha.MigrationVerificationItem(nil), items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind == items[j].Kind {
			return items[i].ID < items[j].ID
		}
		return items[i].Kind < items[j].Kind
	})
	for i, item := range items {
		if !VerificationKind(item.Kind) || strings.TrimSpace(item.ID) == "" || len(item.ID) > 256 || !ValidDigest(item.ExpectedDigest) || (i > 0 && items[i-1].Kind == item.Kind && items[i-1].ID == item.ID) {
			return "", fmt.Errorf("invalid verification set")
		}
	}
	return Digest(struct {
		Version string                         `json:"version"`
		Items   []ha.MigrationVerificationItem `json:"items"`
	}{"v1", items})
}
