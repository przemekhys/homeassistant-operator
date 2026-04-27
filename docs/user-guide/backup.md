# Backup

The operator configures Home Assistant's **built-in backup system** by sending a `backup/config/update` WebSocket command. No separate CRD is needed — backup is a section of the `HomeAssistant` spec.

HA stores backup archives in `/config/backups/` on the PVC. For off-site storage, use [Velero](https://velero.io/) to snapshot the PVC to S3 or NFS.

## Prerequisites

Backup configuration requires a bootstrap API token. Enable `spec.bootstrap` with `createAPIToken: true` before enabling backup.

## Example

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
  backup:
    enabled: true
    recurrence: daily
    time: "03:00:00"
    retentionCopies: 7
    includeDatabase: true
```

## Spec reference

### `spec.backup.enabled`

Set to `true` to activate backup configuration. Default: `false`.

When set back to `false` after being enabled, the operator clears the `BackupConfigured` status condition.

### `spec.backup.recurrence`

How often to create a backup.

| Value | Meaning |
|-------|---------|
| `daily` | Every day at `time` |
| `mon` … `sun` | Specific day of the week |
| `never` | Disable scheduled backups (manual only) |

### `spec.backup.time`

Time of day in `HH:MM:SS` format (e.g. `"03:00:00"`). If empty, HA picks automatically.

### `spec.backup.retentionCopies`

Number of backup archives to keep. Older ones are deleted automatically. If omitted, unlimited retention.

```yaml
backup:
  retentionCopies: 7   # keep last 7 backups
```

### `spec.backup.retentionDays`

Number of days to keep backup archives. If omitted, unlimited retention.

```yaml
backup:
  retentionDays: 30    # keep backups for 30 days
```

`retentionCopies` and `retentionDays` can be combined — HA applies whichever limit is reached first.

### `spec.backup.includeDatabase`

Whether to include the HA database (`home-assistant_v2.db`) in the backup. Default: `true`.

Excluding the database significantly reduces backup size but means you lose history data on restore.

```yaml
backup:
  includeDatabase: false   # config-only backup, smaller files
```

## Status

```sh
kubectl get ha home -o jsonpath='{.status.conditions}' | jq '.[] | select(.type=="BackupConfigured")'
```
```json
{
  "type": "BackupConfigured",
  "status": "True",
  "reason": "BackupConfigured",
  "message": "Backup configuration applied successfully"
}
```

### Condition reasons

| Reason | Meaning |
|--------|---------|
| `BackupConfigured` | Schedule applied in HA |
| `BackupConfigFailed` | WebSocket command returned an error |
| `TokenNotAvailable` | Bootstrap token not ready; requeuing |

## Idempotency

The operator reads the current backup config from HA (`backup/config/info`) before writing. It only sends `backup/config/update` if the desired state differs from the actual state — no unnecessary WebSocket calls on every reconcile.

## Off-site backup with Velero

HA backups on the PVC are only as durable as the underlying storage. For cloud-native durability, snapshot the PVC with Velero:

```sh
# Install Velero with AWS S3 backend
velero install --provider aws --bucket my-ha-backups ...

# Schedule PVC snapshot
velero schedule create ha-daily \
  --schedule="0 4 * * *" \
  --include-namespaces default \
  --selector app=home
```

## Restore

Restore is performed manually through the HA UI (`Settings → System → Backups`). The operator does not automate restore operations.
