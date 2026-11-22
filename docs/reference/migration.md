# Existing-instance migration reference

*Reference — the migration resource, its phases, and the approvals that permit each irreversible stage.*

The `HomeAssistantMigration` resource records the durable intent for one existing-instance migration. It stores references and digests, never token values, recovery-key values, archive contents, or an approval only in status. The generated [API reference](api.md#homeassistantmigrationspec) is the complete field-level contract.

## Operation inputs

| Input | Meaning |
|---|---|
| `sourceID` and `sourceEndpoint` | Immutable source identity and endpoint from the approved scan. |
| `target` | Immutable target `HomeAssistant` name and UID. |
| `reportID` | Immutable identity of the accepted scan report. |
| `sourceTokenSecretRef`, `recoveryKeySecretRef` | Names and keys of Secrets in the migration namespace. |
| `archiveStorage` | Independent archive PVC and descriptor names. |
| `sourceWorkload` | Optional in-cluster `Deployment` or `StatefulSet` observed before restore. |
| `verification` | Non-secret values to verify after restore. |

## Approval IDs

| ID | Permits |
|---|---|
| `backup` | The backup execution request, bound to the report, target, backup scope, and verification set. |
| `backup-settings-stable` | Confirmation that the approved backup settings have not changed. |
| `source-stop` | Restore preparation after the source workload has been observed stopped. |
| `restore` | Upload and restore of one observed archive to one target. |
| `resume` | Release after a verified restored target result. |
| `manual-cutover` | The final manual routing decision. |

Every approval has `approved: true` and is bound to the current target UID and stage-specific digests. Restore, resume, and manual-cutover also bind the archive and verification digests. An approval is valid only until its request is accepted; changing its bound input requires a new review.

## Phases

| Phase | Meaning |
|---|---|
| `AwaitingBackupApproval` | No current, bound backup approval is available. |
| `AwaitingRestoreApproval` | A backup is available but no current restore approval is available. |
| `AwaitingSourceStop` | Restore is approved but the source-stop evidence is absent or invalid. |
| `AwaitingVerification` | Restore was accepted; the target has not passed the configured verification. |
| `AwaitingResumeApproval` | Verification passed and awaits a bound resume approval. |
| `AwaitingManualCutover` | Resume was approved; routing remains an administrator action. |
| `Completed` | The manual-cutover approval was accepted. |
| `Blocked` | A required input, binding, or safety check is invalid. |

## Terminal actions

`spec.cancelRequested: true` prevents new requests and starts local worker cleanup. `resolutions` is append-only and records either `ObserveExternalResult` for an ambiguous accepted request or `Abort` to stop it. Neither action claims to undo a request already accepted by Home Assistant.
