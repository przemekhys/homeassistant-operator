# Controller logging

*For contributors — the structured logging contract for controller code. Not needed to use the operator.*

Every reconciler log is structured so it can be queried, correlated, and handled
without exposing Home Assistant or Kubernetes credentials. Use the logger from
the reconciliation context; do not create a separate logger or write directly
to standard output.

## Reconciliation context

Start each reconciler with `reconcileLogger` and pass its returned context to
all helpers it calls:

```go
func (r *ExampleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, log := reconcileLogger(ctx, "Example", "Example", req)
	log.Info("example reconciled")
	// Pass ctx to all client calls and helpers.
	return ctrl.Result{}, nil
}
```

The logger adds these fields to every emitted record:

| Field | Meaning |
|-------|---------|
| `controller` | Name of the reconciler emitting the record |
| `resourceKind` | Kubernetes kind being reconciled |
| `namespace` | Namespace of the reconciled resource |
| `name` | Name of the reconciled resource |
| `homeAssistant` | Home Assistant resource name; empty until it is known |
| `operation` | High-level operation; `reconcile` for reconciler entry points |
| `entryPoint` | Entry point that initiated the work; `Reconcile` for reconciler entry points |
| `correlationID` | UUID unique to one reconciliation attempt |
| `event` | Stable identifier for the operational outcome |

Give every new or changed log call an explicit event. This keeps dashboard and
alert queries stable when its human-readable message changes:

```go
log.Info("ConfigMap updated", "event", "configmap_updated")
```

Existing calls without an `event` retain a compatibility fallback derived from
their static message. Do not use that fallback in new code.

## Safe fields

Add only bounded, non-sensitive scalar values as log fields:

```go
log.Info("ConfigMap updated", "configMapName", configMap.Name, "attempt", attempt)
```

Never log API tokens, Secrets or Secret data, passwords, credentials,
authorization headers, complete Home Assistant payloads, request/response
bodies, or arbitrary errors returned by remote systems. The log sink rejects
known sensitive field names and complex values, and records an error type rather
than error text. That is a safeguard, not permission to pass unsafe data to the
logger: avoid constructing or attaching it in the first place.

## Severity

Use severities consistently:

| Level | Use for |
|-------|---------|
| `error` | A failed operation that affects reconciliation and needs a retry or attention |
| `warn` | A recoverable or degraded condition that does not fail the current operation |
| `info` | State transitions, successful operations, and meaningful waits or requeues |
| `debug` | High-volume implementation detail, emitted with `V(1)` or higher |

`logr` has `Info` and `Error` methods, but no separate warning method. Express a
warning as an info record with `"severity", "warn"`; do not call `Error` for a
condition that is not an error. Debug logs must follow the same data-safety rules
as info logs.
