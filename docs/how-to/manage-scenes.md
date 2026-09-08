# Manage scenes

*How-to — create, change and delete Home Assistant scenes as Kubernetes resources. Assumes a running instance.*


## Prerequisites

- A running Home Assistant instance with [bootstrap completed](bootstrap-instance.md) — the operator needs its API token

## Basic example

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantScene
metadata:
  name: movie-night
spec:
  homeAssistantRef:
    name: home
  name: "Movie Night"
  icon: "mdi:movie"
  entities:
    - entity_id: light.living_room
      state: "on"
      attributes:
        brightness: 30
        color_temp: 500
    - entity_id: cover.blinds
      state: "closed"
    - entity_id: media_player.tv
      state: "on"
  autoReload: true
```

## Advanced example — multiple rooms

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantScene
metadata:
  name: good-morning
spec:
  homeAssistantRef:
    name: home
  name: "Good Morning"
  icon: "mdi:weather-sunny"
  entities:
    - entity_id: light.bedroom
      state: "on"
      attributes:
        brightness: 80
        color_temp: 250
    - entity_id: light.kitchen
      state: "on"
      attributes:
        brightness: 255
    - entity_id: cover.bedroom_blinds
      state: "open"
    - entity_id: climate.living_room
      state: "heat"
      attributes:
        temperature: 21
  autoReload: true
```

## Activating a scene

Scenes are activated from the HA UI, automations, or via the REST API:

```sh
curl -X POST http://<ha-host>:8123/api/services/scene/turn_on \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"entity_id": "scene.movie_night"}'
```

## Deleting a scene

```sh
kubectl delete hasc movie-night
```

The finalizer removes the scene from HA before deleting the CR.

## Verify

```sh
kubectl get hasc movie-night
```
```
NAME          HOMEASSISTANT   NAME          ENTITIES   READY   AGE
movie-night   home            Movie Night   3          True    1m
```

```sh
kubectl describe hasc movie-night
```
```
Conditions:
  Type:     ReloadReady
  Status:   True
  Reason:   ReloadSuccessful
  Type:     Ready
  Status:   True
  Reason:   SceneGenerated
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantScene` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantscenespec).
