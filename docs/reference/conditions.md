# Status conditions

*Reference — every condition the operator sets, and what each `reason` means. Look things up here; it does not teach.*

Conditions follow the Kubernetes convention: `type`, `status`, `reason`,
`message` and `lastTransitionTime`, with `reason` in PascalCase. Read them with:

```sh
kubectl get homeassistant home -o jsonpath='{.status.conditions}' | jq
```

A condition with `status: False` is not necessarily an error. Most of the
reasons below describe a resource that is **waiting** for something and will
retry on its own — see
[the reconciliation model](../explanation/reconciliation-model.md) for why the
operator waits rather than failing.

Reasons that appear on more than one resource mean the same thing everywhere:

| Reason | Meaning |
|--------|---------|
| `TokenNotAvailable` | The instance's API token Secret does not exist yet. Transient: the operator retries. |
| `HANotReady` / `HomeAssistantNotReady` | The referenced Home Assistant is not serving yet. Transient. |
| `ReconciliationFailed` | A genuine failure while reconciling. The `message` carries the detail. |

## HomeAssistant

### `Ready`

| Reason | Meaning |
|--------|---------|
| `StatefulSetReady` | Home Assistant is running |
| `StatefulSetNotReady` | The pod exists but is not serving yet |
| `Pending` | Child resources are still being created |
| `WaitingForConfiguration` | No `HomeAssistantConfiguration` names this instance. Transient — apply one, in any order. |
| `ReconciliationFailed` | See `message` |

### `BootstrapReady`

| Reason | Meaning |
|--------|---------|
| `BootstrapCompleted` | Admin user created and API token stored |
| `BootstrapInProgress` | Onboarding is under way |
| `BootstrapAlreadyDone` | Home Assistant was already onboarded; the operator adopted it |
| `HomeAssistantNotReady` | Waiting for Home Assistant to answer |
| `MissingCredentials` | The Secret named in `spec.bootstrap.credentials` is missing or incomplete |
| `LoginRecoveryFailed` | Onboarding was already done and the given credentials did not work |
| `BootstrapFailed` | See `message` |

### `BanRecoveryFailed`

