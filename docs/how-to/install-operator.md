# Install the operator

*How-to — get the operator running on a cluster. Assumes you have a cluster and `kubectl` access.*

Set `VERSION` to the release you want — the
[changelog](../reference/changelog.md) lists them — or drop the `--version` flag
entirely to install the latest.

```sh
VERSION=1.4.0
```

!!! note "Registry tags carry no leading `v`"
    The git tag is `v1.4.0`, but the published chart and image are tagged
    `1.4.0`. Use the form without `v` in every command on this page.

## Prerequisites

- Kubernetes cluster v1.24+
- `kubectl` configured to access your cluster

## Install via Helm (recommended)

```sh
helm install homeassistant-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --version "$VERSION" \
  --namespace homeassistant-operator-system \
  --create-namespace \
  --set 'watchNamespaces={homeassistant}'
```

Set `watchNamespaces` to the namespaces where you actually run Home Assistant.
Leaving it out makes the operator watch the whole cluster through a
`ClusterRoleBinding` — more permission than it needs, **deprecated since v1.1.0**
and due for removal in v2.0.0. See
[which namespaces the operator watches](#which-namespaces-the-operator-watches).

!!! warning "Watched namespaces must exist before you install"
    The chart creates a `RoleBinding` inside each namespace you list, so a
    namespace that does not exist yet fails the install outright:
    `namespaces "homeassistant" not found`. `--create-namespace` covers only the
    release namespace, not the watched ones. Create them first:

    ```sh
    kubectl create namespace homeassistant
    ```

    The same applies on upgrade if you add a namespace to the list.

### Customise the installation

Download the default values and override what you need:

```sh
helm show values oci://ghcr.io/przemekhys/charts/homeassistant-operator --version "$VERSION" > values.yaml
# edit values.yaml — set watchNamespaces there, it is empty by default:
#   watchNamespaces:
#     - homeassistant
# then:
helm install homeassistant-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --version "$VERSION" \
  --namespace homeassistant-operator-system \
  --create-namespace \
  -f values.yaml
```

### Which namespaces the operator watches

Name every namespace that will hold Home Assistant resources. Each one gets its own `RoleBinding`, and the operator gets no permissions anywhere else:

```yaml
# values.yaml
watchNamespaces:
  - homeassistant
  - homeassistant-dev
```

| Mode | `watchNamespaces` | RBAC generated | Scope |
|------|-------------------|----------------|-------|
| Namespace-scoped (recommended) | non-empty list | one `RoleBinding` per listed namespace | Only listed namespaces |
| Cluster-wide (deprecated) | `[]` | `ClusterRoleBinding` | Every namespace, now and in future |

When set, the operator receives per-namespace `RoleBinding` objects instead of the `ClusterRoleBinding`, and the `WATCH_NAMESPACES` environment variable is injected automatically.

!!! warning "The operator's own namespace is not auto-included"
    Add `homeassistant-operator-system` to the list explicitly if you deploy `HomeAssistant` resources into the same namespace as the operator itself.

!!! warning "Leaving `watchNamespaces` empty is deprecated"
    An empty `watchNamespaces` gives the operator a cluster-wide
    `ClusterRoleBinding`. That mode is **deprecated since v1.1.0** and planned for
    removal in **v2.0.0**; Helm prints a warning on every install that uses it.
    See [DEPRECATIONS.md](https://github.com/przemekhys/homeassistant-operator/blob/main/DEPRECATIONS.md).

**Migrating with kustomize** (non-Helm users):

1. Remove `config/rbac/role_binding.yaml` (the `ClusterRoleBinding`).
2. Apply `config/rbac/watched_namespace_role_binding.yaml` in each watched namespace.
3. Set the `WATCH_NAMESPACES` environment variable on the operator `Deployment`.

### Upgrade

```sh
helm upgrade homeassistant-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --version "$VERSION" \
  --namespace homeassistant-operator-system
```

### Uninstall (Helm)

!!! warning
    Delete all custom resources **before** removing the operator. The operator must be running to process finalizers (automation/scene/script/integration cleanup). Deleting the operator first causes CRs with finalizers to hang indefinitely.

```sh
# 1. Delete all custom resources (keep operator running to process finalizers)
kubectl delete homeassistants --all -A
kubectl delete homeassistantconfigurations --all -A
kubectl delete homeassistantsecrets --all -A
kubectl delete homeassistantautomations --all -A
kubectl delete homeassistantscenes --all -A
kubectl delete homeassistantscripts --all -A
kubectl delete homeassistantintegrations --all -A

# 2. Uninstall the Helm release (removes the operator and CRDs)
helm uninstall homeassistant-operator -n homeassistant-operator-system
```

## Install via manifest

If you prefer a plain `kubectl apply` without Helm:

```sh
MANIFEST_URL="https://github.com/przemekhys/homeassistant-operator/releases/download/v$VERSION/install.yaml"
kubectl apply -f "$MANIFEST_URL"
```

The manifest is an immutable asset of the selected release and deploys the
matching operator image. To verify its signed checksum before applying it, see
[Verify signed releases](verify-signed-releases.md#verify-the-installation-manifest).

This installs:

- All CRDs (`HomeAssistant`, `HomeAssistantSecrets`, `HomeAssistantConfiguration`, etc.)
- The operator `Deployment` in namespace `homeassistant-operator-system`
- RBAC (`ClusterRole`, `ClusterRoleBinding`, `ServiceAccount`)

### Uninstall (manifest)

!!! warning
    Delete all custom resources **before** removing the operator (same reason as above).

```sh
# 1. Delete all custom resources
kubectl delete homeassistants --all -A
kubectl delete homeassistantconfigurations --all -A
kubectl delete homeassistantsecrets --all -A
kubectl delete homeassistantautomations --all -A
kubectl delete homeassistantscenes --all -A
kubectl delete homeassistantscripts --all -A
kubectl delete homeassistantintegrations --all -A

# 2. Remove the operator and CRDs
MANIFEST_URL="https://github.com/przemekhys/homeassistant-operator/releases/download/v$VERSION/install.yaml"
kubectl delete -f "$MANIFEST_URL"
```

## Verify

```sh
kubectl get pods -n homeassistant-operator-system
```
```
NAME                                      READY   STATUS    RESTARTS   AGE
homeassistant-operator-5f4946ff79-s6f9s   1/1     Running   0          39s
```

Then check that the custom resource definitions registered:

```sh
kubectl get crd | grep homeassistant.io
```

You should see one entry per custom resource the operator manages. If the pod is
running but nothing reconciles, see
[the operator does not reconcile resources in a namespace](troubleshoot.md#operator-does-not-reconcile-resources-in-a-namespace).
