package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type floorRegistryReader interface {
	SendWebSocketCommand(
		context.Context,
		string,
		string,
		map[string]interface{},
	) (json.RawMessage, error)
}

type floorProposal struct {
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Level *int   `json:"level"`
}

// uniqueObject rejects duplicate keys, including for fields we do not consume.
func uniqueObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("invalid object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid object")
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("invalid object")
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("duplicate field")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, fmt.Errorf("invalid field")
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("invalid object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data")
	}
	return fields, nil
}

func floorName(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "floor-" + hex.EncodeToString(digest[:8])
}

func parseFloorEntry(raw json.RawMessage) (string, floorProposal, error) {
	fields, err := uniqueObject(raw)
	if err != nil {
		return "", floorProposal{}, err
	}
	var id, name string
	if json.Unmarshal(fields["floor_id"], &id) != nil || strings.TrimSpace(id) == "" ||
		strings.TrimSpace(
			id,
		) != id || json.Unmarshal(fields["name"], &name) != nil || strings.TrimSpace(name) == "" {
		return id, floorProposal{}, fmt.Errorf("incomplete floor identity or name")
	}
	iconRaw, iconPresent := fields["icon"]
	levelRaw, levelPresent := fields["level"]
	var icon *string
	var level *int
	if !iconPresent || !levelPresent || json.Unmarshal(iconRaw, &icon) != nil ||
		json.Unmarshal(levelRaw, &level) != nil {
		return id, floorProposal{}, fmt.Errorf("incomplete floor fields")
	}
	proposal := floorProposal{Name: name, Level: level}
	if icon != nil {
		proposal.Icon = *icon
	}
	return id, proposal, nil
}

func scanFloors(
	ctx context.Context,
	reader floorRegistryReader,
	token string,
) ([]ReportObject, error) {
	raw, err := reader.SendWebSocketCommand(ctx, token, "config/floor_registry/list", nil)
	if err != nil {
		return nil, fmt.Errorf("floor registry unavailable")
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, fmt.Errorf("floor registry invalid")
	}
	type observed struct {
		id       string
		proposal floorProposal
		err      error
		raw      json.RawMessage
	}
	parsed := make([]observed, 0, len(entries))
	counts := make(map[string]int)
	for _, entry := range entries {
		id, proposal, parseErr := parseFloorEntry(entry)
		if id != "" {
			counts[id]++
		}
		parsed = append(parsed, observed{id, proposal, parseErr, entry})
	}
	objects := make([]ReportObject, 0, len(parsed))
	for _, entry := range parsed {
		id := entry.id
		if id == "" {
			sum := sha256.Sum256(entry.raw)
			id = "missing-" + hex.EncodeToString(sum[:8])
		}
		object := ReportObject{Kind: "Floor", SourceID: id, Name: floorName(id)}
		switch {
		case counts[id] > 1:
			object.Classification, object.Reason = ClassificationConflict, "ambiguous floor identity"
		case entry.err != nil:
			object.Classification, object.Reason = ClassificationIncomplete, "incomplete floor identity or fields"
		default:
			object.Classification = ClassificationImportable
			object.Proposal, err = json.Marshal(entry.proposal)
			if err != nil {
				return nil, fmt.Errorf("encode floor proposal")
			}
		}
		objects = append(objects, object)
	}
	return objects, nil
}