Home Assistant can ban the operator's own IP after repeated failed logins. The
operator recovers by restarting the pod — see
[IP ban self-recovery](../explanation/reconciliation-model.md#ip-ban-self-recovery).

| Reason | Meaning |
|--------|---------|
| `RecoveryInProgress` | A self-unban restart has been triggered |
| `RestartLimitExceeded` | The rate limit was hit; manual intervention needed |

### `DevicesReady`

| Reason | Meaning |
|--------|---------|
| `NoDevicesDeclared` | `spec.alpha.devices` is empty — nothing to do |
| `DevicesMounted` | Every declared device was found and mounted |
| `DeviceUnavailable` | A declared device is not present on the node |

### `SchedulingReady`

| Reason | Meaning |
|--------|---------|
| `NoConstraintsDeclared` | `spec.scheduling` is empty — nothing to do |
| `Scheduled` | The pod was placed within the declared constraints |
| `Unschedulable` | No node satisfies the constraints |

### `BackupConfigured`

| Reason | Meaning |
|--------|---------|
| `BackupConfigured` | Schedule applied in Home Assistant |
| `BackupConfigFailed` | Home Assistant rejected the backup configuration |
| `TokenNotAvailable` | Waiting for the API token |

### `CertManagerAvailable`

| Reason | Meaning |
|--------|---------|
| `CertManagerInstalled` | cert-manager CRDs were found on the cluster |
| `CertManagerNotInstalled` | cert-manager is absent. Not an error: exposure keeps working over HTTP and certificates appear once cert-manager is installed. |

### `ExposureReady`

| Reason | Meaning |
|--------|---------|
| `ExposureReady` | Ingress or Gateway resources are reconciled |

!!! note "`TLSReady` is gone"
    Older releases set a `TLSReady` condition for the removed native-TLS mode.
    The operator now strips it from any resource that still carries it.

## HomeAssistantConfiguration

### `Ready`

| Reason | Meaning |
|--------|---------|
| `ConfigurationGenerated` | `configuration.yaml` was generated and applied |
| `InvalidConfiguration` | The supplied YAML could not be parsed |
| `ReloadFailed` | Home Assistant refused the reload |
| `ReconciliationFailed` | See `message` |

### `HTTPConfigReady`

Applies only on Home Assistant 2026.8 and newer, where the `http:` section is
managed through Home Assistant's API instead of `configuration.yaml`.

| Reason | Meaning |
|--------|---------|
| `Applied` | The HTTP configuration was written through the API |
| `ManagedInYaml` | This Home Assistant predates the API; the `http:` section stays in `configuration.yaml` |
| `WaitingForHomeAssistant` | Cannot tell yet which path applies. Transient. |
| `Rejected` | Home Assistant rejected the pending change; the operator cleared it |
| `ForeignPendingChange` | Someone else has a pending HTTP change; the operator will not promote it |
| `UnreadableSection` | The `http:` section uses `!include` and cannot be read |

## HomeAssistantSecrets

### `Ready`

| Reason | Meaning |
|--------|---------|
| `SecretsGenerated` | `secrets.yaml` was composed and applied |
| `InvalidConfiguration` | A referenced Secret or key is missing |
| `ReconciliationFailed` | See `message` |

## HomeAssistantAutomation / HomeAssistantScene / HomeAssistantScript

Both conditions are set on all three resources.

### `Ready`

| Reason | Meaning |
|--------|---------|
| `AutomationGenerated` / `SceneGenerated` / `ScriptGenerated` | Accepted by Home Assistant |
| `InvalidAutomation` / `InvalidScene` / `InvalidScript` | Home Assistant rejected the definition |
| `TokenNotAvailable` | Waiting for the API token |
| `ReconciliationFailed` | See `message` |

### `ReloadReady`

| Reason | Meaning |
|--------|---------|
| `ReloadSuccessful` | Home Assistant picked the change up without a restart |
| `TokenNotAvailable` | Waiting for the API token |

If the reload attempts are exhausted, reconciliation does **not** fail: the
change is already stored and will take effect the next time Home Assistant
starts.

## HomeAssistantIntegration

### `Ready`

| Reason | Meaning |
|--------|---------|
| `IntegrationConfigured` | The config flow completed and an entry was created |
| `AlreadyConfigured` | The integration already existed; the operator adopted its entry without reconfiguring |
| `ConfigFlowFailed` | Home Assistant's config flow returned an error |
| `SecretResolutionFailed` | A `secretKeyRef` in `spec.configuration` could not be resolved |
| `TokenNotAvailable` | Waiting for the API token |
| `HANotReady` | Waiting for Home Assistant |

!!! note "`IntegrationReady` is gone"
    Older releases used an `IntegrationReady` condition type. It is now `Ready`,
    and the operator removes the old one when it sees it.

## HomeAssistantFloor / HomeAssistantLabel / HomeAssistantArea

### `Ready`

| Reason | Meaning |
|--------|---------|
| `FloorReady` / `LabelReady` / `AreaReady` | Created or updated in Home Assistant's registry |
| `FloorFailed` / `LabelFailed` / `AreaFailed` | The registry call failed; see `message` |
| `FloorNotFound` | (Areas only) The referenced floor name does not exist yet. Transient — apply order does not matter. |
| `TokenNotAvailable` | Waiting for the API token |
| `HANotReady` | Waiting for Home Assistant |

## HomeAssistantCommunityRepository

Experimental (`v1alpha1`). `status.phase` carries the same information in a
single field: `Pending → Validating → Installing → Installed`, or `Failed`.

### `Ready`

| Reason | Meaning |
|--------|---------|
| `Validating` | Fetching the repository and checking its structure |
| `Installing` | Files written; waiting for the extension to become active |
| `Installed` | Done, and `status.installedVersion` now reports this ref. For `plugin`/`theme`/`python_script`/`template` that means Home Assistant confirmed the reload; for `integration` it means the pod restart was triggered — the extension becomes usable once the replacement pod is running. |
| `RepositoryUnreachable` | The repository or the pinned ref could not be fetched |
| `CategoryMismatch` | The repository declares a different category than `spec.category` |
| `StructureInvalid` | The repository does not match the layout its category requires |
| `TargetConflict` | Another resource already installs the same target into this instance |
| `ActivationTimeout` | Files were written but the extension did not become active in time |
| `HomeAssistantNotReady` | Waiting for Home Assistant |
