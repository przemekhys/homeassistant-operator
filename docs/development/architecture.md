# Architecture

*For contributors — how the operator is put together internally. Not needed to use the operator.*

## Overview

Home Assistant Operator is a [Kubebuilder v4](https://book.kubebuilder.io/) operator that manages Home Assistant instances on Kubernetes declaratively. It bridges the gap between Kubernetes resource management and the Home Assistant REST/WebSocket API.

```mermaid
graph TD
    GitOps["kubectl / GitOps\n(CRs YAML)"]

    subgraph operator["homeassistant-operator (controller-manager pod)"]
        Reconcilers["Reconcilers\n(per CRD)"]
        haclient["haclient\nREST + WebSocket"]
        runtime["controller-runtime\ncache / informers"]
        Reconcilers -- uses --> haclient
    end

    subgraph k8s["Kubernetes API"]
        resources["StatefulSet · Service\nPVC · ConfigMap · Secret"]
    end

    HA["Home Assistant pod\n(REST + WebSocket)"]

    GitOps --> operator
    Reconcilers -- "Kubernetes API" --> k8s
    haclient -- "HTTP / WebSocket" --> HA
```

## CRD dependency chain

All CRDs form a strict dependency graph. The operator enforces it — a CRD requeues (waits) until its dependency is ready.

```mermaid
graph TD
    HAConfig[HomeAssistantConfiguration] -->|required| HA[HomeAssistant]
    HA -->|homeAssistantRef| Secrets[HomeAssistantSecrets]
    HA -->|homeAssistantRef| Auto[HomeAssistantAutomation]
    HA -->|homeAssistantRef| Scene[HomeAssistantScene]
    HA -->|homeAssistantRef| Script[HomeAssistantScript]
    HA -->|homeAssistantRef| Integration[HomeAssistantIntegration]
    HA -->|homeAssistantRef| Floor[HomeAssistantFloor]
    HA -->|homeAssistantRef| Label[HomeAssistantLabel]
    HA -->|homeAssistantRef| Area[HomeAssistantArea]
    Floor -.->|optional ref| Area
    Label -.->|optional ref| Area
```

**Recommended apply order:**

1. `HomeAssistantConfiguration` + `HomeAssistant` (simultaneously or config first)
2. `HomeAssistantSecrets` (optional)
3. `HomeAssistantFloor`, `HomeAssistantLabel` (optional, for room structure)
4. `HomeAssistantArea` (optional, depends on Floor/Label)
5. `HomeAssistantAutomation`, `HomeAssistantScene`, `HomeAssistantScript`, `HomeAssistantIntegration` (optional, require bootstrap token)

## Reconciliation flows

### HomeAssistant

The core reconciler orchestrates the full lifecycle.

```mermaid
flowchart LR
    A[Reconcile] --> B[reconcilePVC]
    B --> C[reconcileStatefulSet]
    C --> D[reconcileService]
    D --> E[reconcileBootstrap]
    E --> F[reconcileBackupConfig]
    F --> G[updateStatus]
```

**reconcileStatefulSet** builds the StatefulSet spec from:

- `spec.version` → container image tag
- `spec.resources` → CPU/memory limits
- `spec.storage` → PVC claim template
- `spec.service`, `spec.ingress` → Service/Ingress resources
- `spec.hostNetwork` → enables mDNS/SSDP discovery
- Config hash annotations from `HomeAssistantSecrets` and `HomeAssistantConfiguration` → rolling restarts

**reconcileBootstrap** is a state machine that drives the onboarding flow:

```mermaid
stateDiagram-v2
    [*] --> WaitForHealth: pod not ready
    WaitForHealth --> WaitForAPIReady: HTTP health OK
    WaitForAPIReady --> CheckOnboarding: GET /api/config → 401
    CheckOnboarding --> PerformOnboarding: onboarding pending
    CheckOnboarding --> Done: already complete
    PerformOnboarding --> CreateUser
    CreateUser --> SetCoreConfig
    SetCoreConfig --> SetAnalytics
    SetAnalytics --> CreateAPIToken
    CreateAPIToken --> Done
    Done --> [*]
```

### HomeAssistantConfiguration

Manages `configuration.yaml` as a Kubernetes-native resource.

```mermaid
flowchart LR
    A[CR change] --> B[Generate ConfigMap]
    B --> C{reloadStrategy?}
    C -->|auto| D{sections changed?}
    D -->|automation/script/logger| E[Hot-reload via REST API]
    D -->|homeassistant/http/mqtt| F[Annotate StatefulSet → rolling restart]
    C -->|hot-reload| E
    C -->|restart| F
```

Auto-include: the controller automatically injects `automation: !include automations.yaml`, `scene: !include scenes.yaml`, and `script: !include scripts.yaml` into `configuration.yaml` if missing — required for HA to load resources managed by the operator.

### HomeAssistantAutomation / Scene / Script

Each CR maps to a single entity in HA via the REST config API. No ConfigMap aggregation.

```mermaid
flowchart LR
    A[CR created/updated] --> B[Get API token]
    B -->|missing| C[Requeue]
    B -->|present| D["PUT /api/config/{type}/config/{id}"]
    D --> E[Hot-reload via REST]
    E --> F[Update status]

    G[CR deleted] --> H[Finalizer runs]
    H --> I["DELETE /api/config/{type}/config/{id}"]
    I --> J[Remove finalizer]
```

HA persists these resources in `automations.yaml`, `scenes.yaml`, `scripts.yaml` on the PVC — they survive pod restarts.

### HomeAssistantIntegration

Manages HA integrations through the Config Flow API.

```mermaid
flowchart TD
    A[Reconcile] --> B{deleting?}
    B -->|yes| C[RemoveConfigEntry → remove finalizer]
    B -->|no| D[Get HA + API token]
    D -->|not ready| E[Requeue]
    D -->|ready| F[Resolve config values]
    F --> G{entryID in status?}
    G -->|yes, hash unchanged| H[Verify entry exists → Done]
    G -->|yes, hash changed| I[RemoveConfigEntry → clear entryID → restart flow]
    G -->|no| J{integration exists in HA?}
    J -->|yes| K[Adopt: save entryID → Done]
    J -->|no| L[StartConfigFlow → SubmitConfigFlow → save entryID]
```

### HomeAssistantFloor / Label / Area

These CRDs manage the HA room structure via the WebSocket registry API (no REST equivalent).

```mermaid
flowchart LR
    A[Reconcile] --> B[SendWebSocketCommand]
    B -->|floor_registry/list| C{exists by name?}
    C -->|no| D[create]
    C -->|yes| E[update if changed]
    D --> F[Save ID in status]
    E --> F
```

Area resolution: `floorName` → Floor CR → `status.floorID`; `labels[]` → Label CRs → `status.labelID`s. If a Floor/Label is not yet ready, Area requeues (30 s) — no explicit watch relationship needed.

## Key mechanisms

### Owner references and garbage collection

All sub-resources created by the operator carry an owner reference pointing to their parent CR. When the CR is deleted, Kubernetes garbage-collects owned resources automatically.

```go
controllerutil.SetControllerReference(owner, child, scheme)
```

Resources owned by `HomeAssistant`: `StatefulSet`, `Service`, `PVC`.
Resources owned by `HomeAssistantConfiguration`: the generated `ConfigMap`.
Resources owned by `HomeAssistantSecrets`: the aggregated `ConfigMap`.

### Finalizers

`HomeAssistantAutomation`, `HomeAssistantScene`, `HomeAssistantScript`, `HomeAssistantIntegration`, `HomeAssistantFloor`, `HomeAssistantLabel`, `HomeAssistantArea` all use finalizers to clean up their corresponding HA entities before the CR is removed. Cleanup is best-effort — if HA is unavailable, the finalizer is still removed so the CR is not left stuck.

### Config hash annotations

`HomeAssistantSecrets` and `HomeAssistantConfiguration` each write a SHA-256 hash of their generated content into a pod template annotation on the `StatefulSet`. Kubernetes detects the annotation change as a pod template mutation and performs a rolling restart.

```
ha.homeassistant.io/secrets-hash: sha256:abc123...
ha.homeassistant.io/config-hash:  sha256:def456...
```

A `sync.Map`-based debounce guard in `HomeAssistantReconciler` prevents a burst of hash updates from triggering multiple consecutive rollouts.

### Hot-reload with retry

`reload_helpers.go` (`PerformReloadWithRetry`) provides shared logic for automation, scene, and script controllers:

1. `IsComponentLoaded()` — confirms the integration is loaded in HA before attempting reload
2. 3 attempts × 5 s delay, each tagged with a unique `ReloadID` for log correlation
3. Graceful degradation — exhausted retries do not fail the reconciliation; HA will pick up the change on the next restart

### Dependency injection for testing

Reconcilers that call the HA API expose a `NewHAClient` field:

```go
type HomeAssistantAutomationReconciler struct {
    ...
    NewHAClient func(baseURL string) *haclient.Client
}
```

Tests wire in an `httptest.Server` instead of a real HA instance, keeping unit tests fast and hermetic.

### Validating webhooks: choosing where a rule belongs

A new field-validation rule for any CRD has three possible homes: a **CRD schema**
constraint (`Pattern`/`Enum`/`MinLength`, or an in-object `x-kubernetes-validations` CEL
rule), a **`ValidatingWebhook`** (`internal/webhook/v1/`), or the **reconcile loop**
(status condition + `RequeueAfter`). A rule is a good webhook candidate only when it
satisfies all five of the following at once — the more of them it fails, the stronger the
signal that it belongs somewhere else:

1. **No external state beyond the object and, at most, a simple List of its siblings.**
   A webhook may read the object under validation and List/Get other objects through the
   API server (e.g. sibling resources of the same Kind, already held in the manager's
   cache), but it must never make an outbound network call (an HTTP request to Home
   Assistant, a fetch from GitHub) or write anything.
2. **Fast and side-effect-free.** A webhook blocks `kubectl apply` synchronously, inside
   the default ~10s admission timeout. Anything the reconcilers already do through
   `haclient` (reload, Config Flow) is disqualified by definition.
3. **Deterministic, independent of apply ordering.** The outcome must never depend on
   whether some other, related resource happens to exist yet — that concern belongs to
   the reconcile loop's status-and-`RequeueAfter` pattern, never to a hard admission
   reject.
4. **A false reject costs more than a false accept only when the rule can be wrong.**
   When a rule's correctness genuinely depends on state that might still change, the
   webhook must be `failurePolicy: Ignore` and/or return an `admission.Warning` instead of
   rejecting. A hard reject is reserved for rules that are wrong unconditionally.
5. **A cheaper mechanism doesn't already express it.** If the rule can be written as a
   `+kubebuilder:validation:Pattern` or an `x-kubernetes-validations` CEL rule on the
   field itself (e.g. "exactly one of A/B/C must be set" — see `IntegrationValue` in
   `api/v1/homeassistantintegration_types.go`), use that instead: a CRD schema constraint
   is cheaper than a webhook round trip, since it's enforced directly by the API server
   with no call into the operator at all.

**Worked examples from this repository:**

- `spec.id` on `HomeAssistantAutomation`/`Scene`/`Script`, restricted to a safe character
  set → **CRD `Pattern`**, not a webhook (criterion 5 — a constraint on the field itself,
  no need to look at any other object).
- Two `HomeAssistantAutomation` resources colliding on the same effective identifier
  (`spec.id`, or `metadata.name` when unset) for the same `HomeAssistant` → **webhook**
  (`homeassistantautomation_webhook.go`) — needs a List of siblings in the same namespace,
  but that's still a plain cache read, not a network call (criterion 1); a collision is
  always wrong, so rejecting it (rather than only warning) is justified (criterion 4).
  This remains a best-effort check, not a uniqueness guarantee: it reads the manager's
  cache (which can lag a just-written sibling by a short, bounded window) and fails open
  under `failurePolicy: Ignore`, so it catches the common case rather than closing every
  possible race.
- `HomeAssistantConfiguration.spec.recorder` with both `database` and `databaseSecretRef`
  set → **webhook, but a warning, not a reject**
  (`homeassistantconfiguration_webhook.go`) — both fields being set is legitimate,
  already-documented behavior (`databaseSecretRef` takes precedence), so rejecting it
  would break a valid configuration (criterion 4 cutting the other way).
- `spec.homeAssistantRef.name` pointing at a `HomeAssistant` that doesn't exist yet →
  **reconcile loop**, never a webhook (criterion 3 — apply ordering between related
  resources is never guaranteed; a missing referent is a transient
  `WaitingForConfiguration`-style state, not a user error).
- Validating a `HomeAssistantCommunityRepository`'s repository structure against HACS's
  expected layout → **reconcile loop**, never a webhook (criterion 1 — requires fetching
  a tarball from `codeload.github.com`).

## Package structure

```
api/v1/          CRD type definitions (edit here, then make manifests generate)
cmd/main.go            Entry point — registers all controllers
internal/
  controller/          One reconciler per CRD
    reload_helpers.go  Shared hot-reload with retry
    auto_include.go    Auto-inject !include lines into configuration.yaml
    bootstrap_controller.go  HA onboarding state machine
    backup_controller.go     Backup config via WebSocket
    helpers.go         Shared utilities (status, conditions)
  haclient/
    client.go          HTTP + WebSocket client (bootstrap, reload, config entries, backup)
test/
  e2e/                 Ginkgo/Gomega E2E tests (require k3d cluster)
  utils/timeouts.go    Centralised E2E timeouts
charts/
  homeassistant-operator/  Helm chart
config/
  crd/bases/           Generated CRD manifests (do not edit manually)
  rbac/                Generated RBAC manifests
  samples/             Example CRs for all CRDs
```

## HA client (`internal/haclient`)

The `haclient.Client` wraps all communication with Home Assistant:

| Method | Transport | Purpose |
|--------|-----------|---------|
| `CheckHealth` | REST | Health probe |
| `CheckAPIReady` | REST | API readiness gate (401 = ready, 404 = not yet) |
| `PerformBootstrap` | REST | Full onboarding flow |
| `ReloadAutomations/Scenes/Scripts` | REST | Hot-reload after config change |
| `PutAutomation/Scene/Script` | REST | Create or update single entity |
| `DeleteAutomation/Scene/Script` | REST | Delete single entity (finalizer) |
| `IsComponentLoaded` | REST | Pre-reload integration check |
| `ListConfigEntries` / `IsIntegrationConfigured` | REST | Integration state |
| `StartConfigFlow` / `SubmitConfigFlow` | REST | Config Flow registration |
| `RemoveConfigEntry` | REST | Integration removal (finalizer) |
| `GetBackupConfig` / `ConfigureBackup` | WebSocket | Backup schedule management |
| `SendWebSocketCommand` | WebSocket | Generic one-shot WS helper |
