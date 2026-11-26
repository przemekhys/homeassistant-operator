package controller

import (
	"encoding/json"
	"testing"
)

func TestMigrationAdoptionFailClosedStates(t *testing.T) {
	jsonValue := func(value string) json.RawMessage { return json.RawMessage(value) }
	for _, tc := range []struct {
		name             string
		imported, paused bool
		source, target   string
		desired, actual  json.RawMessage
		want             migrationAdoptionState
	}{
		{"paused", true, true, "source", "target", jsonValue(`{"name":"a"}`), jsonValue(`{"name":"a"}`), migrationAdoptionPaused},
		{"missing identity", true, false, "", "target", jsonValue(`{}`), jsonValue(`{}`), migrationAdoptionBlocked},
		{"incomplete actual", true, false, "source", "target", jsonValue(`{}`), nil, migrationAdoptionBlocked},
		{"mismatched identity", true, false, "source", "target", jsonValue(`{}`), jsonValue(`{}`), migrationAdoptionBlocked},
		{"conflict", true, false, "source", "source", jsonValue(`{"name":"a"}`), jsonValue(`{"name":"b"}`), migrationAdoptionConflict},
		{"canonical match", true, false, "source", "source", jsonValue(`{"a":1,"b":2}`), jsonValue(`{"b":2,"a":1}`), migrationAdoptionAdopted},
		{"ordinary CR", false, false, "", "", nil, nil, migrationAdoptionAdopted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluateMigrationAdoption(
				tc.imported,
				tc.paused,
				tc.source,
				tc.target,
				tc.desired,
				tc.actual,
			)
			if result.State != tc.want {
				t.Fatalf("state = %s, want %s", result.State, tc.want)
			}
			if tc.want == migrationAdoptionConflict &&
				(result.ComparisonDigest == "" || string(tc.desired) != `{"name":"a"}`) {
				t.Fatalf("unsafe conflict: %#v", result)
			}
		})
	}
}

func TestMigrationAdoptionRejectsDuplicateKeysAndPreservesIntegerPrecision(t *testing.T) {
	for _, raw := range []string{`{"id":1,"id":2}`, `{"nested":{"secret":1,"secret":2}}`, `[{"id":1,"id":2}]`, `{} {}`} {
		if _, err := canonicalMigrationJSON(json.RawMessage(raw)); err == nil {
			t.Fatalf("ambiguous JSON accepted: %s", raw)
		}
	}
	result := evaluateMigrationAdoption(
		true,
		false,
		"id",
		"id",
		json.RawMessage(`{"value":9007199254740992}`),
		json.RawMessage(`{"value":9007199254740993}`),
	)
	if result.State != migrationAdoptionConflict {
		t.Fatalf("distinct large integers considered equal: %#v", result)
	}
}
