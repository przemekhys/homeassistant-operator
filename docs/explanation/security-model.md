# The security model

*Explanation — what the operator is allowed to do, and where the boundary between operator and workload runs. Nothing here needs a cluster.*

The operator is a high-privilege component: it reconciles custom resources across
namespaces and holds credentials for the Home Assistant instances it manages. The
design answer is not to make it less capable, but to keep the blast radius
explicit — the operator's own workload is locked down hard, while the Home
Assistant pods it manages are deliberately left alone, because they legitimately
need privileges the operator never does.

## What the operator's own namespace enforces

The operator's own namespace can carry Pod Security Admission labels — but only
when the chart owns it (`namespace.create=true`). That is **not** the default: a
namespace you create yourself, or one Helm creates with `--create-namespace`,
carries no such labels and enforces nothing. The labels the chart applies are:

```yaml
pod-security.kubernetes.io/enforce: restricted
pod-security.kubernetes.io/enforce-version: latest
pod-security.kubernetes.io/audit: restricted
pod-security.kubernetes.io/audit-version: latest
pod-security.kubernetes.io/warn: restricted
pod-security.kubernetes.io/warn-version: latest
```

The controller-manager pod already satisfies `restricted`:

- runs as a non-root user (`runAsNonRoot: true`),
- `seccompProfile: RuntimeDefault`,
- `allowPrivilegeEscalation: false`,
- all Linux capabilities dropped (`capabilities.drop: ["ALL"]`),
- no host namespaces, `hostPath` volumes, or host ports.

Version `latest` means the namespace always applies the newest `restricted` rules and
automatically tightens on cluster upgrades.

The operator pod satisfies `restricted` either way — the labels decide whether the
cluster *enforces* it, not whether the operator complies. See
[enforce Pod Security Standards](../how-to/enforce-pod-security.md) for turning
enforcement on.

## Why Home Assistant pods are out of scope

!!! warning "Home Assistant pods are out of scope"
    This enforcement applies **only to the operator's own workloads**. Home Assistant
    pods run in their own namespaces and are deliberately **not** placed under
    `restricted`. Many Home Assistant setups need elevated privileges (for example
    `hostNetwork`, or access to USB/Zigbee devices), which `restricted` would block.

## Clusters without Pod Security Admission

Pod Security Admission is a cluster feature. On clusters where it is disabled (or that
predate it), the labels are **inert** — they never block installation. The pod's
`securityContext` remains compliant, so enforcement takes effect immediately once PSA
is enabled.

## How wide the operator's permissions are

By default the operator watches every namespace in the cluster, which needs a
cluster-wide role binding. That is the widest permission it can hold, and it is
being phased out: naming the namespaces you actually use in `watchNamespaces`
switches it to per-namespace bindings instead.

The default is wide because it is the one that works without you knowing in
advance where Home Assistant will live. The trade-off is real, so the install
warns about it rather than staying quiet — see
[install the operator](../how-to/install-operator.md).

One permission the operator deliberately does **not** hold is `pods/exec`. It
would be the obvious way to fix things inside a running Home Assistant container,
and it is also a permission that lets its holder run anything, as anyone, in any
pod it can reach. Where the operator needs to change something inside the
container — clearing an IP ban, for instance — it restarts the pod with an
init-container instead. That is clumsier, and it keeps the permission off the
list. See
[IP ban self-recovery](reconciliation-model.md#ip-ban-self-recovery).

## Where the credentials live

Dedicated bootstrap creates a Home Assistant identity separate from the human
onboarding owner. It explicitly assigns the `system-admin` administrator group:
configuration, integration, and automation management require administrative
access, so a normal user role cannot support the enabled capabilities. This is
identity isolation, not a narrower Home Assistant role.

The dedicated token is stored only in `spec.bootstrap.operatorAccount.tokenSecretName`
in the HomeAssistant resource namespace, under `data.token`, with an Opaque type,
managed-by and purpose labels, and a HomeAssistant controller owner reference.
Existing conflicting Secrets are rejected rather than overwritten. A temporary
local password is used to log in as the dedicated identity and issue its token;
it is never persisted in Kubernetes. Status and diagnostics contain identifiers
and reasons, never credential values.

Routine management uses only the dedicated token after verification. The
administrator-provided bootstrap Secret is retained solely for explicit recovery
and is not read during ordinary reconciliation. Manual deletion disables
recovery until valid owner credentials are supplied again. Token loss reports
degradation; it never authorizes an automatic account or password reset.

The existing managed-namespace Secret RoleBinding is sufficient. Dedicated mode
adds no operator-namespace token permissions, cross-namespace Secret reads,
cache namespaces, or required ClusterRoleBinding. Namespace-scoped installations
may still configure independent webhook certificate permissions.

Omitting the new configuration preserves legacy administrator-derived tokens
and their existing namespace-local Secret. Migration is explicit and retains
the old Secret for administrator-directed cleanup; see
[bootstrap and migration](../how-to/bootstrap-instance.md) and
[credential recovery](../how-to/recover-operator-credential.md).

Legacy credential mode, including omission of `operatorAccount`, is deprecated
since v1.7.0 and scheduled for removal in v2.0.0. Existing installations must
migrate to dedicated mode before upgrading to v2.0.0.

That Secret is worth treating as what it is: full control of the Home Assistant
instance. Anyone who can read Secrets in that namespace can read it, which is one
more reason to narrow `watchNamespaces` and keep Home Assistant in a namespace of
its own.

Secrets you feed *into* Home Assistant work the other way round: the operator
reads them and composes `secrets.yaml`, so the values end up on the instance's
volume. They are as protected as that volume is, not more.

## The admission webhook

The operator ships a validating admission webhook that rejects incoherent
resources at `kubectl apply` time rather than letting them fail later. It runs
with `failurePolicy: Ignore` by default, meaning that if the webhook is
unavailable — during an operator rollout, say — resources are admitted
unvalidated instead of being blocked.

That is the deliberate choice: this webhook exists to catch mistakes, not to be a
security control, and an unavailable webhook that blocks every change would take
your smart home down to prevent a typo.

## See also

- [Enforce Pod Security Standards](../how-to/enforce-pod-security.md) — the task.
- [Verify signed releases](../how-to/verify-signed-releases.md) — checking that
  what you install is what the project published.
