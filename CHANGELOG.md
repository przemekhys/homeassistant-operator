# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).


## [Unreleased]

## [0.4.0] - 2026-02-28

### Added

- **Prometheus metrics for hot-reload operations** — three new domain-specific metrics exposed at `/metrics`:
  - `homeassistant_reload_total{component, result}` — counter per component (automation/scene/script) and result (success/failed/skipped)
  - `homeassistant_reload_duration_seconds{component}` — histogram (buckets: 0.5s–30s) for reload latency percentiles
  - `homeassistant_reload_retries_total{component}` — extra retry attempts beyond the first; non-zero value indicates the reload required more than one attempt
  - All data sourced from existing `ReloadResult` fields — no additional API calls required

- **HomeAssistantAutomation CRD**: Declarative automation management with hot-reload capabilities
  - Full automation definition via CRD with triggers, conditions, and actions
  - Uses `runtime.RawExtension` for flexible YAML compatibility with Home Assistant syntax
  - Aggregates multiple automation CRs into single ConfigMap (`<name>-automations`)
  - Finalizer-based deletion: regenerates ConfigMap without removed automation before CR deletion
  - Enable/disable without deletion via `spec.enabled` field
  - Short names: `haautomation`, `haauto`

- **HomeAssistantScene CRD**: Declarative scene management for Home Assistant
  - Aggregation pattern - multiple CR instances → single `scenes.yaml`
  - Entity validation with pattern regex (`domain.object_id`)
  - Flexible entity attributes support via `runtime.RawExtension`
  - Short names: `hascene`, `hasc`
  - Status tracking: Ready, LastActivated, LastReloadTime
  - Finalizer-based cleanup - regenerates ConfigMap without deleted scene
  - Auto-reload control via `spec.autoReload` (default: true)

- **HomeAssistantScript CRD**: Declarative script management for Home Assistant
  - Aggregation pattern - multiple CR instances → single `scripts.yaml`
  - Flexible sequence definition via `runtime.RawExtension`
  - Input parameters support via `spec.fields` map
  - Short names: `hascript`, `hascp`
  - Status tracking: Ready, LastReloadTime, LastReloadMethod
  - Finalizer-based cleanup - regenerates ConfigMap without deleted script
  - Auto-reload control via `spec.autoReload` (default: true)


## [0.3.0] - 2026-01-27

### Added

- **HomeAssistantConfiguration CRD**: Declarative configuration management with intelligent hot-reload capabilities
  - Full `configuration.yaml` management via `spec.configuration` field
  - Smart reload strategy: automatically determines if changes require restart or can be hot-reloaded
  - Zero-downtime updates for reloadable sections (automations, scripts, logger, input helpers, etc.)
  - Three reload strategies: `auto` (default, analyzes changes), `hot-reload` (force REST API), `restart` (force pod restart)
  - Requires bootstrap-generated API token for hot-reload functionality
  - Short names: `haconfig`, `hacfg`

### Changed

- **BREAKING CHANGE**: HomeAssistantConfiguration CRD now REQUIRED for every HomeAssistant instance
  - Every `HomeAssistant` CR must have a corresponding `HomeAssistantConfiguration` CR
  - Controller validates HomeAssistantConfiguration exists before creating StatefulSet
  - ConfigMap auto-generated from HomeAssistantConfiguration spec (pattern: `<name>-configuration`)
- **BREAKING CHANGE**: PVC naming convention changed from `<name>-config` to `<name>-data`
  - The PersistentVolumeClaim for Home Assistant data storage now uses the suffix `-data` instead of `-config`


## [0.2.0] - 2026-01-11

### Added

- **Zero-Touch Bootstrap**: Automatic Home Assistant onboarding without manual UI interaction
  - Creates admin user with credentials from Kubernetes Secret
  - Configures location, timezone, units, and currency (`spec.bootstrap.location`)
  - Sets analytics preferences (`spec.bootstrap.analytics`)
  - Generates long-lived API token via WebSocket API
  - Stores API token in Kubernetes Secret for programmatic access
- **HomeAssistantSecrets CRD**: Declarative secrets management for Home Assistant
  - References existing Kubernetes Secrets
  - Auto-generates `secrets.yaml` file
  - Automatic pod restart on secret changes (configurable via `spec.autoRestart`)
- **New haclient package**: Native Go HTTP/WebSocket client for Home Assistant API

### Changed

- Service naming simplified: now uses `<name>` instead of `<name>-homeassistant`


## [0.1.0] - 2026-01-06

### Added

- Initial release of Home Assistant Operator
- `HomeAssistant` Custom Resource Definition (CRD) with support for:
  - Version/image configuration (`spec.version`, `spec.image`)
  - Storage configuration with PVC (`spec.storage`)
  - Service configuration (ClusterIP, NodePort, LoadBalancer) (`spec.service`)
  - Ingress configuration with TLS support (`spec.ingress`)
  - Resource limits and requests (`spec.resources`)
  - Timezone configuration (`spec.timezone`)
  - External ConfigMap for `configuration.yaml` (`spec.configurationFrom`)
  - External Secret for `secrets.yaml` (`spec.secretsFrom`)
- Reconciliation controller that manages:
  - StatefulSet for Home Assistant deployment
  - PersistentVolumeClaim for data storage
  - Service for network access
  - Ingress for external access (optional)
- Health checks (liveness and readiness probes)
- Status reporting with phase, conditions, and ready state
- Multi-architecture Docker images (amd64, arm64)
- CI/CD with GitHub Actions (lint, test, e2e tests)
- k3d support for local testing

### Target Environments

- Primary: k3s on Raspberry Pi 4/5 (ARM64)
- Also supported: Any Kubernetes cluster (AMD64/ARM64)

[Unreleased]: https://github.com/przemekhys/homeassistant-operator/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.4.0
[0.3.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.3.0
[0.2.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.2.0
[0.1.0]: https://github.com/przemekhys/homeassistant-operator/releases/tag/v0.1.0
