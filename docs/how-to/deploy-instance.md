# Deploy a Home Assistant instance

*How-to — create and change a Home Assistant instance. Assumes the operator is installed.*

## Prerequisites

A `HomeAssistantConfiguration` CR with a matching `spec.homeAssistantRef.name` must exist before or alongside the `HomeAssistant` CR. Without it, the operator sets status to `WaitingForConfiguration` and requeues every 5 seconds.

## Minimal example

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: home
spec:
  homeAssistantRef:
    name: home
  configuration: |
    default_config:
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: home
spec:
  version: "2025.6"
  storage:
    size: 5Gi
  service:
    type: ClusterIP
```

## Keep the data when the resource is deleted

By default the operator sets a controller owner reference on the instance's PVC,
so deleting the `HomeAssistant` resource takes the PVC — and with it your Home
Assistant configuration, database and history — along with it. That is convenient
for throwaway instances and a disaster for a real one, especially when the
deletion is a GitOps reconcile you did not personally type.

To keep the volume:

```yaml
spec:
  storage:
    retainPVC: true
```

With this set, the operator removes its owner reference from the PVC, so nothing
garbage-collects it. Flipping the flag on an existing instance works in both
directions — the operator adds or removes the reference on the next reconcile,
without recreating the volume.

## Deleting an instance

```sh
kubectl delete ha home
```

What happens to the data depends on two independent settings, and both have to
point the same way for the data to actually survive:

| | `retainPVC: false` (default) | `retainPVC: true` |
|---|---|---|
| **PVC object** | Deleted with the resource | Stays |
| **Data** | Follows the StorageClass reclaim policy | Stays, until you delete the PVC yourself |

Once a PVC is deleted, whether the underlying volume and its contents are
destroyed is decided by the StorageClass, not by this operator:

```sh
kubectl get storageclass -o custom-columns=NAME:.metadata.name,RECLAIM:.reclaimPolicy
```

- `reclaimPolicy: Delete` — the usual default, including k3s's `local-path`. The
  volume and everything on it is removed as soon as the PVC goes.
- `reclaimPolicy: Retain` — the volume survives as a `Released` PersistentVolume.
  Your data is still there, but the volume will not be reused automatically; you
  reclaim it by hand.

So `retainPVC: true` on a `Delete` StorageClass still protects you, because the
PVC never goes away. And `retainPVC: false` on a `Retain` StorageClass leaves you
with an orphaned PV holding your data — recoverable, but not something you want
to discover during an incident.

For a deliberate clean teardown of a retained volume:

```sh
kubectl delete ha home
kubectl delete pvc home-data   # name: <ha-name>-data
```

## Verify

```sh
kubectl get ha home
```
```
NAME   READY   VERSION   AGE
home   True    stable    5m
```

The `VERSION` column echoes `spec.version`, so it shows `stable` if that is what
you asked for rather than the calendar version behind it.

```sh
kubectl describe ha home
```
```
Status:
  Phase:  Running
  Ready:  true
  Conditions:
    Type:    Ready
    Status:  True
    Reason:  StatefulSetReady
    Type:    BootstrapReady
    Status:  True
    Reason:  BootstrapCompleted
```

Every condition and reason is listed in the
[status conditions reference](../reference/conditions.md).

## Pin the Home Assistant version

Home Assistant image tag. Accepts any tag published to `ghcr.io/home-assistant/home-assistant`.

```yaml
spec:
  version: "2025.6"     # specific version (recommended)
  # version: "stable"   # latest stable
```

## Set the timezone

Timezone passed to the container. Defaults to `UTC`.

```yaml
spec:
  timezone: "Europe/Warsaw"
```

## Choose storage

Configures the PersistentVolumeClaim for `/config`.

```yaml
spec:
  storage:
    size: 10Gi
    storageClassName: local-path   # optional; uses cluster default if omitted
    accessMode: ReadWriteOnce      # optional; default ReadWriteOnce
```

## Choose how the Service is exposed

Controls the Kubernetes Service created for the HA pod.

```yaml
spec:
  service:
    type: ClusterIP      # ClusterIP | NodePort | LoadBalancer
    port: 8123
```

## Expose over an Ingress

Optional Ingress resource.

```yaml
spec:
  ingress:
    enabled: true
    host: ha.example.com
    ingressClassName: nginx
    tls:
      secretName: ha-tls
```

For a certificate on that Ingress, and for the Gateway API alternative, see
[expose an instance with TLS](expose-with-tls.md).

## Set resource requests and limits

CPU and memory requests/limits for the HA container.

```yaml
spec:
  resources:
    requests:
      cpu: "100m"
      memory: "256Mi"
    limits:
      cpu: "1000m"
      memory: "1Gi"
```

## Use host networking for LAN device discovery

Enables host networking for mDNS/SSDP/DHCP device discovery on the local LAN. Off by default.

```yaml
spec:
  hostNetwork: true
