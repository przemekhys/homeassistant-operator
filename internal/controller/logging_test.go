package controller

import (
	"errors"
	"reflect"
	"testing"
)

func TestStableEvent(t *testing.T) {
	if got, want := stableEvent("Failed to POST automation via HA REST API"), "failed_to_post_automation_via_ha_rest_api"; got != want {
		t.Fatalf("stableEvent() = %q, want %q", got, want)
	}
}

func TestEventValuePrefersExplicitEvent(t *testing.T) {
	if got, want := eventValue([]any{"event", "automation_applied"}, "Automation applied"), "automation_applied"; got != want {
		t.Fatalf("eventValue() = %q, want %q", got, want)
	}
}

func TestMergeLogValuesUsesMostSpecificContext(t *testing.T) {
	got := mergeLogValues([]any{"homeAssistant", "", "operation", "reconcile"}, []any{"homeAssistant", "home"})
	want := []any{"operation", "reconcile", "homeAssistant", "home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeLogValues() = %#v, want %#v", got, want)
	}
}

func TestSafeLogValuesDropsSensitiveFields(t *testing.T) {
	got := safeLogValues([]any{
		"operation", "apply",
		"apiToken", "sensitive-token",
		"responseBody", "full payload",
		"error", errors.New("remote response with secret"),
		"attempt", 2,
		"desired", map[string]any{"payload": "private"},
	})
	want := []any{"operation", "apply", "attempt", 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("safeLogValues() = %#v, want %#v", got, want)
	}
}

func TestErrorTypeDoesNotExposeMessage(t *testing.T) {
	err := errors.New("private token: abc123")
	if got := errorType(err); got != "*errors.errorString" {
		t.Fatalf("errorType() = %q", got)
	}
}
