# Deprecations

This file tracks all deprecated features, their deprecation version, planned removal, and migration path.

| Feature | Deprecated since | Planned removal | Reason | Migration |
|---------|-----------------|-----------------|--------|-----------|
| [ClusterRoleBinding mode](#clusterrolebinding-mode-watchnamespaces) | v1.1.0 | v2.0.0 | Violates least-privilege — operator gains access to all namespaces in the cluster | Set `watchNamespaces` in Helm values |
| [Legacy Home Assistant credential mode](#legacy-home-assistant-credential-mode) | v1.7.0 | v2.0.0 | Routine management uses the human administrator's identity | Migrate to `spec.bootstrap.operatorAccount.mode: dedicated` |

---

## ClusterRoleBinding mode (`watchNamespaces: []`)

**Deprecated since:** v1.1.0
**Planned removal:** v2.0.0
**Affects:** Helm chart users with default `values.yaml` (i.e. `watchNamespaces` not set)

### Why

When `watchNamespaces` is empty, the operator receives a `ClusterRoleBinding` granting it access to secrets, configmaps, and other resources **across all namespaces** in the cluster. This violates the principle of least privilege — in practice, the operator only needs access to the namespaces where Home Assistant is deployed.

### Migration

Set `watchNamespaces` to the list of namespaces where Home Assistant runs:

```yaml
# values.yaml
watchNamespaces:
  - homeassistant
  - homeassistant-dev
```

This switches from a `ClusterRoleBinding` to per-namespace `RoleBinding` objects, restricting the operator's access to only those namespaces.

If you are using kustomize instead of Helm:
1. Remove `config/rbac/role_binding.yaml` (the `ClusterRoleBinding`).
2. Apply `config/rbac/watched_namespace_role_binding.yaml` in each watched namespace.
3. Set the `WATCH_NAMESPACES` environment variable on the operator `Deployment`.

---

## Legacy Home Assistant credential mode

**Deprecated since:** v1.7.0
**Planned removal:** v2.0.0
**Affects:** HomeAssistant resources with `spec.bootstrap.operatorAccount` omitted
or `spec.bootstrap.operatorAccount.mode` set to `legacy`.

### Why

Versions before v1.7.0 use an administrator-derived token for routine Home
Assistant management. Dedicated mode, introduced in v1.7.0, separates the
operator's identity from the human onboarding owner and limits bootstrap
credential use to onboarding and explicit recovery.

Legacy behavior remains available throughout v1.x for compatibility. Upgrading
does not migrate accounts or delete existing token Secrets automatically.

### Migration

Before upgrading to v2.0.0, follow the
[dedicated account migration procedure](docs/how-to/bootstrap-instance.md#migrate-an-existing-installation).
Select `dedicated` mode with an unused namespace-local `tokenSecretName`, retain
the valid legacy API token with Home Assistant administrator permissions during
migration, and wait for
`OperatorCredentialReady=True` and `migrationPhase: Completed`. Only then revoke
the legacy token and remove its Secret deliberately.

Migration uses the existing token rather than owner password login, so owner MFA
can remain enabled. Missing or invalid legacy tokens block migration with an
actionable condition; bootstrap credentials are not used as a fallback.

Rollback to `legacy` remains available in v1.x while the old token is valid;
complete migration before upgrading to v2.0.0.
