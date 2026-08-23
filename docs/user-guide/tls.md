# TLS with cert-manager

The operator integrates with [cert-manager](https://cert-manager.io/) to provision
TLS certificates for three independent use cases:

- **Native TLS** — Home Assistant serves HTTPS itself, on its existing port.
- **Ingress / API Gateway** — the operator manages the edge routing and its certificate.
- **Webhook** — the operator's validating admission webhook serves over TLS.

!!! info "cert-manager is an optional, external dependency"
    Neither the operator nor its Helm chart ever installs cert-manager. You install
    it (and provide an `Issuer`/`ClusterIssuer`) yourself. If cert-manager is **not**
    present, native TLS and Ingress/API Gateway reconciliation degrade gracefully:
    the corresponding mode simply stays inactive and the resource reports a status
    condition — nothing fails or loops. A cert-manager installed *after* the
    operator is picked up automatically.

    This graceful degradation does **not** cover the webhook's cert-manager
    override (`--set webhook.certManager.enabled=true`): that path renders an
    `Issuer`/`Certificate` directly via Helm, which requires the cert-manager CRDs
    to exist at install time. Only enable it when cert-manager is already installed.

## Prerequisites

- cert-manager installed on the cluster.
- A ready `Issuer` or `ClusterIssuer`. The operator only **references** an issuer;
  it never creates application issuers.

```yaml
# Example: a self-signed ClusterIssuer for testing
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ca-issuer
spec:
  selfSigned: {}
```

## The `issuerRef`

Every TLS mode references an issuer the same way. `kind` defaults to `Issuer`,
`group` to `cert-manager.io`.

```yaml
issuerRef:
  name: ca-issuer
  kind: ClusterIssuer   # or Issuer (namespaced)
```

!!! tip "Bring your own certificate"
    Each mode also accepts a `secretName` pointing at a TLS Secret you manage
    yourself. When set, it **takes precedence over `issuerRef`** and the operator
    does not create a cert-manager `Certificate`.

## Native TLS (alpha)

Home Assistant terminates TLS itself, serving HTTPS on its existing port (`8123`) —
no reverse proxy required. The operator provisions a certificate (or uses a
bring-your-own Secret via `secretName`, identically), mounts it into the pod at
`/config/ssl`, and switches its own connection to Home Assistant to HTTPS (trusting
the issued CA — certificate verification is never disabled).

!!! warning "This is an `spec.alpha` feature"
    Native TLS changes how Home Assistant serves traffic and how the operator
    connects to it, so it lives under `spec.alpha` and is **off by default**. Alpha
    fields may change or be removed without a deprecation notice.

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: home
spec:
  alpha:
    tls:
      native:
        enabled: true
        issuerRef:
          name: ca-issuer
          kind: ClusterIssuer
        dnsNames:
          - ha.example.com
        # secretName: my-tls   # bring-your-own; overrides issuerRef
```

The operator always adds the in-cluster Service FQDN
(`<name>.<namespace>.svc.cluster.local`) to the certificate's SANs so it can verify
Home Assistant over HTTPS.

The pod switches to HTTPS only **after** the certificate is issued, so enabling the
mode never leaves Home Assistant stuck without a certificate.

### How certificate rotation is applied

Recent Home Assistant core versions migrate the `http:` integration's configuration
out of `configuration.yaml` into an internal, UI-managed store — once migrated, HA
silently ignores the `http:` YAML block. To keep rotation working regardless, the
operator applies a new certificate through HA's WebSocket API
(`http/config`/`http/config/configure`/`http/config/promote`) whenever it's
available, falling back to the legacy YAML injection (with a pod restart, as
before) only when it isn't — an older HA core version, or an instance that hasn't
finished bootstrapping yet. Which path is active is re-checked on every reconcile,
so an HA upgrade is picked up automatically without operator intervention.

On the WebSocket path, Home Assistant restarts its own process to apply the new
certificate — the operator does **not** also restart the pod, avoiding a redundant,
racy double restart. Before confirming the new certificate, the operator verifies
HA is actually reachable over HTTPS with it (a health-check, never an optimistic
confirmation). If Home Assistant rejects the new certificate, it reverts to the
previous one on its own within a few minutes; the operator observes this and
reports it via a `TLSConfigReverted` condition reason and a warning Event, without
retrying the same rejected configuration in a loop — Home Assistant is never left
unavailable by a bad rotation.

| `TLSReady` reason | Meaning |
|---|---|
| `TLSReady` | Active certificate confirmed (either path). |
| `TLSConfigPending` | A new configuration was sent via WebSocket; waiting for Home Assistant to restart and confirm it. |
| `TLSConfigReverted` | The last rotation attempt was rejected and Home Assistant reverted to the previous certificate on its own. |
| `WSConfigUnsupported` | Home Assistant doesn't support the WebSocket config API yet; using the legacy YAML injection path instead. |

### Other `http:` settings are managed the same way — even without native TLS

HA's YAML-to-storage migration silently drops the **entire** `http:` section, not
just the TLS-related keys — this affects every `HomeAssistant`, whether or not
`spec.alpha.tls.native` is enabled. So the operator applies the rest of your
`http:` block through the same WebSocket mechanism, for every instance:
`server_host`, `cors_allowed_origins`, `login_attempts_threshold`,
`ip_ban_enabled`, `ssl_profile`, `use_x_frame_options`, and `ssl_peer_certificate`.
It reads these straight out of the `http:` block you already wrote in
`HomeAssistantConfiguration.spec.configuration` — there's no separate field to
set them again. If a field is absent from your `http:` block, the operator never
invents a value for it.

If your `http:` block can't be safely read this way (for example, it uses an
`!include` tag to pull in a separate file), the operator falls back to leaving
that reconcile's `http:` handling to the legacy YAML mechanism rather than
guessing at partial settings.

On an instance without native TLS, this happens silently in the background —
there's no dedicated status condition for it (that's what `TLSReady` above is
for, and it only means something when native TLS is actually requested). If you
want to confirm a change went through, check Home Assistant's own effective
configuration (Developer Tools, or `http/config` over the WebSocket API) rather
than a Kubernetes status field.

## Ingress / API Gateway exposure

The operator manages the edge routing resources **and** their certificate.

### Ingress

Enable `spec.ingress` and add an `issuerRef` under `tls`. The operator creates the
`Ingress` and a `Certificate` whose Secret backs the Ingress TLS.

```yaml
spec:
  ingress:
    enabled: true
    host: ha.example.com
    ingressClassName: traefik
    tls:
      enabled: true
      issuerRef:
        name: ca-issuer
        kind: ClusterIssuer
```

### Gateway API

`spec.gateway` (a stable opt-in) makes the operator manage a `HTTPRoute` that routes
to Home Assistant, and — when `manageGateway: true` — a `Gateway` with an HTTPS
listener. Attach the route to an existing `Gateway` via `parentRef`, or let the
operator create one.

```yaml
spec:
  gateway:
    enabled: true
    host: ha.example.com
    issuerRef:
      name: ca-issuer
      kind: ClusterIssuer
    parentRef:                 # attach to an existing Gateway listener
      name: traefik-gateway
      namespace: gateway
      sectionName: https
    # manageGateway: true      # ...or let the operator create the Gateway
```

!!! note "What the operator does not manage"
    The operator does **not** manage the `GatewayClass` or the Ingress/Gateway
    controller itself — those are provided by your platform. It only manages the
    routing resources and the certificate.

## Webhook

The operator ships a validating admission webhook that checks the coherence of your
configuration at apply time (for example, it rejects native TLS enabled without an
`issuerRef` or `secretName`). It is **enabled by default (opt-out)** — more
validations will be added over time; disable it with `--set webhook.enabled=false`
if it ever gets in your way.

### Self-managed serving certificate (default — no cert-manager)

By default the **operator manages its own serving certificate**: it generates a
self-signed certificate, rotates it automatically, and injects the CA bundle into
its own `ValidatingWebhookConfiguration`. This needs **no cert-manager** and works
the same on Helm, Kustomize and plain manifests.

| `webhook.enabled` | `webhook.certManager.enabled` | Serving certificate |
|-------------------|-------------------------------|---------------------|
| `true` (default)  | `false` (default)             | **Self-managed by the operator — no cert-manager** |
| `true`            | `true`                        | Issued by cert-manager, CA injected via annotation |
| `false`           | —                             | Webhook not deployed (`ENABLE_WEBHOOKS=false`) |

### cert-manager (opt-in override)

If you prefer cert-manager to issue and rotate the serving certificate (for example
to centralize certificate policy), opt in:

```bash
helm upgrade ha-operator ... \
  --set webhook.certManager.enabled=true   # requires cert-manager installed
```

!!! info "Installation never requires cert-manager"
    The webhook's default self-managed path needs no cert-manager. Enable the
    cert-manager override only when cert-manager is installed.

!!! tip "Availability of a default-on webhook"
    With `failurePolicy: Ignore` (the default), `HomeAssistant` create/update calls
    are admitted best-effort while the webhook is unavailable (e.g. during an
    operator restart) — validation simply doesn't run for that call. Set
    `--set webhook.failurePolicy=Fail` to reject calls instead while the webhook is
    down, or disable the webhook entirely.

## Status conditions

The operator reflects TLS state in `status.conditions`:

| Condition | Meaning |
|-----------|---------|
| `CertManagerAvailable` | Whether cert-manager was detected on the cluster |
| `TLSReady` | Whether the certificate for the enabled TLS mode has been issued |
| `ExposureReady` | Whether the Ingress/Gateway exposure resources are reconciled |

```bash
kubectl get homeassistant home -o jsonpath='{.status.conditions}' | jq
```

## Behavior without cert-manager

If you enable a cert-manager-backed TLS mode while cert-manager is absent:

- The resource reports `CertManagerAvailable=False` (reason `CertManagerNotInstalled`)
  and `TLSReady=Unknown`, and emits a `CertManagerUnavailable` event.
- Home Assistant keeps serving over HTTP; exposure keeps working over HTTP.
- No error is raised and reconciliation does not loop.
- Once you install cert-manager, the operator provisions the certificate automatically.

See also: [Troubleshooting](../reference/troubleshooting.md) and the
[`config/samples/`](https://github.com/przemekhys/homeassistant-operator/tree/main/config/samples)
directory (`ha_v1_native_tls.yaml`, `ha_v1_ingress_tls.yaml`, `ha_v1_gateway_managed_tls.yaml`).
