# Compatibility

*Reference — the versions this operator is built and tested against. Look things up here; it does not teach.*

## Kubernetes

| | |
|---|---|
| Minimum | **1.24** — declared as the chart's `kubeVersion` constraint, so Helm refuses to install on anything older |
| Tested in CI | k3s **v1.36.4** (end-to-end suite, on k3d) |
| Distributions | Any conformant distribution. The project targets k3s on Raspberry Pi first, but nothing in it is k3s-specific. |

## Home Assistant

The operator does not pin a Home Assistant version: `spec.version` is passed
straight through to the image tag, and `stable` is the default.

| Behaviour | Applies to |
|-----------|------------|
| `http:` managed through Home Assistant's API | **2026.8 and newer** |
| `http:` managed in `configuration.yaml` | Older than 2026.8 |

### Native backups

Native `backup.local` creation, archive discovery, streamed protected-archive
download, onboarding upload/info/restore, and a near-term automatic schedule
were exercised against Home Assistant Container **2026.11.0**. Native recovery on fresh Container targets
also preserved a Local To-do integration, entity identity, and item. A selected
local recorder state was recovered when the database was included and was
absent when it was excluded; the Local To-do file survived both cases.

It does not qualify Home Assistant OS Apps, external databases, external backup
agents/services, credential validity after restore, MFA recovery, a full HA OS
migration, or configuration-object adoption. The migration scan currently
reports integrations as unsupported because the full secret-safe config-flow
export and adoption protocol has not been verified.
See [recover a native backup](../how-to/recover-native-backup.md) for the
manual, isolated recovery procedure; a successful backup observation is not a
claim that recovery has been verified.

### Migration backup archive validation

The isolated qualification scenario exercises Home Assistant Container
**2026.11.0**, `backup.local`, an actual short-lived worker Job and an independent
archive PVC. Its validated profile is
`ha-container-2026.11.0-securetar-v3`: outer TAR metadata version 2 and one
encrypted Home Assistant SecureTar v3 gzip/TAR payload. Other HA versions,
Supervisor Apps, additional folders, external databases and external backup
agents are not qualified by this result.

The worker binds the accepted request to the exact HA job/backup ID and agent,
persists final source evidence before downloading, and authenticates the full
local payload, including its final tag. It checks gzip CRC, TAR structure and
metadata against the observed source result without extracting plaintext.
SHA-256 and size cover the same locally reopened file. Neither a hash alone nor
source-side recovery-key checking establishes local archive validity. The outer
metadata is not described as cryptographically signed.

| Bound | Limit |
|-------|-------|
| Backup metadata | 1 MiB |
| SecureTar plaintext block | 1 MiB, plus the format's authentication overhead |
| Decompressed TAR stream | 64 GiB |
| TAR entries | 1,000,000 |
| Path length / cumulative path bytes | 4 KiB / 64 MiB |
| Worker Job deadline | 55 minutes |
| Automatic read-only continuations | 3, separated by at least one minute |

Absolute/traversing paths, duplicate entries, links, unsupported entry types,
GNU TAR extensions and unrecognised metadata are rejected. Standard PAX
path/size/time fields are accepted. Worker Jobs use `Never`, zero backoff and no
history TTL. A recreated descriptor cannot reuse a previous Job's success:
the validation Pod's termination message binds the exact descriptor UID and
archive digest. Recovery-key references pin Secret UID and resourceVersion;
neither key values nor key hashes are recorded in evidence.

The qualification test reached `BackupReady`, stopped its disposable source,
lost operation status and the descriptor, and reconstructed readiness by
authenticating the same file from the PVC in another read-only Job. Instrumented
HA traffic contained exactly one backup-generation request and no upload or
restore. Generation uses HA's WebSocket API, not an HTTP POST. Network-policy
enforcement was checked with allowed and denied clients and blocked source
egress; worker RBAC did not permit listing or watching Secrets.

`BackupReady` describes a verified restore input, not a successful restore or
working integrations. Backup settings and key stability remain an explicit
operational requirement, not an atomic HA lock.

### Experimental migration restore

One full disposable-cluster scenario passed on HA Container **2026.11.0** with
an independent archive PVC, an isolated one-shot worker and a fresh target. It
proved exactly one backup generation, upload and restore request; source stop;
restored token/data verification; then separate `resume` and `manual-cutover`
approvals. The target Core restart during restore was included. This does not
qualify HA OS, external databases/agents, MFA recovery, a production network
policy setup, or automatic traffic cutover.

Archive PVCs, archive descriptors, worker Jobs/receipts and migration-created
credentials are owned by `HomeAssistantMigration`. Cancellation or deletion
stops local worker Pods before their finalizer permits garbage collection. An
already accepted Home Assistant request is not rolled back. `Unknown` must be
resolved by observation of bound evidence or explicitly aborted; it must not be
retried as a new mutation.

### Migration adoption

The scanner currently qualifies only complete, unambiguous Floor objects for
import; production migration backup and restore remain disabled. Read-only adoption of manually prepared Floor,
Area, Label, Scene, Script and storage Dashboard resources is covered by
controller tests against simulated HA responses; it does not establish full
end-to-end or version compatibility for all kinds. A manually prepared Floor
was additionally tested end-to-end on HA Container **2026.11.0**, including
conflict, restart, status reconstruction, disappearance and safe CR deletion.
Read-only adoption and safe CR deletion of manually prepared related Area and
Label objects, Scene, Script and storage Dashboard were also tested on that
version, including restart, status reconstruction, conflict, disappearance and
safe CR deletion. The scan and paused import of Floor were tested end-to-end; import of
the other kinds remains unsupported.
Automation and Integration remain blocked
when their full managed configuration cannot be verified. Adoption never
enables update or delete in HA; removing an imported CR leaves the HA object in
place, including when the migration owner is garbage-collected. See the
[migration guide](../how-to/migrate-existing-instance.md) for approval and
current limitations.

The operator detects which of the two applies at runtime, on every reconcile, so
upgrading or rolling back Home Assistant needs no change on your side. See
[manage configuration](../how-to/manage-configuration.md#http-configuration-on-home-assistant-20268).

## Container image architectures

The operator image is published for `linux/arm64`, `linux/amd64`, `linux/s390x`
and `linux/ppc64le`.

## Optional cluster components

None of these are required to install the operator. Each one unlocks a feature,
and its absence is reported as a status condition rather than an error — see
[the reconciliation model](../explanation/reconciliation-model.md).

| Component | Needed for | Without it |
|-----------|-----------|------------|
| [cert-manager](https://cert-manager.io/) | TLS certificates for Ingress and Gateway exposure | `CertManagerAvailable=False`; exposure keeps working over HTTP, and certificates appear once you install it |
| [Gateway API](https://gateway-api.sigs.k8s.io/) CRDs | `spec.gateway` exposure | Use `spec.ingress` instead |
| Pod Security Admission | Enforcing the `restricted` profile on the operator's namespace | The namespace labels are inert; the operator pod is compliant regardless |
| A default StorageClass | Home Assistant's persistent volume | The instance's volume stays `Pending` |

## Building from source

| | |
|---|---|
| Go | **1.27** or newer (`go.mod`) |
| Container runtime | Docker or Podman |

See [contributing](../development/contributing.md).
