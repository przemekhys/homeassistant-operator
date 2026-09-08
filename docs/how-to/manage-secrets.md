# Manage secrets

*How-to — compose Kubernetes Secrets into Home Assistant's `secrets.yaml`. Assumes a running instance.*


## Prerequisites

- A running Home Assistant instance ([deploy one](deploy-instance.md))
- The Kubernetes Secrets you want to expose to Home Assistant

## Example

```yaml
# Source secrets (created separately)
# kubectl create secret generic mqtt-credentials \
#   --from-literal=mqtt_user=homeassistant \
#   --from-literal=mqtt_password=supersecret

apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantSecrets
metadata:
  name: home-secrets
spec:
  homeAssistantRef:
    name: home
  autoRestart: true
  secretRefs:
    - name: mqtt-credentials
      keys:
        - mqtt_user
        - mqtt_password
    - name: database-credentials
      keys:
        - db_url
    # Include all keys from a secret (no keys list):
    - name: ha-extra-secrets
```

Then reference in `configuration.yaml`:

```yaml
mqtt:
  broker: !secret mqtt_broker
  username: !secret mqtt_user
  password: !secret mqtt_password
```

## Updating secrets

When a source Kubernetes Secret changes, the operator automatically detects it, regenerates `secrets.yaml`, and triggers a rolling restart (if `autoRestart: true`). No manual intervention is needed.

```sh
# Rotate MQTT password
kubectl create secret generic mqtt-credentials \
  --from-literal=mqtt_user=homeassistant \
  --from-literal=mqtt_password=newpassword \
  --dry-run=client -o yaml | kubectl apply -f -
# operator picks it up within seconds
```

## Verify

```sh
kubectl get hasec home-secrets
```
```
NAME            HOMEASSISTANT   READY   AGE
home-secrets    home            True    2m
```

```sh
kubectl describe hasec home-secrets
```
```
Status:
  Conditions:
    Type:    Ready
    Status:  True
  Secrets Hash: sha256:abc123...
  Last Updated: 2026-04-23T10:00:00Z
```

## Pick which Secrets and keys to merge

List of Kubernetes Secrets to merge.

| Field | Description |
|-------|-------------|
| `name` | Name of the Kubernetes Secret |
| `keys` | Specific keys to include. If omitted, **all keys** from the Secret are included |

## Control the restart on change

When `true` (default), any change to the referenced Secrets triggers a rolling restart of the HA pod via a hash annotation on the StatefulSet.

Set to `false` if you manage restarts externally (e.g. [Stakater Reloader](https://github.com/stakater/Reloader)).

```yaml
spec:
  autoRestart: false
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantSecrets` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantsecretsspec).
