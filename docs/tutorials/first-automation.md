# Your first automation as code

*Tutorial — a guided walkthrough. Follow every step in order; you will end up with an automation running in Home Assistant that you never typed into its UI.*

[Your first instance](first-instance.md) got Home Assistant running. This one
shows the point of running it this way: an automation that lives in a file you
can review, version and roll back, instead of in a database you can only reach
through a browser.

You will write one automation, watch the operator push it into Home Assistant,
change it, and delete it — all with `kubectl`.

Expect it to take about five minutes.

## Before you start

You need the instance from [your first instance](first-instance.md), still
running, with `kubectl get ha home` showing `READY: True`. Nothing else.

## Step 1 — Write the automation

Save this as `sunset-lights.yaml`:

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantAutomation
metadata:
  name: lights-at-sunset
  namespace: default
spec:
  homeAssistantRef:
    name: home
  alias: "Turn on lights at sunset"
  description: "Turns on the living room lights 30 minutes before sunset"
  triggers:
    - trigger: sun
      event: sunset
      offset: "-00:30:00"
  actions:
    - action: light.turn_on
      target:
        entity_id: light.living_room
  mode: single
  enabled: true
```

Read it top to bottom and it says what it does: half an hour before sunset, turn
on the living room lights. The `triggers` and `actions` blocks are Home
Assistant's own syntax, unchanged — anything you can write in the Home Assistant
UI you can write here.

`homeAssistantRef.name` points at the instance you created in the previous
tutorial. That is what ties this automation to that Home Assistant.

## Step 2 — Apply it

```sh
kubectl apply -f sunset-lights.yaml
```

You should see `homeassistantautomation.ha.homeassistant.io/lights-at-sunset created`.

## Step 3 — Watch it reach Home Assistant

```sh
kubectl get haauto lights-at-sunset
```

Within a few seconds:

```
NAME               HOMEASSISTANT   ALIAS                       ENABLED   READY   AGE
lights-at-sunset   home            Turn on lights at sunset    true      True    5s
```

`READY: True` means the operator sent the automation to Home Assistant and asked
it to reload — no restart, no downtime for anything else running there.

To see exactly what happened:

```sh
kubectl describe haauto lights-at-sunset
```

Look at the `Conditions` block:

```
Conditions:
  Type:     ReloadReady
  Status:   True
  Reason:   ReloadSuccessful
  Message:  Automation applied via REST API
  Type:     Ready
  Status:   True
  Reason:   AutomationGenerated
  Message:  Automation successfully generated and loaded
```

`Last Reload Method: api` a few lines below tells you Home Assistant picked the
change up through its API — nothing was restarted.

## Step 4 — See it in Home Assistant

Open the Home Assistant UI from step 7 of the previous tutorial and go to
**Settings → Automations & scenes**. "Turn on lights at sunset" is there, exactly
as you described it.

You can open it in the UI editor and look at it. Do not edit it there, though —
which brings us to the interesting part.

## Step 5 — Change it from Kubernetes

Edit `sunset-lights.yaml` and change the offset from 30 minutes before sunset to
15:

```yaml
      offset: "-00:15:00"
```

Apply it again:

```sh
kubectl apply -f sunset-lights.yaml
```

Refresh the automation in the Home Assistant UI. It now says 15 minutes. The
operator pushed the change and reloaded, again without restarting anything.

This is the whole idea: the file is the truth, and Home Assistant follows it. If
you had made that edit in the UI instead, the next reconcile would have replaced
it — see
[who owns the generated resources](../explanation/resource-ownership.md) for why
that is deliberate rather than annoying.

## Step 6 — Delete it

```sh
kubectl delete haauto lights-at-sunset
```

Check the Home Assistant UI again: the automation is gone from there too. The
operator removed it from Home Assistant before letting Kubernetes delete the
resource.

**You are done.** You created, changed and removed a Home Assistant automation
without once editing it in Home Assistant.

## Where to go next

- Scenes and scripts work exactly the same way:
  [manage scenes](../how-to/manage-scenes.md) and
  [manage scripts](../how-to/manage-scripts.md).
- To keep files like `sunset-lights.yaml` in Git and have them applied
  automatically, see the [Flux CD guide](../ecosystem/flux.md).
- For every field you can set on an automation, see the
  [API reference](../reference/api.md#homeassistantautomationspec).
