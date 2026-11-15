package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconcileLogger adds the common log contract to every reconciliation log.
// Values unavailable at reconcile entry (for example a referenced HomeAssistant)
// are represented by an empty value rather than omitting the field.
func reconcileLogger(
	ctx context.Context,
	controller, resourceKind string,
	req reconcile.Request,
) (context.Context, logr.Logger) {
	base := logf.FromContext(ctx)
	if base.GetSink() == nil {
		return logf.IntoContext(ctx, base), base
	}
	correlationID := uuid.NewString()
	homeAssistant := ""
	if resourceKind == "HomeAssistant" {
		homeAssistant = req.Name
	}
	log := logr.New(&structuredSink{
		delegate: base.GetSink(),
		fields: []any{
			"controller", controller,
			"resourceKind", resourceKind,
			"namespace", req.Namespace,
			"name", req.Name,
			"homeAssistant", homeAssistant,
			"operation", "reconcile",
			"entryPoint", "Reconcile",
			"correlationID", correlationID,
		},
	})
	return logf.IntoContext(ctx, log), log
}

// withHomeAssistant enriches the reconcile context after a dependent resource
// has resolved its HomeAssistant reference. Keep this context for every
// subsequent helper and client call.
func withHomeAssistant(ctx context.Context, name string) (context.Context, logr.Logger) {
	log := logf.FromContext(ctx).WithValues("homeAssistant", name)
	return logf.IntoContext(ctx, log), log
}

var unsafeLogField = regexp.MustCompile(
	`(?i)(token|secret|password|credential|authorization|payload|` +
		`request(body)?|response(body)?|(?:current|expected|configuration|config)content|^error$)`,
)

// structuredSink provides a stable event identifier and prevents sensitive
// values (including arbitrary error text from remote systems) from reaching logs.
type structuredSink struct {
	delegate logr.LogSink
	fields   []any
	name     string
}

func (s *structuredSink) Init(info logr.RuntimeInfo) { s.delegate.Init(info) }

func (s *structuredSink) Enabled(level int) bool { return s.delegate.Enabled(level) }

func (s *structuredSink) Info(level int, message string, kv ...any) {
	values := mergeLogValues(s.fields, safeLogValues(kv))
	values = append(withoutField(values, "event"), "event", eventValue(values, message))
	s.delegate.Info(level, message, values...)
}

func (s *structuredSink) Error(err error, message string, kv ...any) {
	values := mergeLogValues(s.fields, safeLogValues(kv))
	values = append(withoutField(values, "event"), "event", eventValue(values, message), "errorType", errorType(err))
	// Do not forward the original error: HTTP and Kubernetes errors may contain
	// request data, Secret values, or complete Home Assistant responses.
	s.delegate.Error(errors.New("operation failed"), message, values...)
}

func (s *structuredSink) WithValues(kv ...any) logr.LogSink {
	return &structuredSink{delegate: s.delegate, fields: mergeLogValues(s.fields, safeLogValues(kv)), name: s.name}
}

func (s *structuredSink) WithName(name string) logr.LogSink {
	copy := *s
	copy.delegate = s.delegate.WithName(name)
	copy.name = strings.Trim(strings.Join([]string{s.name, name}, "."), ".")
	return &copy
}

func safeLogValues(kv []any) []any {
	filtered := make([]any, 0, len(kv))
	for i := 0; i < len(kv); {
		if i+1 >= len(kv) {
			filtered = append(filtered, kv[i], "<redacted>")
			break
		}
		key, ok := kv[i].(string)
		if ok && unsafeLogField.MatchString(key) {
			i += 2
			continue
		}
		if !safeLogValue(kv[i+1]) {
			i += 2
			continue
		}
		filtered = append(filtered, kv[i], kv[i+1])
		i += 2
	}
	return filtered
}

func eventValue(kv []any, message string) string {
	for i := 0; i+1 < len(kv); i += 2 {
		if key, ok := kv[i].(string); ok && key == "event" {
			if event, ok := kv[i+1].(string); ok && event != "" {
				return event
			}
		}
	}
	// Compatibility fallback for existing call sites. New and changed log calls
	// must supply an explicit event so a message edit cannot alter the contract.
	return stableEvent(message)
}

func withoutField(kv []any, field string) []any {
	filtered := make([]any, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		if key, ok := kv[i].(string); ok && key == field {
			continue
		}
		filtered = append(filtered, kv[i], kv[i+1])
	}
	return filtered
}

// mergeLogValues keeps one value per string key. Values on the right override
// earlier context, which makes a resolved HomeAssistant reference replace the
// empty value installed at reconcile entry.
func mergeLogValues(groups ...[]any) []any {
	merged := make([]any, 0)
	for _, group := range groups {
		for i := 0; i+1 < len(group); i += 2 {
			key, ok := group[i].(string)
			if !ok {
				merged = append(merged, group[i], group[i+1])
				continue
			}
			merged = append(withoutField(merged, key), key, group[i+1])
		}
	}
	return merged
}

func safeLogValue(value any) bool {
	switch value := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	case string:
		return len(value) <= 256
	default:
		// Complex values can contain complete Kubernetes objects or HA payloads.
		return false
	}
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

var nonEventChars = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func stableEvent(message string) string {
	event := strings.ToLower(strings.Trim(nonEventChars.ReplaceAllString(message, "_"), "_"))
	if event == "" {
		return "operation"
	}
	return event
}
