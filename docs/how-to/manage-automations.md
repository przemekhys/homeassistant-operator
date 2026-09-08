# Manage automations

*How-to — create, change and delete Home Assistant automations as Kubernetes resources. Assumes a running instance.*


## Prerequisites

- A running Home Assistant instance with [bootstrap completed](bootstrap-instance.md) — the operator needs its API token

## Basic example

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantAutomation
metadata:
  name: lights-at-sunset
spec:
  homeAssistantRef:
    name: home
  alias: "Turn on lights at sunset"
  description: "Turns on living room lights 30 minutes before sunset"
  triggers:
    - trigger: sun
      event: sunset
      offset: "-00:30:00"
  conditions:
    - condition: state
      entity_id: binary_sensor.someone_home
      state: "on"
  actions:
    - action: light.turn_on
      target:
        entity_id: light.living_room
      data:
        brightness: 200
        color_temp: 400
  mode: single
  autoReload: true
  enabled: true
```

## Advanced example — multiple triggers and actions

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantAutomation
metadata:
  name: security-alert
spec:
  homeAssistantRef:
    name: home
  alias: "Security alert"
  triggers:
    - trigger: state
      entity_id: binary_sensor.motion_detector
      from: "off"
      to: "on"
    - trigger: state
      entity_id: binary_sensor.front_door
      from: "off"
      to: "on"
  conditions:
    - condition: state
      entity_id: binary_sensor.someone_home
      state: "off"
    - condition: time
      after: "22:00:00"
      before: "06:00:00"
  actions:
    - action: light.turn_on
      target:
        entity_id: all
      data:
        brightness: 255
    - action: notify.mobile_app
      data:
        title: "Security Alert!"
        message: "Motion detected while nobody is home"
    - delay:
        minutes: 5
    - action: light.turn_off
      target:
        entity_id: all
  mode: restart
```

## Disabling without deleting

```yaml
spec:
  enabled: false
```

The automation remains in HA but is disabled. Re-enable by setting `enabled: true`.

## Deleting an automation

```sh
kubectl delete haauto lights-at-sunset
```

The finalizer calls `DELETE /api/config/automation/config/lights-at-sunset` before removing the CR. If HA is unavailable, the finalizer still completes (best-effort) to avoid stuck CRs.

## Verify

```sh
kubectl get haauto lights-at-sunset
```
```
NAME               HOMEASSISTANT   ALIAS                      ENABLED   READY   AGE
lights-at-sunset   home            Turn on lights at sunset   true      True    3m
```

```sh
kubectl describe haauto lights-at-sunset
```
```
Conditions:
  Type:     ReloadReady
  Status:   True
  Reason:   ReloadSuccessful
  Message:  Automation applied via REST API
  Type:     Ready
  Status:   True
  Reason:   AutomationGenerated
Last Reload Method:  api
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantAutomation` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantautomationspec).
