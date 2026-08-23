# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).


## [Unreleased]

### Changed

- **`spec.alpha.tls.native` certificate rotation now uses Home Assistant's WebSocket `http/config` API when available, instead of `configuration.yaml`** — recent Home Assistant core versions migrate the `http:` integration to an internal, UI-managed config store and silently ignore the YAML block afterward, which meant native TLS certificate rotation stopped taking effect (while `TLSReady` continued reporting success). The operator now applies a new certificate through `http/config`/`http/config/configure`/`http/config/promote` whenever HA supports it — verifying HA is reachable over HTTPS with the new certificate before confirming it, and never forcing a pod restart on this path, since HA restarts its own process as part of applying the new config. Instances on an older HA core version, or not yet finished bootstrapping, keep using the previous YAML-injection-plus-pod-restart mechanism unchanged; which path applies is re-checked on every reconcile, so an HA upgrade is picked up automatically. If HA rejects a new certificate, it reverts to the previous one on its own within a few minutes — the operator observes this (new `TLSConfigReverted` condition reason and a warning Event) rather than retrying the same rejected configuration in a loop; Home Assistant is never left permanently unavailable by a bad rotation. This is a mechanism-only change: `spec.alpha.tls.native`'s fields are unchanged, no CRD/RBAC changed, and existing bring-your-own-Secret and cert-manager-issued setups behave identically. See `docs/user-guide/tls.md` for the new `TLSReady` reason table.
  **Follow-up in the same release: this WebSocket-based mechanism now covers every `HomeAssistant` instance, not only ones with native TLS enabled** — HA's YAML-to-storage migration silently drops the *entire* `http:` section for every instance, not just the TLS-related keys, so an instance that only ever set `ip_ban_enabled`, `cors_allowed_origins`, `login_attempts_threshold`, `ssl_profile`, `use_x_frame_options`, `server_host`, or `ssl_peer_certificate` in its `http:` block (no native TLS at all) was silently stuck on whatever those values were at first boot. The operator now parses these fields out of the already-generated `configuration.yaml` and pushes changes through the same `http/config/configure` call, merged with any TLS fields when native TLS is also enabled (one call, never two competing ones). No new CRD field: values still come from `HomeAssistantConfiguration.spec.configuration`'s `http:` block, exactly as written. An unparseable `http:` block (e.g. one using an `!include` tag) falls back to leaving that reconcile's `http:` handling to the legacy YAML path rather than guessing at partial settings. On an instance without native TLS this runs silently — there's no new status condition, since `TLSReady` only means something once TLS is actually requested.
- **E2E test suite optimization** — CI e2e tests now run faster; see `docs/development/testing.md` (and `.claude/TESTING.md` for AI guidance) for details.

### Added

- **Admission-time validation for colliding automation/scene/script identifiers, and a recorder-configuration warning** — `HomeAssistantAutomation`, `HomeAssistantScene`, and `HomeAssistantScript` each gained their own validating webhook: creating or updating a resource whose effective identifier (`spec.id`, or `metadata.name` when `spec.id` is left empty) collides with a sibling of the same kind already targeting the same `HomeAssistant` instance is now rejected immediately, naming the conflicting resource. Previously this was completely undetected — the two resources would silently overwrite each other in Home Assistant's `automations.yaml`/`scenes.yaml`/`scripts.yaml`, both still reporting `Ready`. Separately, `HomeAssistantConfiguration` now warns (without rejecting) when `spec.recorder` sets both `database` and `databaseSecretRef`, naming `databaseSecretRef` as the value that takes effect — this precedence already existed, but was previously invisible at apply time. `HomeAssistantAutomation.spec.id` and `HomeAssistantScene.spec.id` are now restricted to the same safe character set (`^[a-z][a-z0-9_]*$`) `HomeAssistantScript.spec.id` already enforced, closing a three-way inconsistency between the sibling kinds. **This is a breaking change for existing resources**: an already-applied `HomeAssistantAutomation` or `HomeAssistantScene` whose `spec.id` contains uppercase letters or hyphens keeps working after the CRD upgrade, but its *next* update (even an unrelated field) will be rejected by the tightened schema until `spec.id` is renamed to a conforming value — rename any such identifiers before upgrading, or be prepared to do so before the next edit. All new checks are best-effort (`failurePolicy: Ignore`), matching the existing `HomeAssistant` webhook, and require no new RBAC permissions.

- **Device passthrough for Zigbee/Z-Wave coordinators (alpha)** — new `spec.alpha.devices` field lets a `HomeAssistant` resource declare host device nodes (e.g. `/dev/ttyACM0` for a Conbee2/SkyConnect/Z-Wave USB coordinator) to mount into the Home Assistant container, so integrations like Zigbee2MQTT, Z-Wave JS, or ZHA can open the serial port — previously this required a manual StatefulSet edit that the operator would silently revert on the next reconcile. Each entry (`hostPath`, optional `containerPath`) is mounted as a `hostPath` volume typed as a character device; the operator never sets `privileged: true` for this — the container already runs with enough default capability to open a root-owned device node. A resource declaring no devices is completely unaffected (no security-context or volume changes). Malformed paths (empty, not rooted under `/dev`, containing `..`) and duplicate `hostPath` entries are rejected by the validating webhook before ever reaching a pod. When a declared device is missing on the node the pod is scheduled to, the new `DevicesReady` status condition names the offending path, diagnosable directly from `kubectl describe homeassistant`. This changes the pod's security context, so it starts under `spec.alpha`; it does not itself pin the pod to the right node — use the new `spec.scheduling.nodeSelector` (see below) for that.

