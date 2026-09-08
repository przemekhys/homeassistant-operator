# The reconciliation model

*Explanation — why the operator waits instead of failing, and how each resource reaches Home Assistant. Nothing here needs a cluster.*

The operator is level-triggered: a resource's spec describes the state the world
should be in, never an action to perform. Reconciliation runs over and over, and
must reach the same result every time. That single property explains most of the
behaviour that surprises people at first — in particular, why a resource can sit
in a waiting state for minutes without ever reporting an error.

A missing prerequisite is not a failure. When a referenced `HomeAssistant` does
not exist yet, or the API token has not been created, the operator schedules
another pass instead of returning an error. Errors are reserved for genuine
faults, because an error triggers exponential backoff and pollutes the metrics —
the wrong response to a resource that simply has not been created yet. The order
in which you `kubectl apply` related resources therefore does not matter.

## How a resource reaches Home Assistant

Nothing is written to a file on the volume and hoped for. Each resource is
pushed into Home Assistant over its own API and, where the component supports it,
reloaded in place:

| Resource | Written with | Removed with | Reload |
|----------|--------------|--------------|--------|
| `HomeAssistantAutomation` | `POST /api/config/automation/config/{id}` | `DELETE` on the same path | `POST /api/services/automation/reload` |
| `HomeAssistantScene` | `POST /api/config/scene/config/{id}` | `DELETE` on the same path | `POST /api/services/scene/reload` |
| `HomeAssistantScript` | `POST /api/config/script/config/{id}` | `DELETE` on the same path | none needed — Home Assistant applies it immediately |
| `HomeAssistantIntegration` | Config Flow: start, then submit the answers | `DELETE` of the config entry | none — the entry is live once created |
| `HomeAssistantFloor` / `Label` / `Area` | WebSocket registry calls | WebSocket registry calls | none |

Home Assistant uses `POST` rather than `PUT` for those config endpoints, and it
is idempotent: re-sending the same definition is how the operator converges,
rather than something it avoids.

All of it needs the API token that
[bootstrap](../how-to/bootstrap-instance.md) creates. Until that Secret exists,
these resources report `TokenNotAvailable` and wait — which is the level-triggered
model doing exactly what it should, not a failure to report.

Deletion goes through a finalizer, and the finalizer completes **even when Home
Assistant is unreachable**. A broken instance would otherwise leave you unable to
delete resources at all, which is a worse failure than an orphaned automation in
a Home Assistant that is already down.

## Bootstrap state machine

```
pod not ready          → requeue 10 s
HA API not loaded      → requeue 5 s  (avoids ambiguous 404 during cold start)
onboarding pending     → perform onboarding → create token
onboarding complete    → ensure token Secret exists → done
```

## IP ban self-recovery

When Home Assistant has `ip_ban_enabled: true`, it can ban the operator's own IP after repeated failed logins (HTTP `403`) — for example during bootstrap retries. A banned operator can no longer reach the HA API, which would normally require manual editing of `/config/ip_bans.yaml`. The operator recovers from this automatically, **without** needing the `pods/exec` RBAC permission.

### How it works

1. The operator detects it is banned (HTTP `403` from HA).
2. It deletes the HA pod. The `StatefulSet` recreates it with an `unban-operator-ip` init-container (reusing the HA image already cached on the node).
3. The init-container removes the operator's IP from `/config/ip_bans.yaml` **before** HA starts, then HA comes up unbanned.

The operator's IP is passed to the pod via the `<ha-name>-operator-ip` ConfigMap, sourced from the `POD_IP` downward-API environment variable on the operator Deployment (set by default in the Helm chart).

### Sliding-window protection

To avoid a restart loop, recovery is rate-limited:

- At most **3 pod restarts** within a **30-minute** window.
- A minimum **5-minute cooldown** between consecutive restarts.
- The window resets automatically after 30 minutes **or** on the first successful HA connection.

Once the limit is exceeded the operator stops restarting and reports
`BanRecoveryFailed=True`, because a loop that keeps deleting the pod would be
worse than a stopped one. Recovering from that point is a manual task — see
[troubleshoot a problem](../how-to/troubleshoot.md#operator-gets-banned-by-home-assistant-banrecoveryfailed).

### Status fields

| Field | Meaning |
|-------|---------|
| `status.selfUnbanCount` | Total number of self-unban restarts performed. |
| `status.lastSelfUnban` | Timestamp of the most recent self-unban. |

## Waiting for cert-manager

If you enable a cert-manager-backed TLS mode while cert-manager is absent:

- The resource reports `CertManagerAvailable=False` (reason `CertManagerNotInstalled`)
  and emits a `CertManagerUnavailable` event.
- Home Assistant keeps serving over HTTP; exposure keeps working over HTTP.
- No error is raised and reconciliation does not loop.
- Once you install cert-manager, the operator provisions the certificate automatically.

See also: [Troubleshooting](../how-to/troubleshoot.md) and the
[`config/samples/`](https://github.com/przemekhys/homeassistant-operator/tree/main/config/samples)
directory (`ha_v1_ingress_tls.yaml`, `ha_v1_gateway_managed_tls.yaml`).

## What the model costs

Level-triggered reconciliation is not free. Because the operator never trusts
that it saw an event, it re-derives the desired state on every pass, which means
more API calls than an event-driven design would make. It also means a resource
can sit in a waiting state for a long time without anyone being told loudly that
something is wrong — the status says so, but nothing pages you.

The alternative is a controller that reacts to changes as they arrive. That one
is cheaper and faster right up until it misses an event, after which it is
confidently wrong and stays that way until someone restarts it. Given a choice
between wasted API calls and silent divergence, this operator picks the calls.

## See also

- [Status conditions](../reference/conditions.md) — what each waiting state is
  called and what it is waiting for.
- [Hot reload versus restart](reload-vs-restart.md) — how a change that *has*
  been detected is applied.
