# Upgrade the operator

*How-to — move an existing installation to a newer version. Assumes the operator is already installed with Helm.*

Helm does **not** upgrade CRDs on `helm upgrade`. The explicit CRD step below is
the part people miss, and skipping it is what makes new fields silently not work.


## Prerequisites

- An existing Helm installation of the operator
- `helm` and `kubectl` configured for the cluster

Set the target version once; every command below uses it. Note that the registry
tag has no leading `v`, even though the git tag does:

```sh
VERSION=1.4.0
```

Every namespace named in `watchNamespaces` must already exist — the chart puts a
`RoleBinding` in each one, and a missing namespace fails the command. See
[install the operator](install-operator.md#which-namespaces-the-operator-watches).

## Fresh install

```bash
helm install homeassistant-operator \
  oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --version "$VERSION" \
  --namespace homeassistant-operator-system --create-namespace \
  --set 'watchNamespaces={homeassistant}'
```

List the namespaces that will hold Home Assistant resources in
`watchNamespaces`. Leaving it empty falls back to a cluster-wide
`ClusterRoleBinding`, which is deprecated — see
[install the operator](install-operator.md#which-namespaces-the-operator-watches).

!!! note "Namespace ownership and Pod Security enforcement"
    `--create-namespace` lets Helm create the namespace **without** the chart's
    Pod Security Admission labels — the operator pod is restricted-compliant
    regardless, but the namespace is not labeled to *enforce* `restricted`. To
    have the chart own the namespace and apply the enforcing PSA labels, install
    **without** `--create-namespace` and set `--set namespace.create=true`
    instead.

On a fresh install all CRDs are created **before** the operator Deployment, so no
extra step is needed. Verify:

```bash
kubectl -n homeassistant-operator-system rollout status \
  deploy -l control-plane=controller-manager
kubectl get crds | grep homeassistant.io   # expect 10 CRDs
```

## Upgrade from the previous version

The operator supports and tests upgrades from the **directly previous released
version (N-1)** to the latest. Perform the steps **in this order** — CRDs first,
then the Helm release:

```bash
# 1. Update the CRDs explicitly (Helm does NOT do this on upgrade).
#    Pull the exact CRDs for the target version and apply them:
helm pull oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --version "$VERSION" --untar --untardir "/tmp/ha-operator-$VERSION"
kubectl apply -f "/tmp/ha-operator-$VERSION/homeassistant-operator/crds/"

# 2. Upgrade the Helm release.
helm upgrade homeassistant-operator \
  oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --version "$VERSION" \
  --namespace homeassistant-operator-system

# 3. Verify the upgrade.
kubectl -n homeassistant-operator-system rollout status \
  deploy -l control-plane=controller-manager
kubectl get crds | grep homeassistant.io
```

Your existing Custom Resources (`HomeAssistant`, `HomeAssistantConfiguration`,
automations, scenes, scripts, integrations, …) are preserved across the upgrade;
the CRD apply is additive and does not delete resources.

!!! warning "Do not skip intermediate versions"
    Only the **N-1 → latest** path is tested. If you are several versions behind
    and intermediate releases changed the CRD schema, upgrade through the
    intermediate versions one at a time rather than jumping straight to latest.

## RBAC on upgrade

`helm upgrade` reconciles the operator's RBAC (ClusterRole / Role / bindings) to
exactly what the target chart version declares. The project's CI enforces that a
chart upgrade never silently broadens the operator's permissions — any new
permission must be explicitly justified — so upgrading does not grant the
operator more access than the release notes describe.

## Verifying a successful upgrade

The upgrade succeeded when:

- `kubectl rollout status` reports the controller-manager Deployment is available;
- `kubectl get crds | grep homeassistant.io` lists all 10 CRDs;
- your existing Custom Resources are still present and reconciling
  (`kubectl get ha,haconfig,hasec,haauto,hasc,hascp,haint,haarea,hafloor,halabel -A`).
