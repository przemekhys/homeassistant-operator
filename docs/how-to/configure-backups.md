# Configure backups

*How-to — schedule Home Assistant's built-in backups. Assumes a running instance.*

Home Assistant writes backup archives to `/config/backups/` on the instance's
persistent volume. That protects you from mistakes inside Home Assistant, not
from losing the volume — for off-site copies, see
[getting backups off the cluster](#getting-backups-off-the-cluster) at the end.

## Prerequisites

Backup configuration requires a bootstrap API token. Enable `spec.bootstrap` with `createApiToken: true` before enabling backup.

## Example

```yaml
apiVersion: ha.homeassistant.io/v1
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
    createApiToken: true
  backup:
    enabled: true
    recurrence: daily
    time: "03:00:00"
    retentionCopies: 7
    includeDatabase: true
```

## Idempotency

The operator reads the current backup config from HA (`backup/config/info`) before writing. It only sends `backup/config/update` if the desired state differs from the actual state — no unnecessary WebSocket calls on every reconcile.

## Getting backups off the cluster

Home Assistant's own backups live on the same volume as the data they protect, so
they survive a mistake but not a lost volume. Snapshotting that volume is a job
for a cluster backup tool such as [Velero](https://velero.io/), which is outside
this operator entirely:

```sh
# Install Velero with an S3-compatible backend
velero install --provider aws --bucket my-ha-backups ...

# Snapshot the instance's volume every night
velero schedule create ha-daily \
  --schedule="0 4 * * *" \
  --include-namespaces default \
  --selector app=home
```

!!! note "Not part of the supported API"
    Velero is a third-party tool that this project does not ship or control. The
    commands above reflect the state at the time of writing and may need
    adjusting for a different Velero version or cluster setup.

    **Tested with**: Velero 1.15.

## Restore

Restore is performed manually through the HA UI (`Settings → System → Backups`). The operator does not automate restore operations.

## Verify

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

## Turn backups off again

Setting `spec.backup.enabled` back to `false` stops the operator managing the
schedule and clears the `BackupConfigured` condition. It does **not** delete
backups Home Assistant has already written.

## Choose the schedule

How often to create a backup.

| Value | Meaning |
|-------|---------|
| `daily` | Every day at `time` |
| `mon` … `sun` | Specific day of the week |
| `never` | Disable scheduled backups (manual only) |

### `spec.backup.time`

Time of day in `HH:MM:SS` format (e.g. `"03:00:00"`). If empty, HA picks automatically.

## Choose how much history to keep

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

## Exclude the database to shrink backups

Whether to include the HA database (`home-assistant_v2.db`) in the backup. Default: `true`.

Excluding the database significantly reduces backup size but means you lose history data on restore.

```yaml
backup:
  includeDatabase: false   # config-only backup, smaller files
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`BackupSpec` fields, with types and defaults, see the
[API reference](../reference/api.md#backupspec).
