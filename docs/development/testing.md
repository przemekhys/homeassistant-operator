# Testing

*For contributors — how this project is tested and how to run the suites. Not needed to use the operator.*

## Philosophy

**Test behavior, not implementation.** Focus on what the code does, not how it does it.
Quality over coverage — 50% coverage with meaningful tests beats 90% coverage with trivial tests.
Every reconciliation test should verify idempotent behavior.

**What NOT to test:**

- Kubernetes API behaviors (tested by k8s upstream)
- controller-runtime internals (tested by upstream)
- Simple getters/setters without logic
- Generated code (`zz_generated.deepcopy.go`)

## Test Pyramid

```
              ┌─────────────┐
              │    E2E      │  ← Six focused suites, run concurrently (≤10 min total)
              ├─────────────┤
              │    Unit     │  ← Controller logic + pure functions (envtest)
              └─────────────┘
```

**Golden rule**: if you can test it with envtest, don't use E2E.

## Choosing a test level

The golden rule above is a starting point, not the whole decision. Use this
checklist for anything more specific — if any signal below applies, the
behavior belongs in e2e (or a Helm chart test); otherwise it belongs in
unit/integration (envtest).

**Push toward e2e when the behavior...**

- Can only be observed against a real running container or HTTP/WebSocket
  server (e.g. a real Home Assistant instance's actual API response) — no
  Go-level fake or mock reproduces the real service's behavior.
- Depends on a real external controller reconciling something on its own
  schedule — a real Gateway API implementation's `HTTPRoute` acceptance, or
  cert-manager actually issuing a `Certificate` — which envtest's fake API
  server does not simulate (it accepts writes to the Kubernetes API but runs
  no other controllers).
- Validates the Helm chart's install/upgrade path itself (RBAC that only
  takes effect once actually applied by Helm, webhook wiring that depends on
  chart-templated Secrets/Services, CRD schema as shipped in the chart).

**Push toward unit/integration (envtest) when the behavior...**

- Is pure reconciliation logic against the Kubernetes API surface envtest
  already provides (creating/updating child resources, computing status
  conditions, hashing, owner references) — envtest's fake API server models
  this faithfully.
