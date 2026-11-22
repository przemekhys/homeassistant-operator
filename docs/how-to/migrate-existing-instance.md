# How to migrate an existing Home Assistant instance

*How-to — move an existing instance through the experimental, operator-managed migration workflow.*

> [!WARNING]
> This workflow is experimental. It has been verified only with Home Assistant Container 2026.11.0 and a one-shot worker in k3d. It has no downtime SLO and does not switch traffic automatically. The operator never stops the source or changes a Service, Ingress, or Gateway for you.

## Before you begin

- Create a **new**, empty `HomeAssistant` target in the destination namespace. Record its name and UID; do not reuse or share the source PVC.
- Ensure that the operator can create Jobs and an independent archive PVC in that namespace.
- Store the source token and recovery key in separate Kubernetes Secrets. Do not put either value in a URL, manifest, status, report, or log.
- Plan a manual traffic cutover and rollback. Keep the source stopped throughout validation of the restored target.

For fields, phases, and exact approval bindings, use the generated [`HomeAssistantMigration` API reference](../reference/api.md#homeassistantmigrationspec).

## 1. Scan the source

```sh
ha-operator-migrate scan \
  --source-url https://ha-source.example \
  --source-token-secret ha-source-token \
  --namespace home \
  --target ha-new > migration-report.json
```

The scan is read-only: it does not write to Home Assistant or Kubernetes. Inspect the report ID and every classification before proceeding. `Importable` has a verified mapper; `Incomplete`, `Unsupported`, and `Conflict` entries need manual handling. Do not commit the report to Git.

`backup: Ready` means that the preflight succeeded. It does not mean a backup exists or that a restore has been verified.

## 2. Create the paused migration operation

After checking the content ID manually, create the operation with an explicit confirmation:

```sh
ha-operator-migrate import \
  --report migration-report.json \
  --report-id sha256:... \
  --namespace home \
  --target ha-new \
  --name ha-existing-migration \
  --source-token-secret ha-source-token \
  --confirm
```

Inspect its status without serialising Secrets:

```sh
kubectl -n home get homeassistantmigration ha-existing-migration -o yaml
```

The command stores Secret *references*, never values. The operation and every imported resource start paused. Removing an imported resource does not delete the corresponding object in Home Assistant.

## 3. Review imported resources

Remove a resource's pause only when its declaration matches the source object. The operator then performs a read-only identity and canonical-field comparison. `MigrationAdoption=True` with reason `Adopted` confirms that comparison; it does not enable normal synchronisation or deletion.

If a comparison conflicts, correct the declaration or keep the resource under manual management. Do not try to force an overwrite: the operator deliberately reports `UnsafeOverwriteUnsupported` rather than making an unprovable write.

## 4. Run the controlled backup and restore

1. Set the archive PVC and descriptor names, recovery-key reference, source workload identity (when the source is in the cluster), and non-secret verification set on the `HomeAssistantMigration`. See the [`HomeAssistantMigration` API](../reference/api.md#homeassistantmigrationspec).
2. Review the reported backup scope, selected agent, archive storage, and target UID. Add the bound `backup` and `backup-settings-stable` approvals.
3. Wait for the backup to complete, then record the backup ID, agent ID, and archive digest from the operation. An `Unknown` result is not permission to submit another request.
4. Stop the source **yourself** and confirm that it has no process or active connections. Network unreachability alone is not evidence of a stopped source. Add the bound `source-stop` approval.
5. Enable `spec.alpha.migrationRestore.enabled` on the empty target. Add the bound `restore` approval for the observed archive and restore scope.
6. Wait for post-restore verification. Pod readiness or HTTP 2xx is not a successful restore. Verify the administrator identity, token, MFA, integrations, and selected data before adding the `resume` approval.
7. After the operation reaches `AwaitingManualCutover`, add the `manual-cutover` approval and switch Service, Ingress, or Gateway routing yourself. Keep the source stopped while observing the target.
8. When the target is accepted, explicitly set `spec.alpha.migrationRestore.enabled: false` to re-enable its normal bootstrap and configuration projections.

## 5. Cancel safely

Set `spec.cancelRequested: true` to prevent new stages. The controller stops local Jobs and waits for their Pods to disappear before releasing its finaliser. It cannot undo a request already accepted by Home Assistant.

If the outcome of an accepted request is unknown, append either an `ObserveExternalResult` resolution with the matching evidence or an `Abort` resolution. Do not delete receipts, archives, or worker resources to make the operation retry; wait for garbage collection after local work has ended.

## Check the result

The migration is ready for manual cutover only when its phase is `AwaitingManualCutover`. It is complete after the bound `manual-cutover` approval is accepted. A completed operation does not prove that traffic has been switched; verify routing separately.
