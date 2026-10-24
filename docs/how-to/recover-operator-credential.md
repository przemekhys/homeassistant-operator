# Recover the operator credential

*How-to — deliberately restore a dedicated Home Assistant operator token.*

## Diagnose the condition

```sh
kubectl get ha home -o jsonpath='{.status.conditions}'
kubectl get ha home -o jsonpath='{.status.bootstrap.operatorAccount}'
```

`CredentialMissing`, `CredentialMalformed`, and `CredentialRejected` report
absent, invalid, and rejected credentials respectively. `SecretAccessDenied`
means the manager lacks Secret access in the HomeAssistant namespace.
`HomeAssistantUnavailable` is a service-availability wait, with bounded retry;
it does not authorize credential recovery. Restore service availability first.

## Request one recovery attempt

Prerequisites:

- Dedicated mode is enabled with a namespace-local `tokenSecretName`.
- The referenced bootstrap Secret contains the current onboarding **owner**
  username and password. Home Assistant requires its owner to reset another
  user's local password; an ordinary administrator is insufficient.
- The operator has the existing managed-namespace Secret permissions.

Choose a new non-empty request ID for each intentional attempt:

```sh
kubectl patch ha home --type=merge -p '{"spec":{"bootstrap":{"operatorAccount":{"recoveryRequestID":"incident-2026-10-24-1"}}}}'
kubectl wait ha home --for=condition=OperatorCredentialReady --timeout=5m
kubectl get ha home -o jsonpath='{.status.bootstrap.operatorAccount}'
```

Success records the request ID and `recoveryResult: Succeeded`, verifies the
dedicated principal and administrator role, then restores credential readiness.
Reapplying the same observed ID does not reset a password or issue another
token. A failed or interrupted request remains observable and requires a new ID
after its prerequisites have been repaired.

Recovery resets only the dedicated user's temporary local password. That
password is held in memory during login and token issuance, then discarded.
The human administrator password is never reset or synchronized. Replacement
tokens are stored only under `data.token` in the managed Secret.

## Deleted bootstrap Secret or conflicting token Secret

The operator never recreates the administrator-provided bootstrap Secret.
Manual deletion disables recovery until you supply current owner credentials
under the referenced name and submit a new request ID. Routine management does
not depend on that Secret once a dedicated token is verified.

An unrelated Secret at `tokenSecretName` is never overwritten. Choose an unused
name during initial setup, or restore the original managed Secret and owner
reference. Do not relabel unrelated credentials to bypass the ownership check.

## Interrupted provisioning

The managed Secret records non-sensitive account, issuance, and recovery
checkpoints. A stored token can be reverified after a restart without issuance.
If the process stopped after changing a temporary password but before storing a
token, the operator requires an explicit recovery request rather than guessing
the lost password or issuing another token automatically. Revoke any orphaned
tokens in Home Assistant after resolving the incident.

Keep the managed Secret's metadata when restoring it from backup. Recovery
request annotations provide replay protection across status loss; deliberately
deleting both the ledger Secret and resource status removes that history.
