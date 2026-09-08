# homeassistant-operator

![Version: 1.1.0](https://img.shields.io/badge/Version-1.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 1.1.0](https://img.shields.io/badge/AppVersion-1.1.0-informational?style=flat-square)

A Kubernetes operator for deploying and managing Home Assistant instances with declarative configuration, zero-touch bootstrap, and GitOps-ready automations.

**Homepage:** <https://przemekhys.github.io/homeassistant-operator/>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| przemekhys |  | <https://github.com/przemekhys> |

## Source Code

* <https://github.com/przemekhys/homeassistant-operator>

## Requirements

Kubernetes: `>=1.24-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for the operator pod, e.g. pod anti-affinity to spread replicas across nodes. |
| fullnameOverride | string | `""` | Override the full generated name of every rendered resource. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy for the operator container. |
| image.repository | string | `"ghcr.io/przemekhys/homeassistant-operator"` | Operator container image repository. |
| image.tag | string | `"1.1.0"` | Image tag. Defaults to the chart's appVersion when empty. |
| imagePullSecrets | list | `[]` | Image pull secrets for a private registry mirror. |
| metricsNetworkPolicy.enabled | bool | `false` | Restrict ingress to the operator pod: its `/metrics` endpoint to namespaces labeled `metrics: enabled`, and — when `webhook.enabled` is also true — its admission webhook to namespaces labeled `webhook: enabled`. That label governs pod-to-pod callers only: on most clusters the API server's AdmissionReview calls do not originate from the pod network, so the rule neither reaches nor blocks them. Where they are evaluated by the policy, an unlabelled caller is denied — with `webhook.failurePolicy: Ignore` (the default) writes are then admitted unvalidated, and with `Fail` they are rejected outright. Egress is deliberately not restricted, because there is no portable way to address the Kubernetes API server across cluster types and CNIs from a generic chart. Unrelated to `spec.alpha.networkPolicy.enabled` on a HomeAssistant resource, which protects the Home Assistant pod instead. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| namespace.create | bool | `false` | Render the release Namespace and label it to enforce the "restricted" Pod Security Standard (version latest). Helm stores its release state in the target namespace, so that namespace must exist before the chart is applied: pass `--create-namespace` (or create it yourself) on the first install. When false (the default), no Pod Security Admission labels are added at all — the operator pod is restricted-compliant either way, see `securityContext`, but the namespace does not enforce it. |
| nodeSelector | object | `{}` | Node selector for the operator pod. Example — pin to ARM64 nodes: `{"kubernetes.io/arch": "arm64"}` |
| podAnnotations | object | `{}` | Extra annotations for the operator pod. |
| podSecurityContext.runAsNonRoot | bool | `true` | Refuse to start the pod if the image would run as root. |
| podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` | Seccomp profile for the pod. `RuntimeDefault` is required by the "restricted" Pod Security Standard. |
| priorityClassName | string | `""` | PriorityClass for the operator pod, so it is evicted after your workloads rather than before them. |
| replicaCount | int | `1` | Number of operator replicas. The operator has no leader election across replicas, so values above 1 are not supported. |
| resources.limits.cpu | string | `"500m"` | CPU limit for the operator container. |
| resources.limits.memory | string | `"128Mi"` | Memory limit. Raise it if you validate very large community repositories. |
| resources.requests.cpu | string | `"10m"` | CPU request for the operator container. |
| resources.requests.memory | string | `"64Mi"` | Memory request for the operator container. |
| securityContext.allowPrivilegeEscalation | bool | `false` | Forbid gaining more privileges than the parent process. |
| securityContext.capabilities.drop | list | `["ALL"]` | Linux capabilities to drop. All of them, as "restricted" requires. |
| securityContext.readOnlyRootFilesystem | bool | `true` | Mount the container root filesystem read-only. |
| securityContext.runAsGroup | int | `65532` | GID the operator process runs as. |
| securityContext.runAsNonRoot | bool | `true` | Refuse to start the container if it would run as root. |
| securityContext.runAsUser | int | `65532` | UID the operator process runs as. |
| serviceAccount.annotations | object | `{}` | Extra annotations for the ServiceAccount, e.g. for cloud workload identity. |
| serviceAccount.create | bool | `true` | Create the operator's ServiceAccount. Set to false to supply your own. |
| serviceAccount.name | string | `""` | Name of the ServiceAccount. Defaults to the chart fullname when empty. |
| tolerations | list | `[]` | Tolerations for the operator pod. |
| topologySpreadConstraints | list | `[]` | Topology spread constraints for the operator pod, e.g. spreading across zones with `topologyKey: topology.kubernetes.io/zone`. |
| watchNamespaces | list | `[]` | Namespaces the operator watches for Home Assistant resources. Empty means ALL namespaces, which requires a cluster-wide ClusterRoleBinding and is deprecated. Listing namespaces creates per-namespace RoleBindings instead — least privilege. The operator's own namespace is NOT included automatically; add it explicitly if you run Home Assistant resources alongside the operator. |
| webhook.certManager.enabled | bool | `false` | Have cert-manager issue the webhook's serving certificate instead of the operator self-managing it. Requires cert-manager to be installed. When false (the default) the operator generates and rotates a self-signed certificate and injects the CA itself, so cert-manager is not a dependency. |
| webhook.enabled | bool | `true` | Run the validating admission webhook, which checks resource coherence at admission time. Enabled by default; setting this to false runs the operator with `ENABLE_WEBHOOKS=false` and renders no webhook resources. |
| webhook.failurePolicy | string | `"Ignore"` | failurePolicy for the ValidatingWebhookConfiguration. `Ignore` (the default) keeps validation best-effort, so resource creation is never blocked while the webhook is unavailable — during an operator rollout, for instance. Use `Fail` to reject invalid resources strictly. |
| webhook.port | int | `9443` | Port the webhook server listens on inside the operator pod. |
