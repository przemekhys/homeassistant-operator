# Expose an instance with TLS

*How-to — put an instance behind an Ingress or Gateway with a cert-manager certificate. Assumes a running instance.*

## Prerequisites

- A running Home Assistant instance.
- **Only if you want cert-manager to issue the certificate** (`spec.ingress.tls.issuerRef`
  or `spec.gateway`): cert-manager installed, plus a ready `Issuer` or
  `ClusterIssuer`. The operator only **references** an issuer; it never creates
  application issuers.

If you already hold a certificate, point `spec.ingress.tls.secretName` (or
`spec.gateway.secretName`) at the Secret holding it and skip cert-manager
entirely. `secretName` takes precedence over `issuerRef`, so setting both means
your Secret is used and the issuer is ignored. The operator's own admission
webhook does not need cert-manager either — it self-signs and rotates its
serving certificate by default.

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
    manageGateway: true
    gatewayClassName: traefik # existing GatewayClass provided by the platform
```

To attach the route to an existing Gateway instead, replace `manageGateway` and
`gatewayClassName` with a `parentRef` containing the Gateway name and, when
needed, its namespace and listener `sectionName`. `parentRef` takes precedence
if both forms are configured, and `gatewayClassName` never modifies an external
Gateway.

Omitting `gatewayClassName` defaults a managed Gateway to the `traefik` class.
You can change the field declaratively; removing it restores the `traefik`
default. Clusters using another Gateway controller should set the field
explicitly.

!!! note "What the operator does not manage"
    The operator does **not** manage the `GatewayClass` or the Ingress/Gateway
    controller itself — those are provided by your platform. `gatewayClassName`
    only selects an existing class; it does not create or discover one. The
    operator manages the routing resources and the certificate.


## Webhook

The operator ships a validating admission webhook that checks the coherence of your
configuration at apply time (for example, it rejects `spec.ingress.tls` enabled
without an `issuerRef` or `secretName`). It is **enabled by default (opt-out)** —
more validations will be added over time; disable it with `--set webhook.enabled=false`
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

## Verify

The operator reflects TLS state in `status.conditions`:

```bash
kubectl get homeassistant home -o jsonpath='{.status.conditions}' | jq
```

## Trusted proxies

Home Assistant rejects every request with `400 Bad Request` unless it is told
to trust the proxy in front of it. Whenever `spec.ingress.enabled` or
`spec.gateway.enabled` is `true`, the operator automatically supplies the
following, unless the keys are already present:

```yaml
http:
  use_x_forwarded_for: true
  trusted_proxies:
    - 10.0.0.0/8
    - 172.16.0.0/12
    - 192.168.0.0/16
```

On Home Assistant 2026.8+ these are delivered through the http config API rather
than written into `configuration.yaml` (see
[manage configuration](manage-configuration.md#http-configuration-on-home-assistant-20268)),
with no change to the outcome. On older Home Assistant they go into the generated
`configuration.yaml`.

These are the RFC1918 private address ranges — a conservative default, not an
autodetection of the real cluster pod/service CIDR (which cannot be reliably
read from the Kubernetes API). Each key is added independently: if you have
already set either `http.use_x_forwarded_for` or `http.trusted_proxies`
yourself in `HomeAssistantConfiguration`, the operator leaves your value
untouched and only fills in the missing key. If `http:` itself is an
externally managed tagged block (for example `http: !include http.yaml`),
the operator leaves it completely untouched — set the keys in that included
file, or move the section into `HomeAssistantConfiguration.spec.configuration`
directly, if you want the operator to manage them.

**Security note**: because these are broad RFC1918 ranges, in most Kubernetes
clusters they cover every pod on the network, not just your actual Ingress
controller or Gateway. Any reachable workload can then set its own
`X-Forwarded-For` header and have Home Assistant trust it as the real client
IP, weakening IP-based bans, rate limiting, and audit-log attribution. If
other workloads in the cluster aren't trusted, replace the default
`trusted_proxies` with the actual CIDR of your Ingress/Gateway proxy (for
example, the ingress controller's pod or Service CIDR) in
`HomeAssistantConfiguration`, or disable the defaults below and configure
`http.trusted_proxies`/`http.use_x_forwarded_for` yourself.

To opt out entirely (for example, if your cluster's pod/service network isn't
RFC1918, or you want to set narrower proxy ranges yourself), set:

```yaml
spec:
  disableDefaultTrustedProxies: true
```

The `HomeAssistant` resource's `ExposureReady` condition message reports
which of the three states applies: `default trusted proxies applied`, `using
user-configured trusted proxies`, or `default trusted proxies disabled
(opt-out)`.
