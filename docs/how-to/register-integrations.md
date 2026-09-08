# Register integrations

*How-to — register a Home Assistant integration (MQTT, ESPHome, …) without opening its UI. Assumes a running instance.*


## Prerequisites

- A running Home Assistant instance with [bootstrap completed](bootstrap-instance.md) — the operator needs its API token
- The service you are integrating with, reachable from the cluster

## Use cases

- Register MQTT broker after deploying Mosquitto via Helm
- Configure ESPHome, recorder, or other single-step integrations
- Adopt an integration that was configured manually in the HA UI

## MQTT example

```yaml
# 1. Deploy broker (e.g. via Helm)
# helm install mosquitto eclipse-mosquitto/mosquitto

# 2. Register integration
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantIntegration
metadata:
  name: mqtt
spec:
  homeAssistantRef:
    name: home
  domain: mqtt
  configuration:
    broker:
      value: "mosquitto.default.svc.cluster.local"
    port:
      value: "1883"
    username:
      secretKeyRef:
        name: mqtt-credentials
        key: username
    password:
      secretKeyRef:
        name: mqtt-credentials
        key: password
```

## Recorder example

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantIntegration
metadata:
  name: recorder
spec:
  homeAssistantRef:
    name: home
  domain: recorder
  configuration:
    db_url:
      secretKeyRef:
        name: database-credentials
        key: db_url
```

## Reconfiguring

To change the integration configuration, update `spec.configuration`. The operator detects the hash change, removes the existing config entry, and runs a new Config Flow.

```sh
kubectl patch haint mqtt --type=merge \
  -p '{"spec":{"configuration":{"port":{"value":"1884"}}}}'
```

## Limitations

- Only **single-step** Config Flows are supported (MQTT, ESPHome, recorder, etc.)
- Multi-step flows (ZHA, Zigbee2MQTT) require physical device interaction — not automatable
- The operator passes `spec.configuration` values as-is; field names must match what HA expects for that domain

## Deleting an integration

```sh
kubectl delete haint mqtt
```

The finalizer calls `DELETE /api/config/config_entries/entry/{entryID}` before removing the CR. If HA is unavailable, the finalizer completes anyway (best-effort).

## Verify

```sh
kubectl get haint mqtt
```
```
NAME   HOMEASSISTANT   DOMAIN   READY   AGE
mqtt   home            mqtt     True    5m
```

```sh
kubectl describe haint mqtt
```
```
Status:
  Entry ID:     abc123def456
  Config Hash:  sha256:...
  Conditions:
    IntegrationReady: True (reason: IntegrationConfigured)
```

## Supply values, in plain text or from a Secret

Key-value map of configuration values passed to the Config Flow. Each value is either:

- **Plain text**: `value: "some-string"`
- **Secret reference**: resolved from a Kubernetes Secret at reconcile time

```yaml
configuration:
  broker:
    value: "mosquitto.default.svc.cluster.local"
  password:
    secretKeyRef:
      name: mqtt-credentials
      key: password
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantIntegration` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantintegrationspec).
