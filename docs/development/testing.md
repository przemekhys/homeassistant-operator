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

## Helm install and upgrade E2E

Run `make helm-test-e2e` with Docker, k3d, Helm, kubectl, jq and an authenticated
GitHub CLI (`gh auth login`, or `GH_TOKEN` in CI). The upgrade scenario installs
the most recently published stable GitHub Release from the OCI chart registry
(N-1), then upgrades it to the working-tree chart and an operator image built
from the current source (unreleased N). Drafts, prereleases and unpublished git
tags are excluded. Release discovery errors fail the test.

The version in the committed `Chart.yaml` is a development placeholder; the
release workflow replaces it when publishing. It does not select the upgrade
baseline or the source used to build the target operator.

For an explicit baseline or an offline discovery override, run
`HELM_N1_VERSION=1.6.1 make helm-test-e2e` (the published chart and images still
need to be accessible). Run `bash hack/test-lib-helm.sh` for the network-free
release-selection regression checks.

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

**Location**: `test/e2e/*_test.go` (8 files, 27 specs total)
**Framework**: Ginkgo v2 + real k3d cluster
**Strategy**: Six independently-labeled suites, run as six concurrent
GitHub Actions jobs (`.github/workflows/test-e2e-parallel.yml`), so the whole
workflow — not any single job — targets a **10-minute** budget (see the
Known gap below for historical CI timings and the pending consolidated baseline).

