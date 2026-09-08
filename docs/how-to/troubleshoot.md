# Troubleshoot a problem

*How-to — find and fix a specific symptom. Each section starts from something you observed.*

Look up the symptom you are seeing. For the meaning of a particular condition or
`reason`, see the [status conditions reference](../reference/conditions.md).


## Prerequisites

- `kubectl` access to the cluster, and the name of the resource that is misbehaving

## Operator does not reconcile resources in a namespace

If the operator ignores `HomeAssistant` (or other) CRs in a given namespace, it is likely running in **namespace-scoped** mode without that namespace in its watch list.

```sh
# Check which namespaces the operator watches
kubectl get deployment -n homeassistant-operator-system \
  -l control-plane=controller-manager \
  -o jsonpath='{.items[0].spec.template.spec.containers[0].env[?(@.name=="WATCH_NAMESPACES")].value}'
```

- Empty / not set → cluster-wide mode, watches all namespaces.
- Comma-separated list → only those namespaces are watched.

Fix: add the namespace to `watchNamespaces` in your Helm values and upgrade. Remember the operator's **own** namespace is not auto-included. See [Installation → Restrict watched namespaces](install-operator.md#which-namespaces-the-operator-watches).

## Operator gets banned by Home Assistant (`BanRecoveryFailed`)

When HA has `ip_ban_enabled: true`, repeated failed logins can ban the operator's IP (HTTP `403`). The operator self-recovers by restarting the HA pod with the `unban-operator-ip` init-container — but this is rate-limited to **3 restarts per 30 minutes**. Once exceeded, it stops and sets a condition:

```sh
kubectl describe ha home | grep -A3 BanRecoveryFailed
```

Manual recovery:

```sh
# 1. Remove the operator IP from ip_bans.yaml on the PVC, e.g. via a debug pod
#    mounting the <ha-name>-data PVC, or edit /config/ip_bans.yaml directly.
# 2. Restart the HA pod so the init-container runs and HA starts unbanned:
kubectl delete pod home-0
```

The window resets automatically after 30 minutes or on the first successful HA connection. See [Home Assistant CR → IP ban self-recovery](../explanation/reconciliation-model.md#ip-ban-self-recovery).


## HomeAssistant stuck in `WaitingForConfiguration`

A `HomeAssistant` CR requires a `HomeAssistantConfiguration` CR with a matching `spec.homeAssistantRef.name` in the same namespace (since v0.3.0). Without it the status stays `WaitingForConfiguration` and requeues every 5s.

```sh
# List HomeAssistantConfigurations and check spec.homeAssistantRef.name matches "home"
kubectl get homeassistantconfigurations -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.homeAssistantRef.name}{"\n"}{end}'
```

## Finalizer blocks resource deletion

If a CR hangs on deletion, the operator was likely removed before the CR (finalizer cleanup could not run). Restore the operator, or force-remove the finalizer as a last resort:

```sh
kubectl patch haauto lights-at-sunset -p '{"metadata":{"finalizers":null}}' --type=merge
```

## TLS mode enabled but no certificate is issued

Check the status conditions ([TLS guide](expose-with-tls.md)):

```sh
kubectl get homeassistant <name> -o jsonpath='{.status.conditions}' | jq
```

- **`CertManagerAvailable=False` (`CertManagerNotInstalled`)** — cert-manager is not
  installed. Home Assistant keeps serving over HTTP; install cert-manager and the
  operator picks it up automatically (no restart needed).
- **Edge certificate not issued** — the `Certificate` exists but has not been issued.
  Inspect it: `kubectl describe certificate <name>-ingress-tls` (or `-gateway-tls`)
  and check the referenced `Issuer`/`ClusterIssuer` is Ready.
- **`spec.alpha.tls` seems to be ignored** — native TLS was removed. Move to
  `spec.ingress.tls` or `spec.gateway` (see the [TLS guide](expose-with-tls.md)).

## HomeAssistantCommunityRepository fails with an extraction limit error

```sh
kubectl get homeassistantcommunityrepository <name> -o jsonpath='{.status.lastError}'
```

There is **no per-file size limit** — a single multi-megabyte source file (some
integrations ship map or icon assets as base64-encoded Python constants) installs
normally. Three archive-wide limits can still reject a repository:

- **`archive exceeds the cumulative extraction limit`** — the repository's contents
  add up to more than **100 MiB** once decompressed. The same limit applies again
  inside the Home Assistant pod when the files are written to the config volume, so a
  larger repository could not be installed anyway.
- **`archive holds more than ... bytes of small files`** — the repository consists of
  so many small files (below 1 MiB each) that reading them exceeds the **32 MiB** the
  operator keeps in memory while validating. Large files cost no memory here (only
  their presence is recorded), so this is reached by repositories with thousands of
  small files rather than by large ones.
- **`archive has too many entries`** — more than 20 000 entries in the tar archive.
  Every entry counts, not just files: directories, and symlinks and other special
  entries the operator otherwise skips, all count towards the limit. This is well
  beyond any HACS-compatible extension; check `spec.repository` points at the
  extension itself and not at a monorepo or a mirror.

If you hit one of these, pin `spec.ref` to a release tag rather than a branch: source
tarballs of release tags are usually far smaller than a `main` snapshot carrying full
documentation and media. Note that raising the operator's own memory limit does not
lift these limits — they are fixed, so that one oversized repository fails on its own
resource instead of exhausting the operator process shared by every other resource.
