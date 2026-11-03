# Native backups and retained encryption keys

> Development preview: native backup qualification is still in progress. The
> production capability catalog currently grants no one-shot backup execution.
> A passing mock test, a configured schedule, or a retained key is not evidence
> that a particular Home Assistant image has passed native qualification.

The operator uses Home Assistant's native backup agents. It does not implement
S3 uploads, unpack archives, install backup providers, or change your encryption
key. Configure encryption, provider credentials and destination protection in
Home Assistant first. Community agents must expose the native backup interface;
an unrelated uploader is not a native agent. S3 compatibility must be tested
against the actual endpoint, not inferred from its name.

## Policy ownership and scheduling

An existing `spec.backup` object defaults to `managementPolicy: Managed`:

```yaml
spec:
  backup:
    managementPolicy: Managed
    enabled: true
    recurrence: daily
    time: "03:00:00"
    retentionCopies: 7
    includeDatabase: true
    agentIDs:
      - backup.local
```

- `enabled: false` means native recurrence `never`, not simply hiding a Condition.
  This changes the old behavior where disabling could leave HA's schedule active.
  Choose `Unmanaged` before upgrading if you want HA's existing policy untouched.
- `managementPolicy: Unmanaged` leaves HA's settings unchanged. Keep the backup
  object with this policy if you need ongoing key observation without managing
  scheduling. Removing the entire backup object releases management as well.
- Omitted recurrence defaults to daily; omitted/empty time delegates time selection
  to HA. `includeDatabase` defaults to true. An omitted agent list in a managed
  policy selects `backup.local`; an explicit empty list is invalid.
- Agent IDs form a set. Multiple or remote-only destinations are permitted when
  HA supports them; an unavailable required agent must not silently fall back to
  another destination. All selected destinations are required for full success.
- Positive `retentionCopies` and `retentionDays` configure native cleanup; an
  omitted dimension has no limit. **Retention can destroy older recovery points.**
  Retaining a key does not preserve an archive that HA or an administrator deletes.
- Managed fields can be reconciled after UI changes. The encryption key and
  provider credentials remain owned by HA/the administrator, not operator policy.

`BackupConfigured` describes configuration, not archive health. A native command
acknowledgement is not completed distribution. Inspect operation phase,
`BackupHealthy`, required-destination results and the last confirmed success.
Partial destination failure is not full success. Unknown outcomes are not
permission to resubmit the same operation.

## One-shot backups

A `HomeAssistantBackup` is one immutable request bound to the live target UID.
Use a fresh operation name for a deliberately new request; do not recreate an
uncertain operation to try to make it run again. Omitted `agentIDs` on an operation
inherit HA's configured selection, unlike the policy's local-agent default.
Resolved parameters are recorded before native dispatch.

Execution requires a qualified running image, a verified nonempty current key,
retained key evidence and protected required destinations. A retained Secret
alone does **not** prove the generated archive is encrypted. The operator does
not disable encryption or choose a fallback destination to bypass a failure.

Use HA's native original-archive download and keep the archive confidential.
Never put a decryption password in a download URL. A UI download may be decrypted;
do not assume that a downloaded archive remains protected. Verify the applicable
native download behavior during qualification and store archives accordingly.

## Key history and its limits

The observation interval is approximately 60 seconds with jitter. Each distinct
observed, nonempty key is retained in an immutable, randomly named Secret under
the instance's namespace and UID. Status exposes references, not values or public
password fingerprints. Errors and empty observations must leave old keys intact.

History Secrets have no garbage-collecting owner reference to HA or the operation
and no automatic TTL. Instance deletion must not delete history; a new instance
with the same name has a different UID and does not inherit that history.
Namespace or cluster deletion can still destroy every retained Secret.

Only **observed** keys are retained. There is no guarantee for keys from before
the operator started, rotations while it was unavailable, or several rotations
between observations. This applies to native scheduled backups too. A reference
to today's key does not establish that an older archive has a usable key.

Kubernetes Secrets are operational copies, not an external disaster-recovery
system. Base64 is not encryption. Restrict Secret access with RBAC, configure etcd
encryption at rest and protect control-plane backups. Keep an independent copy of
the applicable key outside both HA and the Kubernetes cluster, separate from
the archive. Do not commit keys, token snapshots, exported Secrets or archives to
Git, paste them into tickets, or print them in CI output.

