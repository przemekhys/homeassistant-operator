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

## Create one backup now

The operator uses Home Assistant's native backup mechanism. It does not upload,
encrypt, or retain archives itself. To request one backup without enabling a
schedule, keep `backup.enabled: true` with `recurrence: never`, then set a new
opaque request identifier on the HomeAssistant object:

```sh
if request_id=$(uuidgen) && test -n "$request_id"; then
  kubectl annotate homeassistant home \
    "ha.homeassistant.io/backup-request=$request_id" --overwrite
else
  echo "Could not generate a request ID" >&2
fi
```

The request identifier is consumed before Home Assistant receives the command.
If communication fails after that point, the operator reports an unknown result
instead of retrying and possibly making two backups. Inspect Home Assistant and
use a new identifier only when you deliberately want another backup.

`enabled: false` (or omitting `backup`) stops operator management; it does not
change a schedule already stored by Home Assistant. In contrast, `recurrence:
never` explicitly disables that native schedule.

## Backup destinations and keys

`agentIDs` accepts agent identifiers advertised by the individual Home
Assistant instance, including user-configured integrations. The operator does
not install integrations or replace an unavailable requested destination with a
different one.

When Home Assistant exposes a backup encryption key, the operator retains each
observed distinct value in an Opaque Secret. The Secret is deliberately not
owned by the HomeAssistant object, so it survives deletion of that object.
Export the relevant entry to protected storage outside the cluster. Do not put
the key, API token, or a downloaded archive in terminal history, CI artifacts,
or tickets. A Secret in the same cluster is not a disaster-recovery copy.

Export one selected entry without printing it:

```bash
(
set -euo pipefail
umask 077
export_dir=$(mktemp -d)
secret_name=$(kubectl get ha home -o jsonpath='{.status.backup.keySecretRef.name}')
secret_key=$(kubectl get ha home -o jsonpath='{.status.backup.keySecretRef.key}')
test -n "$secret_name"
test -n "$secret_key"
kubectl get secret "$secret_name" -o json | \
  jq -er --arg key "$secret_key" '.data[$key]' | base64 --decode >"$export_dir/recovery-key"
test -s "$export_dir/recovery-key"
chmod 600 "$export_dir/recovery-key"
printf 'Protected export directory: %s\n' "$export_dir"
)
```

The current reference may not match an older archive: choose the key observed
when that archive was made. The retained Secret preserves prior different keys,
but Kubernetes still enforces its size limit; export keys and manually remove
only entries you no longer need if it fills. Deleting the `HomeAssistant` keeps
this Secret, while deleting its namespace or the cluster does not.

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

Configuration is separate from evidence that a copy completed. Check the
observation and bounded result separately:

```sh
kubectl get ha home -o jsonpath='{.status.backup}' | jq
```

`lastSuccessfulBackupTime` appears only after Home Assistant reports an
archive with every configured destination. `lastAttempt.outcome: Unknown`
means the operator cannot prove the result; inspect Home Assistant before
submitting a new request ID. An unavailable observation does not erase an older
time, but that older value is not proof of a fresh copy.

## Turn backups off again

Setting `spec.backup.enabled` back to `false` stops the operator managing the
schedule. It does **not** disable that schedule or delete backups Home Assistant
has already written.

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
