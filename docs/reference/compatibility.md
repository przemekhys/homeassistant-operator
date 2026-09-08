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
| Go | **1.26** or newer (`go.mod`) |
| Container runtime | Docker or Podman |

See [contributing](../development/contributing.md).