## Export to an approved external location

Choose a destination in your organisation's vault/offline-media process first;
the operator does not select or upload to an external destination. The following
exports a complete Secret JSON file containing **base64-encoded secret material**,
not encrypted data. It is only an intermediate input to that approved process.

1. Disable shell tracing. Select the intended Kubernetes context and namespace.
   Obtain the current key Secret name from the HA status reference without
   displaying Secret data. For historical keys, select by the old instance UID:

   ```bash
   kubectl --context="$CONTEXT" get secrets -n "$NAMESPACE" \
     -l "ha.homeassistant.io/instance-uid=$INSTANCE_UID,ha.homeassistant.io/purpose=backup-key-history" \
     -o custom-columns=NAME:.metadata.name,UID:.metadata.uid,IMMUTABLE:.immutable
   ```

2. Set `KEY_SECRET` to the reviewed name and `KEY_EXPORT_DIR` to the approved,
   access-controlled external export directory, **not a repository or CI artifact
   directory**. Save using restrictive permissions and without displaying content:

   ```bash
   set +x
   umask 077
    EXPORT_FILE=$(mktemp "$KEY_EXPORT_DIR/ha-backup-key.XXXXXXXX.json") || exit 1
   if ! kubectl --context="$CONTEXT" get secret "$KEY_SECRET" -n "$NAMESPACE" \
       -o json > "$EXPORT_FILE"; then
     rm -f -- "$EXPORT_FILE"
     exit 1
   fi
   ```

   Export errors are not successful exports. Do not `cat` the file, use `set -x`,
   put its contents into command arguments, or upload it with other test artifacts.

3. Import the file through the approved vault/offline procedure. Verify that an
   authorised recovery operator can retrieve the correct nonempty key without
   printing it, including from outside this cluster. Verify correspondence to
   the intended instance history and archive. Keep a non-secret record of archive
   identity, target UID, Secret UID and the external recovery record reference.

4. Follow the external store's retention and local-file disposal policy. Deleting
   a local file does not guarantee secure erasure on an SSD or a copy-on-write
   filesystem. Do not assume that the local export is the independent copy.
   Never blindly apply an exported Secret to a different target or namespace.

Actual recovery usability requires a separately authorised test restore on a
disposable target with the supplied archive and key. Merely exporting a file,
accepting an upload or obtaining an HTTP 200 does not prove recoverability.

## Explicit key deletion

**Deleting the only usable key can make every associated encrypted archive
permanently unreadable.** The operator does not automatically delete key history
when archives expire, an instance disappears or an operation completes.

Before deleting a particular history Secret:

1. Review its name, Secret UID, instance UID and purpose. Ensure it is the intended
   immutable history Secret, not a current active key or another operation's input.
   Coordinate with other administrators so it cannot be replaced during review.
2. Inventory retained archives at **all** destinations, including manually exported
   or offline archives. Establish whether any still need this key. Do not infer
   disuse from the latest backup status or the absence of a current HA instance.
3. Verify independent retrieval of the key from the approved external store, or
   explicitly accept that all dependent archives will become unusable. Do not
   assume that a newer key can decrypt archives protected with an older value.
4. Obtain explicit authorisation for that specific Secret and consequence. Do not
   delete an entire label-selected history set or automate this from retention.
5. Recheck identity immediately before the narrowly scoped deletion:

   ```bash
   kubectl --context="$CONTEXT" delete secret "$KEY_SECRET" -n "$NAMESPACE"
   ```

6. Record the approved deletion without recording key contents. A still-observed
   current key may be retained again later; deletion is not a way to change HA's
   encryption policy or disable observation.

## Archive scope and recovery boundaries

Native archives cover HA data actually included in that archive. Local recorder
history is included only if requested and supported. External databases, MQTT,
Kubernetes manifests, PVC provisioning, device passthrough and cluster state are
not made recoverable by an HA archive. A Git copy of YAML configuration does not
replace encrypted HA state, authentication data and the applicable key.

Operator synchronization pauses do not stop native HA automations or native
backup schedules and are not device isolation. Full pause and restore safety
remain separate prerequisites; do not treat partially implemented development
controls as permission for a destructive production restore.
