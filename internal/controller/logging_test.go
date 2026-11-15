package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestStableEvent(t *testing.T) {
	got := stableEvent("Failed to POST automation via HA REST API")
	const want = "failed_to_post_automation_via_ha_rest_api"
	if got != want {
		t.Fatalf("stableEvent() = %q, want %q", got, want)
	}
}

func TestEventValuePrefersExplicitEvent(t *testing.T) {
	got := eventValue([]any{"event", "automation_applied"}, "Automation applied")
	const want = "automation_applied"
	if got != want {
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

func TestReconcileLoggerPreservesDiscardLogger(t *testing.T) {
	ctx := logf.IntoContext(context.Background(), logr.Logger{})
	_, log := reconcileLogger(ctx, "Example", "Example", reconcile.Request{})
	if log.GetSink() != nil {
		t.Fatal("reconcileLogger() wrapped a logger without a sink")
	}
	log.Info("discarded safely")
}

func TestSafeLogValuesDropsSensitiveFields(t *testing.T) {
	got := safeLogValues([]any{
		"operation", "apply",
		"apiToken", "sensitive-token",
		"responseBody", "full payload",
		"currentContent", "current configuration",
		"expectedContent", "expected configuration",
		"error", errors.New("remote response with secret"),
		"attempt", 2,
		"desired", map[string]any{"payload": "private"},
		"configMap", "generated-config",
		"configMapName", "generated-config",
	})
	want := []any{
		"operation", "apply",
		"attempt", 2,
		"configMap", "generated-config",
		"configMapName", "generated-config",
	}
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