**This section is the sole source of truth in this repository for e2e
suite/job duration** (per this project's testing policy). Any other file that
needs to reference how long the suite takes should point here rather than
restating a number.

**Goal**: the whole e2e workflow completes in about 10 minutes, split across
the six concurrent jobs below.

### Running E2E locally

```bash
make test-e2e-critical-a                 # HomeAssistant + sibling CRDs (15 specs)
make test-e2e-critical-b                 # spec.alpha.devices device passthrough (1 spec)
make test-e2e-tls                        # TLS ingress/gateway/webhook (4 specs)
make test-e2e-network-policy             # NetworkPolicy enforcement (1 spec)
make test-e2e-pod-security                # Pod Security Standards (2 specs)
make test-e2e-community-repository       # All HACS-style categories and lifecycle (4 specs)
```

Each target creates its own fresh k3d cluster (`K3D_MEMORY_E2E=12g` by
default; CI uses 4g), runs its `ginkgo run --label-filter=...` subset, and tears the
cluster down afterward — mirroring exactly what each CI job does.

Local runs build and use `example.com/homeassistant-operator:v0.0.1` (the
suite's own default), rebuilding the image each time. CI instead builds the
image once (the `build` job), uploads it as an artifact tagged `operator:e2e`,
and every e2e job downloads and loads that same artifact — set
`E2E_SKIP_IMAGE_BUILD=true` and `E2E_IMG=<tag>` to reproduce that
skip-the-rebuild behavior locally against a pre-built image.

### The six e2e jobs

| Job | Label filter | Specs | What is verified |
|---|---|---|---|
| `e2e-critical-a` | `critical-path && group-a` | 15 | All CRDs' core lifecycle (see table below) — shares one HA bootstrap |
| `e2e-critical-b` | `critical-path && group-b` | 1 | `spec.alpha.devices` device passthrough — own cluster/instance, no shared bootstrap |
| `e2e-tls` | `tls` | 4 | TLS via Ingress, Gateway API, and the validating webhook |
| `e2e-network-policy` | `network-policy` | 1 | `spec.alpha.networkPolicy` actually restricts traffic, not just that the object exists |
| `e2e-pod-security` | `pod-security` | 2 | Operator namespace enforces the `restricted` Pod Security Standard |
| `e2e-community-repository` | `community-repository` | 4 | `HomeAssistantCommunityRepository`: integration download retry and dependent config flow recovery, alpha rejection, all four hot-reload categories, theme ref-update and python_script deletion — one shared HA bootstrap |

`critical-path` uses the generic `group-a`/`group-b` (and, if a group ever
fills up, `group-c`, ...) label convention. Its group-a
(`e2e_critical_path_test.go`) and group-b (`e2e_device_passthrough_test.go`)
are separate files/`Describe` blocks with independent setup: group-a needs a
fully onboarded HA instance, while group-b does not need onboarding. New
specs should join whichever group has suitable setup and time-budget headroom.

Community repositories run in one `Ordered` block and one job. The integration
scenario first verifies init-container retry, the required pod replacement,
and recovery of a dependent `HomeAssistantIntegration`. Then theme,
python_script, template, and plugin CRs are all created before waiting for
their activation and files, so the production activation settle delays and
ConfigMap propagation overlap. Each category is checked, including Lovelace
registration, and the pod must not be replaced or restarted by these installs.
Finally, the theme ref update and python_script deletion are started before
waiting for either, checking both the updated file and removal of the deleted
entry and file. These four specs retain the scenarios previously spread over
eight specs in two jobs, without duplicating the cluster and HA bootstrap.
The local target passes `HA_VERSION` to Ginkgo so HA uses the same pinned image
that setup imported into the cluster.

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

**Known gap**: the whole workflow's 10-minute goal is not yet verified after
consolidating community repositories. Before consolidation, successful CI run
[40181823757](https://github.com/przemekhys/homeassistant-operator/actions/runs/40181823757)
took about 17 min 40 s overall: community-repository A took 11 min 20 s and B
12 min 39 s, including their separate setup/cleanup. The consolidated job
overlaps activation and propagation waits and uses one bootstrap; its timeout
retains headroom (18 min for the job, 15 min for Ginkgo) until cold-run CI
measurements establish the new baseline. These limits are failure budgets,
not measured durations. Image import and the shared `build` job also count
toward the workflow's total duration.

### `e2e-critical-a` tests (15 specs)

| # | CRD | What is verified |
|---|-----|-----------------|
| 0 | `HomeAssistant` bootstrap | Dedicated runtime principal isolated from bootstrap access; bootstrap access retired |
| 1 | `HomeAssistant` | Pod running, Service created, bootstrap completed |
| 1b | `HomeAssistantDashboard` | Inline dashboard created and updated |
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
| 11 | Operator account recovery | Credential loss requires explicit recovery; observed requests are not replayed |
| 12 | Legacy account adoption | Collisions reported; original token retained until deliberate cleanup |

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

Every remaining e2e scenario is still verified — the 27 specs above are
split across the six jobs above. This section exists as the place to
record it when a scenario is deliberately not e2e-gated, rather than fitting
it into an existing (or new) job:

| Scenario | Why not gating | Where (if anywhere) it's still verified |
|---|---|---|
| `spec.scheduling` (nodeSelector/affinity/tolerations actually influencing real placement) | An e2e job for this was built and run successfully, then deliberately removed: every piece of this operator's own logic (field copy onto the pod template, rollout-on-change diffing, the `SchedulingReady` condition mirroring the pod's own `PodScheduled` condition, admission validation) is already covered by envtest without a real scheduler. The only thing a real cluster adds is confirming that Kubernetes' own scheduler honors `nodeSelector`/affinity/taints — a stable, heavily-tested upstream API contract, not something specific to this operator (unlike e.g. device passthrough's hostPath mount, where non-`privileged` access to a device node is a container-runtime-default assumption, not a documented Kubernetes guarantee, and genuinely needs a real kubelet to confirm). | `internal/controller/scheduling_test.go` and `internal/webhook/v1/admission_envtest_test.go` (both envtest, real API server) |
| Operator's own `allow-webhook-traffic` NetworkPolicy actually unblocking admission-webhook traffic under live CNI enforcement | A live e2e assertion of the *allow* transition was attempted and abandoned: direct inspection of a dev k3d node's `iptables`/`ipset` state confirmed the CNI translates the policy into a textbook-correct rule (right ipset membership, right port, right chain wiring), but the live connection kept failing regardless of the correct rule, for reasons not conclusively identified (suspected same-node/bridge-netfilter interaction specific to that one machine's k3d networking setup, not this operator's own logic — the *block* side, i.e. traffic genuinely denied without the label, reproduced correctly and reliably on the same machine). The remaining uncertainty is about `NetworkPolicy`/CNI enforcement itself — a stable upstream contract this operator doesn't implement — not about whether the shipped rule is correctly shaped, which is what the static check below actually proves. | `hack/verify-network-policy.sh` (`make verify-network-policy`, no cluster) — asserts the rendered `NetworkPolicy` resources have the correct `podSelector`/port/namespace-label shape across every install path (kustomize and Helm); confirmed to fail when the rule is removed. |
| `http:` YAML fallback path on a Home Assistant *older than 2026.8* (no http config API — the section keeps going into `configuration.yaml`, `status.httpConfigSource` = `Yaml`, no writes attempted) | The e2e job's shared Home Assistant image is pinned at 2026.8+, which has the API. Pinning a second, older image just for this one assertion would double the image-load time of `e2e-critical-a` for a branch that is pure "do nothing new". The path decision (`unknown_command` → YAML, no `configure`/`promote`, `ManagedInYaml` condition) and the API↔YAML switch across a version change — decided fresh every reconcile with no carried state — are covered against a stubbed Home Assistant. | `internal/controller/http_config_test.go` (`TestReconcileHTTPConfig_YAMLPath`, `TestReconcileHTTPConfig_PathSwitchIsStateless`) and `internal/haclient/http_config_test.go` (`TestGetHTTPConfig_Unsupported`) |
