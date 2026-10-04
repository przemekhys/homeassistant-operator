# Manage Dashboards

`HomeAssistantDashboard` manages one storage-mode Home Assistant dashboard.
The referenced Home Assistant must be bootstrapped so the operator can use its
administrator API token.

## Inline Dashboard

```yaml
apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantDashboard
metadata:
  name: kitchen
spec:
  homeAssistantRef:
    name: home
  title: Kitchen
  urlPath: kitchen
  inline: |
    title: Kitchen
    views:
      - title: Overview
        cards: []
```

Changing `inline` replaces the complete configuration of the same dashboard.
Deleting the resource removes that dashboard only.

## ConfigMap Source

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: dashboards
data:
  kitchen.yaml: |
    title: Kitchen
    views: []
---
apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantDashboard
metadata:
  name: kitchen
spec:
  homeAssistantRef:
    name: home
  title: Kitchen
  urlPath: kitchen
  configMapKeyRef:
    name: dashboards
    key: kitchen.yaml
```

Only changes to the selected ConfigMap key update the dashboard.

## Default Dashboard

Set `defaultDashboard: true` to manage the storage dashboard at the `lovelace`
path. This is explicit because it replaces the primary dashboard configuration.

```yaml
spec:
  homeAssistantRef:
    name: home
  title: Home
  defaultDashboard: true
  inline: |
    views: []
```

YAML-mode, built-in, and per-user dashboards are not supported.
