# Enforce Pod Security Standards

*How-to — run the operator under the `restricted` Pod Security Standard. Assumes a cluster with Pod Security Admission.*


## Prerequisites

- A cluster with Pod Security Admission enabled (Kubernetes 1.25+ has it by default)

## Enforcement with Helm

The Helm chart's operator pod is restricted-compliant out of the box (see
`securityContext`/`podSecurityContext` in `values.yaml`). Namespace-level enforcement
is **opt-in**, because Helm does not label the namespace it is installed into.

**GitOps / `helm template` (recommended for enforcement).** Set `namespace.create=true`
so the chart renders a `Namespace` carrying the six `restricted` PSA labels. This works
when the rendered manifests are applied directly — e.g. Argo CD, Flux, or a plain
`helm template ... | kubectl apply -f -`:

```bash
helm template ha-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --namespace homeassistant-operator-system \
  --set namespace.create=true | kubectl apply -f -
```

Nothing has to exist beforehand here: `kubectl apply` creates the labelled
`Namespace` from the rendered manifests like any other object.

**Direct `helm install`.** Helm stores release state in the target namespace
*before* it applies the chart, so the namespace has to exist first — otherwise the
install fails with `namespaces "..." not found`. Pass `--create-namespace`
alongside `namespace.create=true`; the two do not collide, and the chart's
`Namespace` object applies the enforcing labels on top:

```bash
helm install homeassistant-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --namespace homeassistant-operator-system --create-namespace \
  --set namespace.create=true \
  --set 'watchNamespaces={homeassistant}'
```

**Namespace you manage yourself.** Leave `namespace.create` at its default and
label the namespace before installing:

```bash
kubectl create namespace homeassistant-operator-system
kubectl label namespace homeassistant-operator-system \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/audit-version=latest \
  pod-security.kubernetes.io/warn=restricted \
  pod-security.kubernetes.io/warn-version=latest
helm install homeassistant-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --namespace homeassistant-operator-system \
  --set 'watchNamespaces={homeassistant}'
```

The operator pod stays restricted-compliant in every case; only the namespace-level
enforcement differs.