```

!!! warning
    `hostNetwork: true` binds HA directly to the node's network interface. Use only on single-node clusters or when LAN device discovery is required.

    It also weakens `spec.alpha.networkPolicy.enabled` (see below): NetworkPolicy operates on pod IPs, so it does not restrict traffic arriving via the host's network interface. Combining both gives only partial isolation.

## Control where the pod runs

Controls where the Home Assistant pod is eligible to run and how it's treated under resource contention, using Kubernetes' own scheduling primitives directly — every field is copied verbatim onto the generated pod template. All fields are optional; leaving `spec.scheduling` unset preserves today's freely-schedulable, default-priority behavior exactly.

```yaml
spec:
  scheduling:
    nodeSelector:
      ha-device-node: zigbee
    affinity:
      nodeAffinity:
        preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 1
            preference:
              matchExpressions:
                - key: ha-storage
                  operator: In
                  values: ["nvme"]
    tolerations:
      - key: ha-dedicated
        operator: Equal
        value: "true"
        effect: NoSchedule
    priorityClassName: ha-critical
```

Fields:

- `nodeSelector` (optional): restricts the pod to nodes matching all of these labels — the simplest way to pin the pod to a specific node (see `spec.alpha.devices` below).
- `affinity` (optional): node affinity/anti-affinity and pod affinity/anti-affinity rules, using Kubernetes' own `Affinity` semantics unchanged (`nodeAffinity`, `podAffinity`, `podAntiAffinity`).
- `tolerations` (optional): allows the pod onto nodes with matching taints that would otherwise repel it (e.g. a node pool dedicated to hardware-attached workloads).
- `priorityClassName` (optional): assigns a `PriorityClass` to the pod, influencing preemption/eviction order under resource contention. Must name an existing `PriorityClass` — the operator rejects the resource at admission time if it doesn't exist.

The `HomeAssistant` resource's `SchedulingReady` status condition reports whether the pod's declared constraints are currently satisfiable (mirroring the pod's own `PodScheduled` condition), so an impossible-to-satisfy `nodeSelector`/`affinity` combination is diagnosable straight from `kubectl describe homeassistant` instead of a generic "not ready".

!!! note
    Kubernetes only evaluates scheduling constraints when a pod is placed — editing `spec.scheduling` on an already-running instance triggers a pod recreation so the new constraint actually takes effect; it does not live-migrate the running pod.

## Restrict network access to the pod (experimental)

!!! note "Alpha"
    Opt-in, off by default. Fields under `spec.alpha` are experimental and may
    change or be removed without a deprecation notice — see
    [what `spec.alpha` means](../explanation/alpha-lifecycle.md).

    If you turn this on, please
    [say how it went](https://github.com/przemekhys/homeassistant-operator/discussions/new/choose)
    — whether it worked is the evidence that decides whether it stays.

When enabled, the operator creates a `NetworkPolicy` restricting ingress to the Home Assistant pod to the same namespace and the operator's own namespace, on the Service port. Egress is left unrestricted — Home Assistant needs broad, unpredictable egress to IoT devices, cloud APIs, and MQTT brokers.

```yaml
spec:
  alpha:
    networkPolicy:
      enabled: true
```

!!! warning
    The operator-namespace ingress peer is only added when the controller knows its own namespace via the `OPERATOR_NAMESPACE` environment variable (set automatically by the shipped manifests). If it is unset, the operator silently omits that peer and only logs a warning — the resulting policy blocks the operator from reaching the HA API, breaking bootstrap, hot-reload, and health checks. Ensure `OPERATOR_NAMESPACE` is set on the controller before enabling this.

## Pass a USB device through to the container (experimental)

!!! note "Alpha"
    Opt-in, off by default. Fields under `spec.alpha` are experimental and may
    change or be removed without a deprecation notice — see
    [what `spec.alpha` means](../explanation/alpha-lifecycle.md).

    If you turn this on, please
    [say how it went](https://github.com/przemekhys/homeassistant-operator/discussions/new/choose)
    — whether it worked is the evidence that decides whether it stays.

Mounts one or more host device nodes (e.g. `/dev/ttyACM0` for a Zigbee/Z-Wave USB coordinator such as a Conbee2 or SkyConnect) into the Home Assistant container, so integrations like Zigbee2MQTT, Z-Wave JS, or ZHA can open the serial port. Each entry is mounted as a `hostPath` volume typed as a character device — the operator never sets `privileged: true` on the pod for this.

```yaml
spec:
  alpha:
    devices:
      - hostPath: /dev/ttyACM0
        # containerPath defaults to hostPath when omitted
```

Fields per entry:

- `hostPath` (required): the device node's path on the host. Must be an absolute path under `/dev`.
- `containerPath` (optional): the path the device is mounted at inside the container. Defaults to `hostPath`.

!!! warning
    This does **not** by itself pin the pod to the node the device is physically attached to. A USB coordinator only exists on one specific node, so declaring it here is only useful once you've separately pinned the pod there via [`spec.scheduling.nodeSelector`](#control-where-the-pod-runs) (label the node, then match that label). If the declared device isn't present on whichever node the pod lands on, the pod fails to start and the `HomeAssistant` resource's `DevicesReady` status condition names the missing path.

## Use a ready-made secrets Secret

Direct reference to a Kubernetes Secret containing a `secrets.yaml` blob. Prefer [`HomeAssistantSecrets`](manage-secrets.md) for managed secret composition.

```yaml
spec:
  secretsFrom:
    name: my-raw-secrets
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistant` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantspec).
