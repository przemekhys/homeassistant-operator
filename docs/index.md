# Home Assistant Operator

[![Lint](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml)
[![E2E Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml)
[![Security Scan](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

A Kubernetes operator that simplifies deploying and managing [Home Assistant](https://www.home-assistant.io/) instances on Kubernetes clusters, with a primary focus on lightweight environments like k3s on Raspberry Pi.

## What is it?

The Home Assistant Operator automates the full lifecycle of Home Assistant on Kubernetes. Instead of managing Deployments, Services, PVCs and config files by hand, you declare the desired state in custom resources and the operator handles the rest — including zero-touch bootstrap, hot-reload of configuration changes, and declarative management of automations, scenes, scripts and integrations.

```yaml
apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistant
metadata:
  name: home
spec:
  version: "2025.6"
  storage:
    size: 10Gi
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-admin
    createAPIToken: true
```
## Is this for you?

!!! warning "This is one of the most demanding ways to run Home Assistant"
    Deploying Home Assistant on Kubernetes via an operator is a deliberate engineering choice — not a beginner-friendly shortcut. If you don't already have Kubernetes experience, this setup will likely cause more frustration than it solves.

    **This project is a good fit if you:**

    - Already run a homelab Kubernetes cluster
    - Are comfortable with `kubectl`, CRDs, RBAC and StatefulSets
    - Want a **GitOps workflow** where your entire HA configuration lives in a git repository
    - Treat your smart home infrastructure the same way you treat production software

    **This project is probably not for you if you:**

    - Are new to Home Assistant or just want to get it running quickly
    - Don't have an existing Kubernetes cluster
    - Prefer managing HA through its UI rather than YAML files

    ---

    **Looking for an easier alternative?**
    The official [Home Assistant OS](https://www.home-assistant.io/installation/) or the
    [Home Assistant Community Add-on: Git pull](https://github.com/home-assistant/addons/tree/master/git_pull)
    give you version-controlled configuration without requiring Kubernetes.
    The Git pull add-on syncs your `/config` directory from a git repository on each HA start —
    a great middle ground between full GitOps and manual UI management.

## Key Features

- **Zero-touch bootstrap** — operator creates the initial admin user, completes onboarding, and generates a long-lived API token automatically
- **Declarative configuration** — manage `configuration.yaml` as a Kubernetes CR with smart hot-reload (no restart for automation/script/logger changes)
- **GitOps-ready automations** — each automation, scene and script is a separate CR synced to HA via REST API
- **Integration management** — register MQTT, ESPHome and other integrations via Config Flow API
- **Infrastructure as code** — manage floors, labels and areas (house structure) as CRDs

## Supported CRDs

| CRD | Short Name | Description |
|-----|-----------|-------------|
| `HomeAssistant` | `ha` | Core HA deployment (StatefulSet + PVC + Service) |
| `HomeAssistantSecrets` | `hasec` | Aggregate K8s Secrets → `secrets.yaml` |
| `HomeAssistantConfiguration` | `haconfig` | Manage `configuration.yaml` with hot-reload |
| `HomeAssistantAutomation` | `haauto` | Individual automations via REST API |
| `HomeAssistantScene` | `hasc` | Individual scenes via REST API |
| `HomeAssistantScript` | `hascp` | Individual scripts via REST API |
| `HomeAssistantIntegration` | `haint` | Register integrations via Config Flow API |
| `HomeAssistantFloor` | `hafl` | House floor registry |
| `HomeAssistantLabel` | `halb` | Entity label registry |
| `HomeAssistantArea` | `haar` | Room/area registry with floor + label refs |

## Project Status

**Alpha** — APIs may change between minor versions until v1.0.0.

[Get started →](getting-started/installation.md){ .md-button .md-button--primary }
[View on GitHub →](https://github.com/przemekhys/homeassistant-operator){ .md-button }
