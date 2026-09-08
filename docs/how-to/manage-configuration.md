# Manage configuration.yaml declaratively

*How-to — describe Home Assistant's `configuration.yaml` as a Kubernetes resource. Assumes a running instance.*


## Prerequisites

- A running Home Assistant instance ([deploy one](deploy-instance.md))

## Example

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: home
spec:
  homeAssistantRef:
    name: home
  reloadStrategy: auto
  configuration: |
    homeassistant:
      name: My Home
      latitude: 52.237703
      longitude: 20.989075
      unit_system: metric
      currency: PLN
      country: PL
      time_zone: Europe/Warsaw

    default_config:

    logger:
      default: info
      logs:
        homeassistant.components.mqtt: warning
```

## Auto-include

The operator automatically appends the following lines to `configuration.yaml` if they are missing:

```yaml
automation: !include automations.yaml
scene: !include scenes.yaml
script: !include scripts.yaml
```

These lines are required for Home Assistant to load resources managed by `HomeAssistantAutomation`, `HomeAssistantScene`, and `HomeAssistantScript`. You do not need to add them manually.

## HTTP configuration on Home Assistant 2026.8+

Home Assistant 2026.8 moved the `http` integration out of `configuration.yaml`
into its own store, managed from the UI under **Settings → System → Network**.
The YAML `http:` block is ignored after Home Assistant's one-time import, and
stops working entirely in Home Assistant 2027.2.0.

You keep writing `http:` in `spec.configuration` exactly as before — nothing in
your resource changes. On a Home Assistant that supports the new API the operator:

- applies the `http:` section (plus its default `trusted_proxies` /
  `use_x_forwarded_for` for Ingress/Gateway-exposed instances) through that API;
- **omits `http:` from the generated `configuration.yaml`**, so Home Assistant
  does not raise a "remove the http: block" repair issue;
- reports which channel it used in `status.httpConfigSource` (`Api` or `Yaml`)
  and whether it took effect in the `HTTPConfigReady` condition.

On older Home Assistant the `http:` block still goes into `configuration.yaml`
as it always has. Switching Home Assistant versions in either direction is
picked up automatically, with no operator restart.

!!! warning "The resource is the source of truth"
    The operator enforces the `http` configuration from the resource. A change
    made in the Home Assistant UI (Settings → System → Network) to a setting the
    resource covers is **reverted on the next reconcile** — even though Home
    Assistant itself points you at that UI. Edit `spec.configuration`, not the
    Home Assistant UI, for these settings. The operator never confirms a pending
    change it did not send, so a change you start in the UI is left for you (or
    Home Assistant's 5-minute auto-revert) to resolve.

    If your `http:` section is an `!include` of a separate file, the operator
    cannot read it to deliver via the API — it stays in `configuration.yaml` and
    Home Assistant's warning remains until you inline the keys.

## Multiple strategy examples

=== "Auto (recommended)"

    ```yaml
    spec:
      reloadStrategy: auto
    ```

=== "Always hot-reload"

    ```yaml
    spec:
      reloadStrategy: hot-reload
    ```

=== "Always restart"

    ```yaml
    spec:
      reloadStrategy: restart
    ```

## Verify

```sh
kubectl get haconfig home
```
```
NAME   HOMEASSISTANT   STRATEGY   READY   AGE
home   home            auto       True    10m
```

```sh
kubectl describe haconfig home
```
```
Status:
  Config Hash:          sha256:abc123...
  Last Reload Time:     2026-04-23T10:00:00Z
  Last Reload Method:   hot-reload
  Last Error:           <none>
  Observed Generation:  3
```

## Force a reload strategy

Controls how changes are applied.

| Value | Behaviour |
|-------|-----------|
| `auto` (default) | Hot-reload for `automation`, `script`, `scene`, `group`, `logger`, `timer`, `counter` and `input_*`. Restart for `homeassistant`, `mqtt`, `template`, `zone`, and unknown sections. `http` triggers a pod restart **only on the YAML delivery path** (older Home Assistant); on the API path Home Assistant restarts its own process when needed and the operator does not roll the pod. |
| `hot-reload` | Always attempt hot-reload, regardless of which sections changed. |
| `restart` | Always trigger a rolling restart. |

## Turn automatic reloading off

Set `spec.autoReload` to `false` to stop the operator reloading or restarting
Home Assistant when the configuration changes. It defaults to `true`.

```yaml
spec:
  autoReload: false
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantConfiguration` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantconfigurationspec).
