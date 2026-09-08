# Events

*Reference — every Kubernetes Event the operator emits. Look things up here; it does not teach.*

Events record things that happened; [conditions](conditions.md) record the state
a resource is in. Read them with:

```sh
kubectl describe homeassistant home        # Events are at the bottom
kubectl get events --field-selector involvedObject.name=home
```

Kubernetes discards Events after about an hour by default, so treat them as a
recent-activity log rather than a record you can rely on later. A resource that
reconciles without incident emits **no** events at all — in particular, a
successful automation, scene or script reload is reported only as the
`ReloadReady` condition with reason `ReloadSuccessful`, never as an event.

## HomeAssistant

| Event | Type | Meaning |
|-------|------|---------|
| `BanRecoveryRestart` | Normal | Home Assistant had banned the operator's IP; the pod was restarted to clear the ban |
| `BanRecoveryLimitReached` | Warning | The self-unban rate limit was hit; the operator stopped restarting and needs manual help |
| `BackupConfigured` | Normal | The backup schedule was applied |
| `BackupConfigFailed` | Warning | Home Assistant rejected the backup configuration |
| `CertificateRequested` | Normal | A cert-manager `Certificate` was created for the instance |
| `CertManagerUnavailable` | Warning | A cert-manager-backed mode is enabled but cert-manager is not installed. Exposure keeps working over HTTP. |
| `ExposureConfigured` | Normal | Ingress or Gateway resources were reconciled |
| `NativeTLSRemoved` | Warning | Emitted once on an instance that still had the removed native-TLS mode enabled, while cleaning up after it |

## HomeAssistantConfiguration

| Event | Type | Meaning |
|-------|------|---------|
| `HTTPConfigRejected` | Warning | Home Assistant rejected the pending HTTP configuration; the operator cleared it |
| `HTTPConfigForeignChange` | Warning | A pending HTTP change was made by something other than the operator, so it was left alone |

## HomeAssistantIntegration

| Event | Type | Meaning |
|-------|------|---------|
| `IntegrationConfigured` | Normal | The config flow completed and an entry was created |
| `IntegrationAdopted` | Normal | An existing entry was adopted instead of being recreated |
| `IntegrationReconfigured` | Normal | `spec.configuration` changed, so the entry was replaced |
| `IntegrationRemoved` | Normal | The entry was deleted from Home Assistant |
| `IntegrationFailed` | Warning | The config flow or an API call failed |

## HomeAssistantCommunityRepository

| Event | Type | Meaning |
|-------|------|---------|
| `RepositoryValidated` | Normal | The repository was fetched and its structure accepted |
| `RepositoryInstalled` | Normal | The extension was written and activated — for `integration`, that the pod restart was triggered rather than that the new pod is running |
| `RepositoryRemoved` | Normal | The installed files were removed |
| `RepositoryConflict` | Warning | Another resource already installs the same target into this instance |
| `RepositoryInstallFailed` | Warning | The resource entered `Failed` for any reason — fetch, validation, activation timeout, or a target conflict, in which case it follows `RepositoryConflict` |
