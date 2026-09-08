# Model your house structure

*How-to — describe floors, labels and areas as Kubernetes resources. Assumes a running instance.*

Home Assistant's floor, label and area registries have no REST endpoints, so the
operator drives them over Home Assistant's WebSocket API. That detail matters
only if something fails: the errors you would see come from the WebSocket
connection, not from HTTP.


## Prerequisites

- A running Home Assistant instance with [bootstrap completed](bootstrap-instance.md) — the operator needs its API token

## Dependency order

```
HomeAssistantFloor  ──┐
                      ├──► HomeAssistantArea
HomeAssistantLabel  ──┘
```

Area resolves floor and label references by name at reconcile time. The two behave differently when a reference is missing: if the referenced Floor is not found, the Area sets condition `FloorNotFound` and requeues after 30 seconds. If a referenced Label is not found, that label is silently skipped and the area is still created/updated without it — no requeue is triggered.

## Floors

A floor represents a physical level of your building.

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantFloor
metadata:
  name: ground-floor
spec:
  homeAssistantRef:
    name: home
  name: "Ground Floor"
  level: 0
  icon: "mdi:home-floor-0"
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantFloor
metadata:
  name: first-floor
spec:
  homeAssistantRef:
    name: home
  name: "First Floor"
  level: 1
  icon: "mdi:home-floor-1"
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantFloor
metadata:
  name: basement
spec:
  homeAssistantRef:
    name: home
  name: "Basement"
  level: -1
  icon: "mdi:home-floor-negative-1"
```

### Floor spec reference

| Field | Type | Description |
|-------|------|-------------|
| `homeAssistantRef.name` | string | Name of the `HomeAssistant` CR |
| `name` | string | Display name in the HA UI |
| `level` | int | Floor level (0 = ground, negative = below ground) |
| `icon` | string | Material Design icon |

## Labels

Labels are tags you can attach to areas for filtering and grouping.

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantLabel
metadata:
  name: outdoor
spec:
  homeAssistantRef:
    name: home
  name: "Outdoor"
  icon: "mdi:tree"
  color: "green"
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantLabel
metadata:
  name: critical
spec:
  homeAssistantRef:
    name: home
  name: "Critical"
  icon: "mdi:alert"
  color: "red"
```

### Label spec reference

| Field | Type | Description |
|-------|------|-------------|
| `homeAssistantRef.name` | string | Name of the `HomeAssistant` CR |
| `name` | string | Display name in the HA UI |
| `icon` | string | Material Design icon |
| `color` | string | Label colour name (e.g. `green`, `red`, `blue`) |

## Areas

Areas represent rooms or zones. They can optionally belong to a floor and carry labels.

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantArea
metadata:
  name: living-room
spec:
  homeAssistantRef:
    name: home
  name: "Living Room"
  floorName: "Ground Floor"   # matches spec.name of a HomeAssistantFloor
  icon: "mdi:sofa"
  labels:
    - "Indoor"
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantArea
metadata:
  name: garden
spec:
  homeAssistantRef:
    name: home
  name: "Garden"
  icon: "mdi:flower"
  labels:
    - "Outdoor"
```

### Area spec reference

| Field | Type | Description |
|-------|------|-------------|
| `homeAssistantRef.name` | string | Name of the `HomeAssistant` CR |
| `name` | string | Display name in the HA UI |
| `floorName` | string | `spec.name` of the target `HomeAssistantFloor` CR (optional) |
| `icon` | string | Material Design icon |
| `labels` | list | Label names (matching `spec.name` of `HomeAssistantLabel` CRs) |


## Deleting infrastructure

Each CR has a finalizer that removes the entity from HA before the CR is deleted. Delete areas before floors/labels to avoid dangling references.

```sh
kubectl delete haar --all
kubectl delete hafl --all
kubectl delete halb --all
```

## Verify

```sh
kubectl get hafl
```
```
NAME            HOMEASSISTANT   NAME            READY   AGE
ground-floor    home            Ground Floor    True    2m
```

```sh
kubectl get halb
```
```
NAME       HOMEASSISTANT   NAME       READY   AGE
outdoor    home            Outdoor    True    2m
```

```sh
kubectl get haar
```
```
NAME           HOMEASSISTANT   NAME           FLOOR          READY   AGE
living-room    home            Living Room    Ground Floor   True    1m
```
