# Manage scripts

*How-to — create, change and delete Home Assistant scripts as Kubernetes resources. Assumes a running instance.*


## Prerequisites

- A running Home Assistant instance with [bootstrap completed](bootstrap-instance.md) — the operator needs its API token

## Basic example

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantScript
metadata:
  name: notify-mobile
spec:
  homeAssistantRef:
    name: home
  alias: "Notify Mobile Devices"
  description: "Send a notification to all mobile devices"
  icon: "mdi:cellphone-message"
  mode: queued
  max: 5
  fields:
    message:
      description: "The message to send"
      example: "Dinner is ready!"
      selector:
        text:
          multiline: false
    title:
      description: "Notification title"
      example: "Alert"
      selector:
        text:
          multiline: false
  sequence:
    - service: notify.mobile_app_phone
      data:
        title: "{{ title }}"
        message: "{{ message }}"
    - delay:
        seconds: 1
    - service: notify.mobile_app_tablet
      data:
        title: "{{ title }}"
        message: "{{ message }}"
  autoReload: true
```

## Advanced example — conditional sequence

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantScript
metadata:
  name: arrive-home
spec:
  homeAssistantRef:
    name: home
  alias: "Arrive Home"
  description: "Actions to run when someone arrives home"
  icon: "mdi:home-account"
  mode: single
  sequence:
    - action: light.turn_on
      target:
        entity_id: light.entrance
      data:
        brightness: 255
    - action: climate.set_temperature
      target:
        entity_id: climate.living_room
      data:
        temperature: 21
    - condition: time
      after: "18:00:00"
      before: "23:00:00"
    - action: media_player.turn_on
      target:
        entity_id: media_player.living_room
  autoReload: true
```

## Execution modes

| Mode | Behaviour |
|------|-----------|
| `single` | Only one run at a time; new triggers while running are ignored |
| `restart` | Stop current run and start fresh on each trigger |
| `queued` | Queue new runs; execute sequentially (up to `max`) |
| `parallel` | Allow multiple simultaneous runs (up to `max`) |

## Calling a script from an automation

```yaml
actions:
  - action: script.notify_mobile
    data:
      title: "Alert"
      message: "Front door opened"
```

## Verify

```sh
kubectl get hascp notify-mobile
```
```
NAME            HOMEASSISTANT   ALIAS                   MODE     READY   AGE
notify-mobile   home            Notify Mobile Devices   queued   True    2m
```

```sh
kubectl describe hascp notify-mobile
```
```
Conditions:
  Type:     ReloadReady
  Status:   True
  Reason:   ReloadSuccessful
  Type:     Ready
  Status:   True
  Reason:   ScriptGenerated
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantScript` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantscriptspec).