- Can be exercised with the `NewHAClient` dependency-injection pattern (an
  `httptest.Server` standing in for Home Assistant's REST API) rather than a
  real HA container.

**Tiebreaker** for borderline cases (technically reproducible with envtest,
but only via significant custom scaffolding): if reproducing the real
behavior in envtest would require re-implementing a third-party controller's
logic (Gateway API, cert-manager) rather than just calling the Kubernetes API,
that is itself the "e2e" signal — you'd be testing your fake, not the real
integration.

### Worked examples

1. **A `HomeAssistantScript` field that changes what gets written into
   `scripts.yaml`.** → Unit/integration. This is reconciliation logic
   (ConfigMap generation) against the Kubernetes API — envtest covers it
   fully; assert on the generated ConfigMap's content.
2. **A change whose correctness depends on a real Home Assistant
   HTTP/WebSocket API response** (e.g. confirming a `spec.gateway.filters`
   redirect actually changes traffic once reconciled onto a real `HTTPRoute`
   by a real Gateway API implementation, or confirming an automation actually
   hot-reloads via HA's real REST API). → E2e. No fake API server simulates a
   real Gateway API controller's route acceptance or a real HA process's
   config-reload behavior.
3. **A change to RBAC/webhook wiring that only matters once deployed via
   Helm** (e.g. the webhook's `certManager.enabled` fallback path, or a new
   RBAC rule's effect on the shipped `ClusterRole`). → E2e or a Helm chart
   test via `make helm-verify` — this is only observable once the chart is
   actually installed, not from Go code directly.

### A note on coverage trade-offs

Adding e2e coverage is not free — every e2e job runs against a real k3d
cluster and counts against the 10-minute workflow budget (see below). If a
new scenario cannot fit within an existing job's budget even after
considering unit/integration alternatives, it is acceptable to intentionally
not add e2e coverage for it, **as long as that decision is recorded** in the
Coverage Gap Record below rather than the scenario silently going untested.

---

## Unit Tests (envtest)

**Location**: `internal/controller/*_test.go`
**Framework**: Ginkgo v2 + Gomega + envtest (fake API server, no real cluster)

```bash
make test                                          # All unit tests
go test ./internal/controller -run TestName -v    # Specific test
```

### Mock HA API pattern

Controllers for Automation/Scene/Script/Integration have a `NewHAClient` field for dependency injection. Tests replace it with an `httptest.Server`:

```go
mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // handle PUT /api/config/automation/config/...
    w.WriteHeader(http.StatusOK)
}))

reconciler = &HomeAssistantAutomationReconciler{
    NewHAClient: func(_ string) *haclient.Client {
        return haclient.NewClient(mockServer.URL)
    },
}
```

### Key patterns

**Eventually** — async state assertions:
```go
Eventually(func(g Gomega) {
    resource := &hav1alpha1.HomeAssistantAutomation{}
    g.Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
    g.Expect(resource.Status.Ready).To(BeTrue())
}, timeout, interval).Should(Succeed())
```

**Consistently** — assert nothing changed:
```go
Consistently(func(g Gomega) {
    sts := &appsv1.StatefulSet{}
    g.Expect(k8sClient.Get(ctx, stsKey, sts)).To(Succeed())
    _, hasHash := sts.Spec.Template.Annotations["ha.homeassistant.io/secrets-hash"]
    g.Expect(hasHash).To(BeFalse())
}, time.Second*2, interval).Should(Succeed())
```

**Two-phase** — detect changes across reconcile calls:
```go
// Phase 1: create, capture initial hash
reconciler.Reconcile(ctx, req)
var initialHash string
Eventually(func(g Gomega) {
    g.Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
    initialHash = resource.Status.ConfigHash
    g.Expect(initialHash).NotTo(BeEmpty())
}, timeout, interval).Should(Succeed())

// Phase 2: update, verify hash changed
Eventually(func() error {
    Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
    resource.Spec.Configuration = "updated: true"
    return k8sClient.Update(ctx, resource)
}, timeout, interval).Should(Succeed())
reconciler.Reconcile(ctx, req)

Eventually(func(g Gomega) {
    g.Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
    g.Expect(resource.Status.ConfigHash).NotTo(Equal(initialHash))
}, timeout, interval).Should(Succeed())
```

---

## E2E Tests

**Location**: `test/e2e/*_test.go` (8 files, 26 specs total)
**Framework**: Ginkgo v2 + real k3d cluster
**Strategy**: Seven independently-labeled suites, run as seven concurrent
GitHub Actions jobs (`.github/workflows/test-e2e-parallel.yml`), so the whole
workflow — not any single job — targets a **10-minute** budget (see the
Known gap below: not currently met in practice, closer to 15-20 minutes).

**This section is the sole source of truth in this repository for e2e
suite/job duration** (per this project's testing policy). Any other file that
needs to reference how long the suite takes should point here rather than
restating a number.

**Goal**: the whole e2e workflow completes in about 10 minutes, split across
the seven concurrent jobs below.

### Running E2E locally

```bash
make test-e2e-critical-a                 # HomeAssistant + sibling CRDs (11 specs)
make test-e2e-critical-b                 # spec.alpha.devices device passthrough (1 spec)
make test-e2e-tls                        # TLS ingress/gateway/webhook (4 specs)
make test-e2e-network-policy             # NetworkPolicy enforcement (1 spec)
make test-e2e-pod-security                # Pod Security Standards (2 specs)
make test-e2e-community-repository-a     # HACS-style installs, group A (3 specs)
make test-e2e-community-repository-b     # HACS-style installs, group B (4 specs)
```

Each target creates its own fresh k3d cluster (`K3D_MEMORY_E2E=4g` by
default), runs its `ginkgo run --label-filter=...` subset, and tears the
cluster down afterward — mirroring exactly what each CI job does.

Local runs build and use `example.com/homeassistant-operator:v0.0.1` (the
suite's own default), rebuilding the image each time. CI instead builds the
image once (the `build` job), uploads it as an artifact tagged `operator:e2e`,
and every e2e job downloads and loads that same artifact — set
`E2E_SKIP_IMAGE_BUILD=true` and `E2E_IMG=<tag>` to reproduce that
skip-the-rebuild behavior locally against a pre-built image.

### The seven e2e jobs

| Job | Label filter | Specs | What is verified |
|---|---|---|---|
| `e2e-critical-a` | `critical-path && group-a` | 11 | All CRDs' core lifecycle (see table below) — shares one HA bootstrap |
| `e2e-critical-b` | `critical-path && group-b` | 1 | `spec.alpha.devices` device passthrough — own cluster/instance, no shared bootstrap |
| `e2e-tls` | `tls` | 4 | TLS via Ingress, Gateway API, and the validating webhook |
| `e2e-network-policy` | `network-policy` | 1 | `spec.alpha.networkPolicy` actually restricts traffic, not just that the object exists |
| `e2e-pod-security` | `pod-security` | 2 | Operator namespace enforces the `restricted` Pod Security Standard |
| `e2e-community-repository-a` | `community-repository && group-a` | 3 | `HomeAssistantCommunityRepository`: integration + theme install, theme ref-update |
| `e2e-community-repository-b` | `community-repository && group-b` | 4 | `HomeAssistantCommunityRepository`: python_script + template + plugin install, deletion |

`critical-path` and `community-repository` both use the generic
`group-a`/`group-b` (and, if a group ever fills up, `group-c`, ...) label
convention instead of a one-off job name per new spec — a future spec joins
whichever group has time-budget/setup-cost headroom rather than requiring a
brand-new CI job. **The two features are NOT symmetric in how groups share
state**, though: community-repository's group-a and group-b specs live in one
file/one `Ordered` block and share a single `BeforeAll` (their setup cost is
similar either way), while critical-path's group-a (`e2e_critical_path_test.go`)
and group-b (`e2e_device_passthrough_test.go`) are deliberately two separate
files/`Describe` blocks with independent setup — group-a's specs need a fully
real-onboarded HA instance (expensive, worth sharing across many specs),
group-b's don't need onboarding at all (worth keeping cheap and independent).
Adding a new group-a spec means a new `It` in the existing file; adding a new
group-b-shaped spec (no onboarding needed) means either a new `It` in
`e2e_device_passthrough_test.go` or, if its setup is different again, a new
file with the same `critical-path`+`group-b` (or a new group) labels.

The community-repository split (not an arbitrary half-and-half) keeps two
spec pairs together: "keeps installedVersion..." reuses the CR created by the
theme-install spec, and "removes the ConfigMap entry..." reuses the CR
created by the python_script-install spec — each pair must run in the same
Ginkgo process since separate CI jobs use separate clusters and cannot see
each other's resources.

`e2e-critical-b` exists because a first attempt folded its one spec into
`e2e-critical-a` (then still named `e2e-critical-path`) as an 11th spec, to
avoid a new CI job, instead of giving it its own job from the start. A real CI
run showed the combined job hitting its `timeout-minutes` and getting
cancelled outright — no Ginkgo failure output, no diagnostic artifacts, since
`if: failure()` steps don't run on cancellation. Splitting it into its own job
(its bootstrap skips `spec.bootstrap` entirely, so it's cheaper to stand up
than group-a's real-onboarding instance) both removed the time pressure from
`e2e-critical-a` and made a future failure of this spec actually diagnosable
instead of silently killed.

**Known gap**: real CI runs showed every job's cold-start overhead — and the
community-repository specs' own runtime — running noticeably longer than
initial estimates (extrapolated from a single long-running, cache-warm job)
suggested, so per-job timeouts have been progressively widened rather than
left to fail: `e2e-community-repository-b` up to 16 min, `-a` up to 14 min,
`e2e-tls` up to 11 min. **The whole workflow does not currently meet the
10-minute goal** — with `build` (a few minutes) plus the slowest job
(`e2e-community-repository-b`), real end-to-end time is closer to 15-20
minutes. Tightening this back down needs either genuine optimization (e.g.
the per-spec activation-confirmation polling in community-repository, or the
"Load Home Assistant image" step's own variability) or accepting a revised,
honest target — not just more timeout increases.

### `e2e-critical-a` tests (11 specs)

| # | CRD | What is verified |
|---|-----|-----------------|
| 1 | `HomeAssistant` | Pod running, Service created, bootstrap completed |
| 2 | `HomeAssistantConfiguration` | ConfigMap generated, hot-reload on config change |
| 2b | `HomeAssistantConfiguration` (`http:`) | `http:` delivered via the HA http config API, omitted from `configuration.yaml`, no hash churn on repeated reconciles |
| 3 | `HomeAssistantSecrets` | Secret aggregated, hash annotation set |
| 4 | `HomeAssistantAutomation` | PUT to REST API, reload, DELETE via finalizer |
| 5 | `HomeAssistantScene` | PUT to REST API, reload, DELETE via finalizer |
| 6 | `HomeAssistantScript` | PUT to REST API, reload, DELETE via finalizer |
| 7 | `HomeAssistantIntegration` | Config Flow started, `entryID` stored in status |
| 8 | `HomeAssistantFloor` | Created via WebSocket registry API, deleted |
| 9 | `HomeAssistantLabel` | Created via WebSocket registry API, deleted |
| 10 | `HomeAssistantArea` | Created via WebSocket registry API, deleted |

This job's specs share one Home Assistant bootstrap (real onboarding) and run
sequentially (`Ordered`), continuing even if one fails (`ContinueOnFailure`)
so later CRDs are still exercised.

The `http:` spec (2b) was added here rather than as a new job because it reuses
the shared bootstrap and adds only a few seconds — unlike `e2e-critical-b`,
whose spec needs its own instance. If this job starts brushing its
`timeout-minutes` after the addition, move spec 2b to `group-b` (or a new group)
per the group convention above rather than widening the timeout.

### `e2e-critical-b` tests (1 spec)

| # | CRD | What is verified |
|---|-----|-----------------|
| 1 | `HomeAssistant` (`spec.alpha.devices`) | Device mounted without `privileged: true` (`/dev/null`/`/dev/zero` stand-ins), missing device surfaced via `DevicesReady` |

Originally folded into `e2e-critical-a` as an 11th spec to avoid a new CI
job, but a real CI run showed the combined job exceeding its `timeout-minutes`
and getting cancelled outright — no Ginkgo failure output, no diagnostic
artifacts (`if: failure()` steps don't run on cancellation). Split into its
own job/cluster instead: it skips `spec.bootstrap` entirely (the readiness
probe only needs HTTP 200 on `/`, which HA serves before onboarding), so its
own bootstrap cost is much lower than critical-path's real-onboarding
instance, and its final step is free to leave the instance in a broken state
(an intentionally unmountable device, to exercise the missing-device
diagnostics) without affecting any other spec.

## Coverage Gap Record

Every remaining e2e scenario is still verified — the 26 specs above are
split across the seven jobs above. This section exists as the place to
record it when a scenario is deliberately not e2e-gated, rather than fitting
it into an existing (or new) job:

| Scenario | Why not gating | Where (if anywhere) it's still verified |
|---|---|---|
| `spec.scheduling` (nodeSelector/affinity/tolerations actually influencing real placement) | An e2e job for this was built and run successfully, then deliberately removed: every piece of this operator's own logic (field copy onto the pod template, rollout-on-change diffing, the `SchedulingReady` condition mirroring the pod's own `PodScheduled` condition, admission validation) is already covered by envtest without a real scheduler. The only thing a real cluster adds is confirming that Kubernetes' own scheduler honors `nodeSelector`/affinity/taints — a stable, heavily-tested upstream API contract, not something specific to this operator (unlike e.g. device passthrough's hostPath mount, where non-`privileged` access to a device node is a container-runtime-default assumption, not a documented Kubernetes guarantee, and genuinely needs a real kubelet to confirm). | `internal/controller/scheduling_test.go` and `internal/webhook/v1/admission_envtest_test.go` (both envtest, real API server) |
| Operator's own `allow-webhook-traffic` NetworkPolicy actually unblocking admission-webhook traffic under live CNI enforcement | A live e2e assertion of the *allow* transition was attempted and abandoned: direct inspection of a dev k3d node's `iptables`/`ipset` state confirmed the CNI translates the policy into a textbook-correct rule (right ipset membership, right port, right chain wiring), but the live connection kept failing regardless of the correct rule, for reasons not conclusively identified (suspected same-node/bridge-netfilter interaction specific to that one machine's k3d networking setup, not this operator's own logic — the *block* side, i.e. traffic genuinely denied without the label, reproduced correctly and reliably on the same machine). The remaining uncertainty is about `NetworkPolicy`/CNI enforcement itself — a stable upstream contract this operator doesn't implement — not about whether the shipped rule is correctly shaped, which is what the static check below actually proves. | `hack/verify-network-policy.sh` (`make verify-network-policy`, no cluster) — asserts the rendered `NetworkPolicy` resources have the correct `podSelector`/port/namespace-label shape across every install path (kustomize and Helm); confirmed to fail when the rule is removed. |
| Transitional cleanup after the removed native TLS feature (an upgraded instance that had `spec.alpha.tls` reverting to HTTP: orphaned `Certificate` deleted, obsolete `TLSReady` condition stripped, one `NativeTLSRemoved` warning event) | The whole feature being removed had one e2e spec; keeping (or adding) an e2e for its *removal* would need a cluster running the previous operator version, a real cert-manager issuance and an operator swap — expensive setup for a one-shot code path that is itself scheduled for deletion in a later minor. Every branch of the cleanup (delete-if-present, condition removal gated on `certManagerRequired`, exactly-one-event, idempotent re-run, silence for instances that never used the feature) is covered by `TestReconcileNativeTLSRemoval`. The remaining gap is only the one-line wiring into `Reconcile()`. | `internal/controller/tls_helpers_test.go` (`TestReconcileNativeTLSRemoval`, controller-runtime fake client) |
| `http:` YAML fallback path on a Home Assistant *older than 2026.8* (no http config API — the section keeps going into `configuration.yaml`, `status.httpConfigSource` = `Yaml`, no writes attempted) | The e2e job's shared Home Assistant image is pinned at 2026.8+, which has the API. Pinning a second, older image just for this one assertion would double the image-load time of `e2e-critical-a` for a branch that is pure "do nothing new". The path decision (`unknown_command` → YAML, no `configure`/`promote`, `ManagedInYaml` condition) and the API↔YAML switch across a version change — decided fresh every reconcile with no carried state — are covered against a stubbed Home Assistant. | `internal/controller/http_config_test.go` (`TestReconcileHTTPConfig_YAMLPath`, `TestReconcileHTTPConfig_PathSwitchIsStateless`) and `internal/haclient/http_config_test.go` (`TestGetHTTPConfig_Unsupported`) |
