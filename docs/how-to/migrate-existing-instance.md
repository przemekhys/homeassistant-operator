# How to migrate an existing Home Assistant instance

*How-to — prepare and import an existing instance with the experimental migration workflow.*

> [!WARNING]
> This workflow is experimental. You can scan and import a migration today, but
> the current release does not provide a supported user-facing workflow for all
> execution approvals needed to run backup and restore. The operator never
> stops the source or switches Service, Ingress, or Gateway traffic for you.

## Before you begin

- Create a **new**, empty `HomeAssistant` target in the destination namespace
  with `spec.alpha.migrationRestore.enabled: true` from its first apply. Record
  its name and UID; do not reuse or share the source PVC.
- Do not let the ordinary controller create `<target>-data`. The migration owns
  the fresh PVC used for restore. If an empty PVC already exists, delete it and
  wait for deletion before proceeding; if it contains data, create a new target
  instead.
- Store the source token and recovery key in separate Kubernetes Secrets. Do
  not put either value in a URL, manifest, status, report, or log.
- Plan manual traffic cutover and rollback. Keep the source stopped while
  validating a restored target.

## 1. Scan the source

```sh
ha-operator-migrate scan \
  --source-url https://ha-source.example \
  --source-token-secret ha-source-token \
  --namespace home \
  --target ha-new > migration-report.json
```

The scan is read-only: it does not write to Home Assistant or Kubernetes.
Inspect the report ID and every classification before proceeding.

## 2. Create the paused migration operation

```sh
ha-operator-migrate import \
  --report migration-report.json \
  --report-id sha256:... \
  --namespace home \
  --target ha-new \
  --name ha-existing-migration \
  --source-token-secret ha-source-token \
  --recovery-key-secret ha-recovery-key \
  --confirm
```

The command stores Secret *references*, never values. Inspect the operation
without serialising Secrets:

```sh
kubectl -n home get homeassistantmigration ha-existing-migration -o yaml
```

## 3. Review imported resources

Imported resources start paused. Remove a resource's pause only after its
declaration matches the source object. The operator performs a read-only
identity and canonical-field comparison; it does not enable normal
synchronisation or deletion.

Conflicts and unsupported objects need manual handling. Removing an imported
resource does not delete the corresponding object in Home Assistant.

## 4. Prepare for a future execution workflow

Set the archive PVC and descriptor names, source workload identity when the
source is in the cluster, and non-secret verification set on the
`HomeAssistantMigration`.

Do not create execution approvals manually. The current CLI and status do not
expose supported sources for every required approval binding, so backup and
restore cannot yet be completed safely through this guide. For field details
and the execution protocol, use the generated
[`HomeAssistantMigration` API reference](../reference/api.md#homeassistantmigrationspec).

## Cancel safely

Set `spec.cancelRequested: true` to prevent new stages. The controller stops
local Jobs and waits for their Pods to disappear before releasing its finaliser.
It cannot undo a request already accepted by Home Assistant.

Do not delete receipts, archives, or worker resources to force a retry. If an
accepted request has an unknown outcome, resolve it with bound evidence or
explicitly abort it, then wait for garbage collection after local work ends.