- **Default trusted proxies for Ingress/Gateway exposure** — when `spec.ingress.enabled` or `spec.gateway.enabled` is `true`, the operator now automatically injects `http.use_x_forwarded_for: true` and `http.trusted_proxies` (the RFC1918 ranges `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) into the generated `configuration.yaml`, so Home Assistant no longer rejects every request through the exposed endpoint with `400 Bad Request` until these are set by hand. No defaults are injected while exposure is disabled. The real cluster pod/service CIDR can't be reliably read from the Kubernetes API, so this is a conservative, commonly-correct default rather than autodetection. Each key is added independently and only when missing — a value you already set yourself in `HomeAssistantConfiguration` is never overridden, and an externally managed `http:` block (e.g. `http: !include http.yaml`) is left completely untouched — and the new `spec.disableDefaultTrustedProxies` field opts a `HomeAssistant` instance out entirely for clusters where the default doesn't apply (for example, to set narrower `trusted_proxies` matching your actual Ingress/Gateway proxy instead of the broad RFC1918 ranges). Injecting, changing, or removing these keys follows the existing hot-reload classification (no forced pod restart), and the `HomeAssistant`'s `ExposureReady` condition message reports which of the three states applies (defaults applied / user-configured / opted out).

- **Gateway API route filters (`spec.gateway.filters`)** — the `HomeAssistant` CRD's `spec.gateway` now supports declaring HTTP route-level behaviors on the operator-managed `HTTPRoute`: request/response header modification, redirects (e.g. enforcing HTTPS), and URL rewrites — mirroring upstream Gateway API's own `HTTPRouteFilter` field names/shape so existing Gateway API knowledge transfers directly (`RequestMirror` and `ExtensionRef` are intentionally out of scope). Filters are validated at admission time by the existing validating webhook: an unknown filter type, a missing or mismatched sub-object for the declared type, or an all-empty filter are all rejected with a message naming the problem, before ever reaching the cluster. Changing filters never restarts the Home Assistant pod (route exposure is fully decoupled from the pod template hash), and omitting `filters` entirely leaves the managed route byte-for-byte unchanged from today's behavior.

- **Pod scheduling controls (`spec.scheduling`)** — new field lets a `HomeAssistant` resource declare `nodeSelector`, `affinity` (node and pod affinity/anti-affinity — one field, matching Kubernetes' own `Affinity` shape), `tolerations`, and `priorityClassName`, all copied verbatim onto the generated pod template. Closes the node-placement gap left open by `spec.alpha.devices`: a USB Zigbee/Z-Wave coordinator only exists on one specific node, and until now nothing kept the pod from being scheduled elsewhere. Every field is optional; a resource that sets none of them continues to schedule exactly as before. Editing a scheduling field on an already-running instance triggers a pod recreation so the change actually takes effect (Kubernetes only evaluates these fields at pod creation). A new `SchedulingReady` status condition mirrors the pod's own built-in `PodScheduled` condition, so an unsatisfiable `nodeSelector`/`affinity` combination is diagnosable directly from `kubectl describe homeassistant`. `priorityClassName` is validated against real `PriorityClass` objects at admission time — a nonexistent one is rejected immediately rather than surfacing later as an opaque StatefulSet failure.

- **HACS-compatible community repository installs (alpha)** — new `HomeAssistantCommunityRepository` CRD (`ha.homeassistant.io/v1alpha1`, short name `hacr`) declaratively installs community extensions the same way [HACS](https://hacs.xyz/) does, **without requiring HACS or its UI to be installed**. Supports the 5 HACS categories that don't require a separate runtime: `integration`, `plugin`, `theme`, `python_script`, and `template` (`appdaemon`/`netdaemon` are out of scope — they need their own runtime container this operator does not deploy). `spec.repository` (`owner/repo`) and `spec.ref` (tag/branch/commit, explicitly pinned — no "latest" tracking) are fetched directly from GitHub (`codeload.github.com`, no `git` binary or authenticated REST API dependency) and validated against the category's expected HACS structure before anything is installed. Conflict detection is stronger than HACS's own: two resources that would resolve to the same `(category, resolvedTarget)` on the same `HomeAssistant` instance are rejected rather than silently overwriting each other. Activation matches HACS's own per-category mechanism — a pod restart only for `integration` (needed because Python components are imported at HA startup), hot-reload via HA's own service calls for `theme`/`python_script`/`template`, and Lovelace resource registration for `plugin` (with a `plugin` YAML-dashboard-mode edge case reported informationally, not as a failure, since HA itself has no API to register a resource in that mode). A failed update never breaks the previously working installation: `status.installedVersion` only changes once the new version is fully confirmed active. Files are materialized inside the Home Assistant pod by a new init-container (`integration` only, since it must be present before HA starts) and a lightweight sidecar (the other four categories, polling every ~30s) — both reuse the existing HA image and are only added to the pod spec when at least one `HomeAssistantCommunityRepository` actually targets that instance, so the change is invisible to every existing installation. As with all `spec.alpha`/`v1alpha1` surfaces, this CRD carries no API stability guarantee between releases.

## [v1.2.0] - 2026-07-24

### Added

- **Helm chart is now generated from a single source of truth** — the chart's static parts (all 10 CRDs and the operator RBAC rules) are generated from the authoritative Kustomize sources under `config/` via `make helm-sync`, so the Helm and Kustomize install paths can no longer physically drift. New CI gates (`make helm-verify`) block a PR that leaves the chart out of sync (`verify-chart-sync`), that lets the two render paths diverge on RBAC / securityContext / PSA labels (`verify-equivalence`), or that broadens the operator's RBAC versus the previous release without an explicit justification in `hack/rbac-allowlist.txt` (`verify-rbac-upgrade`, enforcing the minimal-RBAC principle).
- **Helm chart test layers** — the chart now ships with `values.schema.json` (install-time validation of `values.yaml` types/enums), `helm-unittest` template unit tests under `charts/homeassistant-operator/tests/`, an auto-generated parameter table in `README.md` via `helm-docs` (with a CI drift gate), a k3d end-to-end job covering a fresh install **and** an upgrade from the previous release (N-1 → latest, including the explicit CRD apply step), and a post-publish smoke test that installs the exact published OCI artifact and, on failure, raises a maintainer alert and marks the release "needs verification" without yanking the artifact. See the new `.github/workflows/test-helm.yml` and the `##@ Helm` targets in the Makefile.

- **cert-manager TLS integration — foundation (opt-in)** — groundwork for issuing TLS certificates via cert-manager for three modes: native TLS in Home Assistant, Ingress / API Gateway exposure, and the operator's webhook. New opt-in API fields land as `spec.ingress.tls.issuerRef` and `spec.gateway` (`enabled`, `host`, `issuerRef`, `secretName`, `parentRef`, `manageGateway`) at the stable top level, plus `spec.alpha.tls.native` (`enabled`, `issuerRef`, `dnsNames`, `secretName`) under `spec.alpha` — native TLS changes how Home Assistant serves traffic and how the operator connects to it, so it follows the experimental `spec.alpha` lifecycle, while Ingress/Gateway only manage sibling routing resources and are stable opt-ins. All modes share an `IssuerReference` (the operator only *references* an existing `Issuer`/`ClusterIssuer`, never creates issuers). The operator now **detects cert-manager at runtime** (via the API RESTMapper, cached with a 60s TTL) and **never installs it and never requires it**: when a TLS mode is requested but cert-manager is absent, the resource reports `CertManagerAvailable=False`/`TLSReady=Unknown`, emits a `CertManagerUnavailable` event, and keeps Home Assistant fully functional over HTTP without erroring or looping — a later cert-manager install is picked up automatically. Narrow RBAC for `cert-manager.io/certificates`, `networking.k8s.io/ingresses`, and `gateway.networking.k8s.io/{httproutes,gateways}` was added. Implemented on top of this gate: cert-manager `Certificate` provisioning for all three modes; native TLS runtime (Secret mounted into the HA pod at `/config/ssl`, `http.ssl_certificate`/`ssl_key` injected, and the operator→HA connection switched to HTTPS with `ca.crt` trust — never `InsecureSkipVerify`, all gated on a single readiness predicate so HA and the operator flip together); operator-managed Ingress and Gateway API (HTTPRoute/Gateway) exposure; and a validating admission webhook, **enabled by default (opt-out)**, whose serving certificate is **self-managed by the operator** (self-signed, auto-rotated, CA injected into its own `ValidatingWebhookConfiguration` via [cert-controller](https://github.com/open-policy-agent/cert-controller)) — so the webhook needs **no cert-manager**; cert-manager can still issue the serving certificate as an opt-in (`webhook.certManager.enabled=true`). Cluster end-to-end validation is the remaining follow-up. The `spec.alpha.tls.native` field is off by default and may change or be removed without a deprecation notice.

### Security

- **Signed releases (keyless, Sigstore/cosign)** — starting with **v1.2.0**, the release pipeline signs the container image (full multi-arch manifest), the Helm chart OCI artifact, and a `checksums.txt` bundle attached to a newly-created GitHub Release for each version tag. Signing is **keyless**: it uses the release workflow's own GitHub Actions OIDC identity via Sigstore (Fulcio short-lived certificates + a public Rekor transparency-log entry) — no long-lived key is generated, stored, or rotated by the maintainer. Verification needs only public information; see the [Signed Releases guide](https://przemekhys.github.io/homeassistant-operator/user-guide/signed-releases/) for the `cosign verify`/`verify-blob` commands and `hack/verify-signatures.sh` for a one-command local check. A sample Kyverno `ClusterPolicy` (`hack/kyverno/verify-homeassistant-operator-image.yaml`, tested via `kyverno test` in CI) lets cluster operators enforce this at admission time for the container image; this is entirely opt-in and does not change the default Helm chart install. Releases published before v1.2.0 are not signed.

- **Hardened the operator container securityContext in the Kustomize path** — `config/manager` now sets `readOnlyRootFilesystem: true`, `runAsUser: 65532`, and `runAsGroup: 65532` on the controller-manager container, matching the hardening the Helm chart already applied. This was surfaced by the new Kustomize↔Helm equivalence gate, which found the Kustomize install path shipped a weaker container securityContext than Helm.

- **Opt-in NetworkPolicy for the Home Assistant pod (alpha)** — new `spec.alpha.networkPolicy.enabled` field (default `false`). When enabled, the operator creates a `NetworkPolicy` that restricts ingress to the HA pod to the same namespace and the operator's own namespace on the Service port, while leaving egress unrestricted (HA needs broad, unpredictable egress to IoT devices, cloud APIs, and MQTT brokers). The operator only manages `NetworkPolicy` objects it owns (via controller reference), so a pre-existing policy of the same name is left untouched. The operator-namespace ingress peer is added only when the controller knows its own namespace via the `OPERATOR_NAMESPACE` env var (set by the shipped manifests); otherwise it is omitted with a warning. Being an `spec.alpha` feature, it is off by default and may change or be removed without a deprecation notice.

- **Restricted Pod Security Standard enforced on the operator namespace** — the shipped manifests now label the operator's own namespace with `pod-security.kubernetes.io/{enforce,audit,warn}=restricted` (version `latest`), so the controller-manager pod runs under and is enforced at the strictest Pod Security profile. The pod's `securityContext` already satisfied `restricted` (runs as non-root, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, all capabilities dropped); this change adds enforcement plus a `make verify-pss` CI check — covering **both** the Kustomize (`config/default`) and Helm render paths — that fails if either regresses. For Helm, enforcement is opt-in via the new `namespace.create=true` value (install without `--create-namespace` so the chart owns and labels the namespace); the operator pod is restricted-compliant regardless. Scope is the **operator only** — Home Assistant workload pods run in their own namespaces and are unaffected (they often need elevated privileges such as `hostNetwork` or USB/Zigbee device access that `restricted` would block). On clusters without Pod Security Admission the labels are inert, so installation never fails.

## [v1.1.0] - 2026-07-05

### Added

- **Helm chart: `watchNamespaces`** — new `values.yaml` field (default `[]`) restricts the operator to watching only the listed namespaces. When set, generates per-namespace `RoleBinding` objects instead of a cluster-wide `ClusterRoleBinding`, reducing the operator's blast radius to only the namespaces it needs. Set `WATCH_NAMESPACES` env var is injected automatically. Backwards compatible — empty list (default) preserves the existing `ClusterRoleBinding` behaviour.

- **IP ban self-recovery via init-container** — when the operator's IP is banned by Home Assistant (HTTP 403), the operator now deletes the HA pod so it restarts with a new `unban-operator-ip` init-container. The init-container (using the same HA image already cached on the node) runs an idempotent Python script that removes the operator's IP from `/config/ip_bans.yaml` before HA starts. No `pods/exec` RBAC permission needed. Sliding window protection: at most 3 pod restarts within 30 minutes; once the limit is reached the `BanRecoveryFailed=True` condition is set and manual intervention is required. The window resets automatically on a successful HA connection. Requires the `POD_IP` downward-API env var on the operator Deployment (set by default in the Helm chart).

### Security

- **Removed `pods/exec` RBAC permission** — the operator no longer requires `create` on `pods/exec`. The unban flow uses pod deletion + init-container instead of exec-into-pod. Run `make manifests` to regenerate `config/rbac/role.yaml`.

- **Narrowed Secret RBAC** — removed unused `patch` verb from the `homeassistantsecrets` controller; moved `delete` exclusively to the `homeassistantconfiguration` controller which is the only one that deletes Secrets. All operator-managed Secrets now carry the `app.kubernetes.io/managed-by: homeassistant-operator` label for auditing and Kyverno policies.

### Deprecated

- **ClusterRoleBinding mode** (`watchNamespaces: []`) — deprecated since v1.1.0, planned removal in v2.0.0. See [DEPRECATIONS.md](https://github.com/przemekhys/homeassistant-operator/blob/main/DEPRECATIONS.md) for migration instructions.

## [v1.0.1] - 2026-06-21

### Changed

- **Dependency upgrades** — Go 1.26.4 (fixes CVEs GO-2026-5037/5038/5039), `controller-runtime` v0.24.1, `k8s.io/*` v0.36.2, `ginkgo/v2` v2.31.0, `gomega` v1.42.0, `golang.org/x/net` v0.55.0 (fixes GO-2026-5026), `actions/checkout` v7.

## [v1.0.0] - 2026-06-11

### Breaking Changes

- **API promoted from `v1alpha1` to `v1`** — all CRDs now use `apiVersion: ha.homeassistant.io/v1`. Existing resources must be migrated:
  ```bash
  kubectl get <resource> -o yaml | sed 's|ha.homeassistant.io/v1alpha1|ha.homeassistant.io/v1|' | kubectl apply -f -
  ```
  Affected kinds: `HomeAssistant`, `HomeAssistantSecrets`, `HomeAssistantConfiguration`, `HomeAssistantAutomation`, `HomeAssistantScene`, `HomeAssistantScript`, `HomeAssistantIntegration`, `HomeAssistantFloor`, `HomeAssistantLabel`, `HomeAssistantArea`.

- **`api/v1alpha1` package removed** — Go import path changed from `github.com/przemekhys/homeassistant-operator/api/v1alpha1` to `github.com/przemekhys/homeassistant-operator/api/v1`.

### Changed

- **API naming consistency** — Go field names now follow acronym conventions (`CreateAPIToken`, `APITokenSecretName`, `HTTPConfig`, `MQTTConfig`). JSON/YAML wire format is unchanged (`createApiToken`, `http`, `mqtt`).

- **`ObservedGeneration` added to all status structs** — all CRDs now surface `status.observedGeneration` with the last reconciled generation number.

- **`LastError` added to all status structs** — all CRDs now surface `status.lastError` with a human-readable error description, eliminating the need for `kubectl describe`.

- **Condition type standardized to `Ready`** — `HomeAssistantIntegration` previously used condition type `IntegrationReady`; now uses `Ready` consistent with all other CRDs. Existing conditions are migrated automatically on first reconcile.

## [v0.10.1] - 2026-04-26

### Fixed

- **Helm chart OCI path collision** — `helm push` was publishing the chart to `oci://ghcr.io/przemekhys/homeassistant-operator`, overwriting the Docker image tag. Chart is now published to `oci://ghcr.io/przemekhys/charts/homeassistant-operator`. Install command updated accordingly.

- **`runAsNonRoot` admission failure** — container `securityContext` now explicitly sets `runAsUser: 65532` and `runAsGroup: 65532`, eliminating reliance on image manifest UID resolution which failed on some k3s versions.

- **Go stdlib CVEs** — upgraded Go 1.26.0 → 1.26.2, fixing 10 vulnerabilities in `crypto/x509` (GO-2026-4947, GO-2026-4946, GO-2026-4866, GO-2026-4600, GO-2026-4599), `crypto/tls` (GO-2026-4870), `html/template` (GO-2026-4865, GO-2026-4603), `os` (GO-2026-4602), and `net/url` (GO-2026-4601).

### Added

- **Helm chart: `priorityClassName`** — new `values.yaml` field (default `""`) sets `priorityClassName` on the operator Deployment. Previously required a `postRenderers` JSON patch workaround in Flux HelmRelease.

- **Helm chart: `topologySpreadConstraints`** — new `values.yaml` field (default `[]`) for spreading operator pods across failure domains. Includes commented example for zone-based spreading.

- **Helm chart: `nodeSelector` and `affinity` examples** — `values.yaml` now includes commented examples for ARM64 node pinning (`kubernetes.io/arch: arm64`) and pod anti-affinity across nodes.

### Dependencies

- `k8s.io/apimachinery` 0.35.4 → 0.36.0
- `k8s.io/streaming` 0.36.0 (new transitive dependency of apimachinery 0.36.0)
- `actions/checkout` v4 → v6
- `actions/setup-go` v5 → v6
- `actions/setup-python` v5 → v6

## [v0.10.0] - 2026-04-22 [YANKED]

> **This release is yanked.** The Helm chart OCI artifact overwrote the Docker image tag — installing via `helm install` pulled a Helm chart manifest instead of a runnable container image, causing pods to fail with `/manager: no such file or directory`. Use v0.10.1 instead.



### Security

- **CVE-2026-33186** — upgraded `google.golang.org/grpc` v1.78.0 → v1.80.0 (fixes gRPC-Go authorization bypass via malformed HTTP/2 `:path` pseudo-header; operator does not expose a gRPC server so not directly exploitable, but upgraded as a precaution).
- **CVE-2026-39883** — upgraded `go.opentelemetry.io/otel` v1.40.0 → v1.43.0 (fixes OpenTelemetry-Go path traversal via untrusted `kenv` search path on BSD/Solaris; not exploitable on Linux, upgraded as a precaution).

### Added

- **`jsonValue` field for `HomeAssistantIntegration`** — new field in `IntegrationValue` that accepts a JSON-encoded string and submits it as a native JSON object to the Config Flow API. Fixes integrations like `openweathermap` that require dict-type fields (e.g. `location: {"latitude": 54.17, "longitude": 18.55}`). Example: `location: { jsonValue: '{"latitude": 54.17, "longitude": 18.55}' }`.

- **Documentation site** — MkDocs Material documentation deployed to GitHub Pages (`https://przemekhys.github.io/homeassistant-operator/`). Includes getting started guides, CRD API reference auto-generated from Go type comments (`make docs-api`), changelog auto-included from `CHANGELOG.md`, Contributing and Testing developer guides. New Makefile targets: `make docs-serve`, `make docs-build`, `make docs-api`. `README.md` simplified to a short landing page pointing to the full docs.

- **Helm chart** — `charts/homeassistant-operator/` provides a Helm chart for installing the operator. CRDs are bundled in `crds/` (installed automatically by Helm). Configurable via `values.yaml`: image, replicas, resources, nodeSelector, tolerations, affinity, serviceAccount. Published to OCI registry on each release: `helm install homeassistant-operator oci://ghcr.io/przemekhys/homeassistant-operator --version <version>`. New Makefile targets: `make helm-lint`, `make helm-package`, `make helm-push`.

- **Auto-unban operator IP from HA `ip_bans.yaml`** — when HA returns 403/429 (operator IP banned after too many failed login attempts), the operator automatically execs into the HA pod, removes its IP from `/config/ip_bans.yaml`, and deletes the pod so StatefulSet recreates it (clears in-memory bans). Limits: at most 5 unbans total, 5-minute cooldown between each. Once the limit is reached a `SelfUnbanLimitReached` warning event is emitted and manual intervention is required. New status fields: `selfUnbanCount`, `lastSelfUnban`. New event reasons: `SelfUnbanned`, `SelfUnbanFailed`, `SelfUnbanLimitReached`. Requires `POD_IP` env var (injected via downward API) and new RBAC `pods/exec create`.

## [v0.9.0] - 2026-04-17

### Added

- **`spec.recorder.databaseSecretRef` for HomeAssistantConfiguration** — database URL is read from a K8s Secret and mounted into the HA pod as a file; `configuration.yaml` references it via `db_url: !include recorder_db_url.yaml` so credentials are never written to a ConfigMap. Takes precedence over `spec.recorder.database` when both are set. `spec.recorder.enabled: false` skips injection and cleans up the mounted Secret. `purgeKeepDays` is also injected when set. The injection uses `yaml.Node` so `!include`/`!secret` tags in other sections are preserved.

- **API readiness gate in bootstrap** — `CheckAPIReady` (GET `/api/config` without auth) is now called between health check and onboarding status check. Returns 401 when HA routes are fully loaded, 404 during startup (same as `/api/onboarding`). This eliminates the ambiguity that required the 10-minute confirmation window.

### Changed

- **`onboardingConfirmDelay` reduced from 10 minutes to 30 seconds** — now that `CheckAPIReady` gates the bootstrap flow, a 404 from `/api/onboarding` is only seen after the API is fully loaded, making it a reliable signal. The 30-second window remains as a safety net for edge cases.

- **`logger` added to hot-reloadable config sections** — `logger` was documented as hot-reloadable but missing from `reloadableSections` map, causing the controller to trigger a full pod restart instead of calling `ReloadCoreConfig` when it was added or modified.

### Fixed

- **Bootstrap infinite loop on slow CI runners** — two interacting bugs caused `LoginRecoveryAttempts` to never advance past 1, trapping bootstrap in an infinite `LoginNoUser` cycle. (1) The "first seen OnboardingDone" handler reset `LoginRecoveryAttempts = 0` on every cycle, undoing any progress. (2) `LoginNoUser` (`type=form`) is a transient startup race where onboarding routes have not yet registered — it is not a credential failure and should not count toward the retry limit. Fixed: the counter is no longer reset in the first-seen handler, and `LoginNoUser` does not increment it. The loop now continues until onboarding routes load and `CheckOnboardingStatus` returns nil.

- **E2E critical path: wrong Service name** — test looked up `<ha-name>-homeassistant` but the controller creates the service as `<ha-name>`. Fixed to use the correct name.

- **E2E critical path: wrong PVC name** — test looked up `data-<ha-name>-0` (StatefulSet volumeClaimTemplate convention) but the controller creates a standalone PVC named `<ha-name>-data`. Fixed to use the correct name.

- **E2E critical path: script ID with hyphen rejected by HA** — `HomeAssistantScript` CR named `cp-script` used the CR name as the HA script ID when `spec.id` was not set. HA rejects script IDs containing hyphens (valid format: `[a-z0-9_]+`). Added explicit `id: critical_path_script`.

- **E2E critical path: debug info not collected on failure** — `AfterAll` checked `CurrentSpecReport().Failed()` which returns the state of the last `It` block (Backup — passed), so debug info was never collected even when earlier tests failed. Added `suiteFailed` flag updated by `AfterEach`.

## [v0.8.0] - 2026-04-07

### Added

- **HomeAssistantFloor CRD** (`hafloor`, `hafl`) — declarative management of Home Assistant floors via WebSocket registry API (`config/floor_registry/*`). Supports `name`, `level`, and `icon`. Adopts existing floors by name, finalizer-based cleanup on deletion.

- **HomeAssistantLabel CRD** (`halabel`, `halb`) — declarative management of Home Assistant labels via WebSocket registry API (`config/label_registry/*`). Supports `name`, `icon`, and `color`. Adopts existing labels by name, finalizer-based cleanup on deletion.

- **HomeAssistantArea CRD** (`haarea`, `haar`) — declarative management of Home Assistant areas via WebSocket registry API (`config/area_registry/*`). Supports `name`, `icon`, `floorName` (resolved to `floor_id` at reconcile time), and `labels[]` (resolved to `label_ids`). Requeues with `FloorNotFound` if referenced floor doesn't exist; missing labels produce a warning but don't block creation.

- **`SendWebSocketCommand` helper in haclient** — reusable one-shot WebSocket command pattern (connect → auth → command → response → close) used by Floor/Label/Area controllers. Refactored `CreateLongLivedToken` to use the same helper.

- **`spec.backup` for HomeAssistant CR** — declarative configuration of Home Assistant's built-in backup system via WebSocket API (`backup/config/info`, `backup/config/update`). Supports schedule (daily, per-day-of-week, never), time, retention (copies/days), and database inclusion. Requires bootstrap with API token enabled.
  - Idempotent: compares current HA config with desired state, only updates when drift detected
  - Condition: `BackupConfigured` with reasons: `BackupConfigured`, `BackupConfigFailed`, `TokenNotAvailable`
  - Events: `BackupConfigured` (Normal), `BackupConfigFailed` (Warning)

### Fixed

- **Bootstrap fails when onboarding already completed** — `CheckOnboardingStatus` did not handle HTTP 404 (which HA returns when onboarding is fully done, as the endpoint is unregistered). Also, when onboarding was already done, the operator tried to delete the pod (which doesn't reset PVC data) instead of logging in with credentials. Fixed: 404 is now correctly detected as "onboarding done", partial onboarding (user step done) is detected from the step array, and `handleOnboardingAlreadyDone` now logs in via HA's auth flow and creates the API token instead of deleting the pod.

- **Bootstrap pod delete forbidden** — RBAC ClusterRole was missing `delete` verb on `pods` resource, causing bootstrap to fail with "pods is forbidden" when the operator tried to restart the HA pod after onboarding.

- **`!secret` YAML tags stripped during location injection** — `injectLocation` used `map[string]interface{}` for YAML round-trip, which discards custom tags like `!secret` and `!include`. Switched to `yaml.Node` tree which preserves all YAML tags through the unmarshal/marshal cycle. Only affected configs with `spec.bootstrap.location` set.

- **Bootstrap missing `integration` onboarding step** — `PerformBootstrap()` only completed 3 of 4 required HA onboarding steps (`user`, `core_config`, `analytics`), leaving out `integration`. This caused non-admin users to be blocked from accessing HA's websocket API and redirected to `/onboarding.html`.

- **Config Flow `description` as object** — `FlowField.Description` was typed as `*string`, but some integrations (e.g. OpenWeatherMap) return it as an object (`{"suggested_value": "..."}`), causing JSON unmarshal errors. Changed to `json.RawMessage` to accept both formats.

- **Enable automation 404 on HA 2025.x+** — the operator called `POST /api/config/automation/config/{id}/enable` after every PUT, but this endpoint no longer exists in HA 2025.x/2026.x. Automations created via API are enabled by default. Removed the `EnableAutomation` call; only `DisableAutomation` is used when `spec.enabled: false`.

- **Bootstrap startup race: false-positive onboarding 404** — on slow clusters (CI, resource-constrained environments), HA's HTTP server becomes healthy before the onboarding component registers its routes, causing `/api/onboarding` to return 404 transiently. The controller now uses a time-based 10-minute confirmation window (`OnboardingDoneFirstSeen`) and re-polls every 30 seconds instead of trusting a single 404. If onboarding routes register during the window, normal bootstrap proceeds without disruption.

- **Bootstrap stuck in `LoginRecoveryFailed` when onboarding was never completed** — when the 10-minute confirmation window elapsed but `/api/onboarding` was still transiently unavailable, login recovery ran and failed with `type=form` (HA's auth flow returns this when no user exists). The controller now detects `type=form` as a signal that onboarding was never completed, resets `OnboardingDoneFirstSeen`, and restarts the confirmation window. `LoginRecoveryAttempts` is preserved across resets to limit total retries to `maxLoginRecoveryRetries` (3), preventing infinite loops on genuine credential errors.

### Changed

- Bump `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go` from v0.35.2 to v0.35.3

## [v0.7.1] - 2026-03-19

### Fixed

- **`auto_include` strips `!include` tags after YAML round-trip** — when `injectLocation` re-serialised `configuration.yaml`, the `!include` YAML tag was lost (e.g. `automation: !include automations.yaml` became `automation: automations.yaml`). HA treated the bare filename as a literal string and disabled all automations. `ensureAutoIncludes` now detects bare filenames and restores the `!include` directive in-place.

## [v0.7.0] - 2026-03-17

### Added

- **HomeAssistantIntegration CRD** (`haint`) — declarative management of Home Assistant integrations via Config Flow API. Does not deploy containers — only registers integrations in HA.
  - Supports single-step Config Flows (MQTT, ESPHome, and others)
  - `spec.configuration` fields support plain text values and `secretKeyRef` references to K8s Secrets
  - **Adopt pattern**: if integration already exists in HA (e.g. configured via UI), operator adopts the existing `entryID` without reconfiguring
  - **Day-2 reconfiguration**: changing `spec.configuration` triggers delete + re-create of the config entry
  - Finalizer-based cleanup: removes config entry from HA on CR deletion (best-effort)
  - Events: `IntegrationConfigured`, `IntegrationAdopted`, `IntegrationReconfigured`, `IntegrationRemoved`, `IntegrationFailed`
  - Condition: `IntegrationReady` with reasons: `IntegrationConfigured`, `AlreadyConfigured`, `TokenNotAvailable`, `HANotReady`, `ConfigFlowFailed`, `SecretResolutionFailed`
- **`spec.hostNetwork` for HomeAssistant CR** — enables host networking for IoT device discovery (mDNS/SSDP/DHCP). When enabled, sets `hostNetwork: true` and `dnsPolicy: ClusterFirstWithHostNet` on the pod.
- **Config Entry Flow API methods in haclient** — `ListConfigEntries`, `IsIntegrationConfigured`, `StartConfigFlow`, `SubmitConfigFlow`, `SubmitConfigFlowUntilDone`, `RemoveConfigEntry`.

### Fixed

- **Auto-inject `!include` directives** — the operator now automatically appends `automation: !include automations.yaml`, `scene: !include scenes.yaml`, and `script: !include scripts.yaml` to `configuration.yaml` if not already present. HA 2025.x requires explicit includes for PVC-managed files.

- **Recovery mode on first start** (`spec.storage.initContainer`) — HA was entering recovery mode with `Unable to read file /config/automations.yaml` because `auto_include.go` injects `!include automations.yaml` unconditionally, but the files are created by the automation/scene/script controller only after the pod is already running. An init container (`busybox` by default) now pre-creates empty `[]` files (`automations.yaml`, `scenes.yaml`, `scripts.yaml`) on the PVC before the main container starts. Image, tag, and repository are configurable via `spec.storage.initContainer`.


- **Location not set after bootstrap** — `SetCoreConfig` during the onboarding flow was silently ignored (HA returns an error when the endpoint is not yet ready), leaving `zone.home` at `latitude: 0, longitude: 0`. The configuration controller now injects `latitude`, `longitude`, `elevation`, `unit_system`, and `time_zone` into the `homeassistant:` section of `configuration.yaml` (only for keys not already defined by the user). The correct location is applied on the first configuration reconcile via hot-reload or restart.

### Removed

- **BREAKING CHANGE: HomeAssistantAddon CRD removed** — `HomeAssistantAddon` (`haad`) has been completely removed. Use Helm charts or standard Kubernetes resources (Deployment, Service, PVC) to deploy companion services like Mosquitto, MariaDB, or Node-RED. Use the new `HomeAssistantIntegration` CRD (`haint`) to register integrations declaratively.

## [v0.6.0] - 2026-03-09

### Added

- **HomeAssistantAutomation / Scene / Script: individual management via HA REST API** — each CR is now managed individually via `POST /api/config/{type}/config/{id}` (create/update) and `DELETE /api/config/{type}/config/{id}` (removal). Home Assistant writes directly to `automations.yaml` / `scenes.yaml` / `scripts.yaml` on the PVC. The old ConfigMap aggregation approach (`<ha-name>-automations`, `<ha-name>-scenes`, `<ha-name>-scripts`) has been removed.
  - Status condition `ReloadReady` reflects the last API call result
  - When the bootstrap token is not yet available, the controller requeues with backoff (30s) and sets `ReasonTokenNotAvailable`
  - Deletion via finalizer calls DELETE to HA API (best-effort — continues even when HA is unavailable)

### Migration (v0.5.x → v0.6.0)

> **Note for existing deployments**: when upgrading from v0.5.x to v0.6.0, the operator will automatically remove the old aggregation ConfigMaps (`<ha-name>-automations`, `<ha-name>-scenes`, `<ha-name>-scripts`) and their volume mounts from the Home Assistant StatefulSet. **The HA pod will be restarted once** during this migration. After restart, existing automations/scenes/scripts CRs will be re-synced to HA via the REST API.

## [v0.5.1] - 2026-03-07

### Fixed

- **HomeAssistantConfiguration: restart not triggered after adding new integration (e.g. `prometheus:`)** — when a reconcile attempt updated the ConfigMap content but failed before saving status, a subsequent retry would read `oldConfig == newConfig` and incorrectly choose hot-reload instead of restart. The controller now defaults to restart when the ConfigMap is already synced but status hash is stale.

- **HomeAssistantAddon mosquitto profile: conflict with Flux GitOps and HA 2025.x incompatibility** — the mosquitto profile was writing `mqtt: broker: ...` to `HomeAssistantConfiguration` CR on every reconcile. HA 2025.x dropped support for the `broker` key in `configuration.yaml` (returns `'broker' is an invalid option`), and Flux GitOps would immediately revert the change, causing an infinite reconcile loop. The `HAIntegration` has been removed from the mosquitto profile. Configure the MQTT broker via the Home Assistant UI: **Settings → Integrations → MQTT**. Automatic setup via Config Flow API is planned in Phase 6.


## [v0.5.0] - 2026-03-01

### Added

- **HomeAssistantAddon CRD**: Declarative addon management for Home Assistant
  - Profile system with built-in profiles: `mosquitto`, `mariadb`, `node-red` with sensible defaults
  - User overrides: user-provided fields take priority over profile defaults
  - Automatic Home Assistant integration (`spec.haIntegration`) — adds integration section to HomeAssistantConfiguration CR
  - Auto-provisioning of K8s resources: Deployment/StatefulSet, Service, PVC, ConfigMap, Ingress
  - Configuration via ConfigMap (`spec.config`) — mount configuration files into the addon container
  - Finalizer-based cleanup — removes integration section from HomeAssistantConfiguration on CR deletion
  - Status tracking: phase (Pending/Running/Failed), resolvedImage, workloadType, serviceName
  - Short names: `haaddon`, `haad`


## [v0.4.0] - 2026-02-21

### Added

- **Prometheus metrics for hot-reload operations** — three new domain-specific metrics exposed at `/metrics`:
  - `homeassistant_reload_total{component, result}` — counter per component (automation/scene/script) and result (success/failed/skipped)
  - `homeassistant_reload_duration_seconds{component}` — histogram (buckets: 0.5s–30s) for reload latency percentiles
  - `homeassistant_reload_retries_total{component}` — extra retry attempts beyond the first; non-zero value indicates the reload required more than one attempt
  - All data sourced from existing `ReloadResult` fields — no additional API calls required

- **HomeAssistantAutomation CRD**: Declarative automation management with hot-reload capabilities
  - Full automation definition via CRD with triggers, conditions, and actions
  - Uses `runtime.RawExtension` for flexible YAML compatibility with Home Assistant syntax
  - Aggregates multiple automation CRs into single ConfigMap (`<name>-automations`)
  - Finalizer-based deletion: regenerates ConfigMap without removed automation before CR deletion
  - Enable/disable without deletion via `spec.enabled` field
  - Short names: `haautomation`, `haauto`

- **HomeAssistantScene CRD**: Declarative scene management for Home Assistant
  - Aggregation pattern - multiple CR instances → single `scenes.yaml`
  - Entity validation with pattern regex (`domain.object_id`)
  - Flexible entity attributes support via `runtime.RawExtension`
  - Short names: `hascene`, `hasc`
  - Status tracking: Ready, LastActivated, LastReloadTime
  - Finalizer-based cleanup - regenerates ConfigMap without deleted scene
  - Auto-reload control via `spec.autoReload` (default: true)

- **HomeAssistantScript CRD**: Declarative script management for Home Assistant
  - Aggregation pattern - multiple CR instances → single `scripts.yaml`
  - Flexible sequence definition via `runtime.RawExtension`
  - Input parameters support via `spec.fields` map
  - Short names: `hascript`, `hascp`
  - Status tracking: Ready, LastReloadTime, LastReloadMethod
  - Finalizer-based cleanup - regenerates ConfigMap without deleted script
  - Auto-reload control via `spec.autoReload` (default: true)


## [v0.3.0] - 2026-01-27

### Added

- **HomeAssistantConfiguration CRD**: Declarative configuration management with intelligent hot-reload capabilities
  - Full `configuration.yaml` management via `spec.configuration` field
  - Smart reload strategy: automatically determines if changes require restart or can be hot-reloaded
  - Zero-downtime updates for reloadable sections (automations, scripts, logger, input helpers, etc.)
  - Three reload strategies: `auto` (default, analyzes changes), `hot-reload` (force REST API), `restart` (force pod restart)
  - Requires bootstrap-generated API token for hot-reload functionality
  - Short names: `haconfig`, `hacfg`

### Changed

- **BREAKING CHANGE**: HomeAssistantConfiguration CRD now REQUIRED for every HomeAssistant instance
  - Every `HomeAssistant` CR must have a corresponding `HomeAssistantConfiguration` CR
  - Controller validates HomeAssistantConfiguration exists before creating StatefulSet
  - ConfigMap auto-generated from HomeAssistantConfiguration spec (pattern: `<name>-configuration`)
- **BREAKING CHANGE**: PVC naming convention changed from `<name>-config` to `<name>-data`
  - The PersistentVolumeClaim for Home Assistant data storage now uses the suffix `-data` instead of `-config`


## [v0.2.0] - 2026-01-11

### Added

- **Zero-Touch Bootstrap**: Automatic Home Assistant onboarding without manual UI interaction
  - Creates admin user with credentials from Kubernetes Secret
  - Configures location, timezone, units, and currency (`spec.bootstrap.location`)
  - Sets analytics preferences (`spec.bootstrap.analytics`)
  - Generates long-lived API token via WebSocket API
  - Stores API token in Kubernetes Secret for programmatic access
- **HomeAssistantSecrets CRD**: Declarative secrets management for Home Assistant
  - References existing Kubernetes Secrets
  - Auto-generates `secrets.yaml` file
  - Automatic pod restart on secret changes (configurable via `spec.autoRestart`)
- **New haclient package**: Native Go HTTP/WebSocket client for Home Assistant API

### Changed

- Service naming simplified: now uses `<name>` instead of `<name>-homeassistant`


## [v0.1.0] - 2026-01-06

### Added

- Initial release of Home Assistant Operator
- `HomeAssistant` Custom Resource Definition (CRD) with support for:
  - Version/image configuration (`spec.version`, `spec.image`)
  - Storage configuration with PVC (`spec.storage`)
  - Service configuration (ClusterIP, NodePort, LoadBalancer) (`spec.service`)
  - Ingress configuration with TLS support (`spec.ingress`)
  - Resource limits and requests (`spec.resources`)
  - Timezone configuration (`spec.timezone`)
  - External ConfigMap for `configuration.yaml` (`spec.configurationFrom`)
  - External Secret for `secrets.yaml` (`spec.secretsFrom`)
- Reconciliation controller that manages:
  - StatefulSet for Home Assistant deployment
  - PersistentVolumeClaim for data storage
  - Service for network access
  - Ingress for external access (optional)
- Health checks (liveness and readiness probes)
- Status reporting with phase, conditions, and ready state
- Multi-architecture Docker images (amd64, arm64)
- CI/CD with GitHub Actions (lint, test, e2e tests)
- k3d support for local testing

### Target Environments

- Primary: k3s on Raspberry Pi 4/5 (ARM64)
- Also supported: Any Kubernetes cluster (AMD64/ARM64)

[Unreleased]: https://github.com/przemekhys/homeassistant-operator/compare/v1.2.0...HEAD
[v1.2.0]: https://github.com/przemekhys/homeassistant-operator/compare/v1.1.0...v1.2.0
[v1.1.0]: https://github.com/przemekhys/homeassistant-operator/compare/v1.0.1...v1.1.0
[v1.0.1]: https://github.com/przemekhys/homeassistant-operator/compare/v1.0.0...v1.0.1
[v1.0.0]: https://github.com/przemekhys/homeassistant-operator/compare/v0.10.1...v1.0.0
[v0.10.1]: https://github.com/przemekhys/homeassistant-operator/compare/v0.10.0...v0.10.1
[v0.10.0]: https://github.com/przemekhys/homeassistant-operator/compare/v0.9.0...v0.10.0
[v0.9.0]: https://github.com/przemekhys/homeassistant-operator/compare/v0.8.0...v0.9.0
[v0.8.0]: https://github.com/przemekhys/homeassistant-operator/compare/v0.7.1...v0.8.0
[v0.7.1]: https://github.com/przemekhys/homeassistant-operator/compare/v0.7.0...v0.7.1
[v0.7.0]: https://github.com/przemekhys/homeassistant-operator/compare/v0.6.0...v0.7.0
[v0.6.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.6.0
[v0.5.1]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.5.1
[v0.5.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.5.0
[v0.4.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.4.0
[v0.3.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.3.0
[v0.2.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.2.0
[v0.1.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.1.0
