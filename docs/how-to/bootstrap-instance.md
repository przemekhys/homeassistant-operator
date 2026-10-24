# Bootstrap an instance without touching the UI

*How-to — have the operator create the admin account and an API token for you. Assumes the operator is installed.*

Bootstrap creates the first admin user, finishes the parts of Home Assistant's
onboarding that concern the deployment, and stores a long-lived access token in a
Secret. The operator needs that token for everything else it does, so almost
every other guide depends on this one.


## Prerequisites

- A `HomeAssistant` resource, or one you are about to create
- A Kubernetes Secret holding the admin username and password

## Quick setup

```sh
# 1. Create the credentials Secret. This is the admin account for the whole
#    instance, so choose the password deliberately — you will log in with it.
read -rsp 'Admin password: ' HA_PASSWORD; echo

kubectl create secret generic ha-admin \
  --from-literal=username=admin \
  --from-literal=password="$HA_PASSWORD"
```

If you later need to check what was set, read it back from the Secret:

```sh
kubectl get secret ha-admin -o jsonpath='{.data.password}' | base64 -d; echo
```

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: home
spec:
  version: "2025.6"
  storage:
    size: 10Gi
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-admin
    operatorAccount:
      mode: dedicated
      tokenSecretName: home-operator-token
```

Bootstrap typically completes in **2–5 minutes** on a fresh install.

## Checking bootstrap status

```sh
kubectl get ha home
```
```
NAME   READY   STATUS    VERSION   AGE
home   True    Running   2025.6    8m
```

```sh
kubectl get ha home -o jsonpath='{.status.bootstrap}'
```
```
{"completed":true,"apiTokenReady":true}
```

```sh
# Inspect the managed Secret ownership without printing its token
kubectl get secret home-operator-token -o jsonpath='{.metadata.ownerReferences}'
```

## API token Secret

Dedicated mode creates a separate Home Assistant administrator identity for the
operator and stores its token under `data.token` in `tokenSecretName`, in the
same namespace as the `HomeAssistant`. The Secret has a controller owner
reference and is garbage-collected with that resource. Its only credential
payload is the token; the temporary operator password is never persisted.

Inspect metadata without printing the credential:

```sh
kubectl get secret home-operator-token -o jsonpath='{.metadata.labels}'
```
```yaml
data:
  token: <base64-encoded long-lived token>
```

Other CRDs (`HomeAssistantAutomation`, `HomeAssistantIntegration`, etc.) automatically use this Secret — no manual wiring required.

## Re-running bootstrap

Bootstrap is idempotent. In dedicated mode, a verified account retires bootstrap
access from normal reconciliation. Missing or invalid operator tokens set
`OperatorCredentialReady=False` and `Degraded=True`; they never cause automatic
password resets, account replacement, or token creation. Follow
[explicit credential recovery](recover-operator-credential.md).

Retain the bootstrap owner Secret only for deliberate recovery. You may manually
delete it after onboarding; normal management continues, but recovery cannot
work until valid owner credentials are supplied again. Changing that Secret
never synchronizes the human administrator password with Home Assistant.

!!! note
    Changing `spec.bootstrap.credentials.secretRef` after initial bootstrap has no effect on the running HA instance — HA stores credentials internally. To change the admin password, use the HA UI.

## Point at the credentials Secret

Reference to a Kubernetes Secret with admin credentials.

```yaml
bootstrap:
  credentials:
    secretRef:
      name: ha-admin
      usernameKey: username   # optional, default: "username"
      passwordKey: password   # optional, default: "password"
```

## Have an API token created

When `operatorAccount` is omitted or its mode is `legacy`, `createApiToken`
preserves the administrator-derived token behavior and conventional Secret
name. Dedicated mode always issues its own token, independently of this legacy
flag, and requires a distinct `tokenSecretName`.

Legacy mode is deprecated since **v1.7.0** and scheduled for removal in
**v2.0.0**. This includes omitting `operatorAccount`, which selects legacy
behavior in v1.x. Use dedicated mode for new installations and migrate existing
ones before upgrading to v2.0.0.

```yaml
bootstrap:
  createApiToken: true
  apiTokenSecretName: home-homeassistant-api-token   # optional, default: <ha-name>-homeassistant-api-token
```

## Migrate an existing installation

!!! note "Available from v1.7.0"
    Versions before v1.7.0 use the administrator-derived token model, now called
    `legacy` mode. Dedicated operator accounts and this migration procedure are
    available from v1.7.0. Existing installations retain legacy behavior after
    upgrading until an administrator explicitly selects `dedicated` mode.

!!! warning "Legacy mode removal in v2.0.0"
    Legacy mode is deprecated since v1.7.0 and scheduled for removal in v2.0.0.
    Complete this migration before upgrading to v2.0.0. Compatibility and
    rollback to legacy mode remain available during v1.x while the old token
    is valid.

First upgrade the CRDs and operator. Keep the current token Secret and valid
bootstrap owner credentials. Set an unused dedicated Secret name:

```sh
kubectl patch ha home --type=merge -p '{"spec":{"bootstrap":{"operatorAccount":{"mode":"dedicated","tokenSecretName":"home-operator-token"}}}}'
kubectl wait ha home --for=condition=OperatorCredentialReady --timeout=5m
kubectl get ha home -o jsonpath='{.status.bootstrap.operatorAccount}'
```

Runtime consumers retain the legacy token until the dedicated replacement is
verified. Status then reports `migrationPhase: Completed` and
`legacySecretName`. The old Secret is retained; manually revoke its token in
Home Assistant and remove the Secret only after checking dedicated management.

During v1.x, to abandon migration while the legacy credential is still valid, set mode back
to `legacy` and set `apiTokenSecretName` to the retained legacy Secret. Do not
delete the dedicated account or token until legacy access is restored. Changing
mode never deletes either credential automatically. A name collision or failed
provisioning reports an actionable credential condition; correct the name or
use an explicit recovery request as appropriate.

## Set the home location during onboarding

Configures home location during onboarding.

```yaml
bootstrap:
  location:
    name: "Home"
    latitude: "52.237703"
    longitude: "20.989075"
    elevation: 100
    unitSystem: "metric"      # metric | us_customary
    currency: "PLN"
    timeZone: "Europe/Warsaw"
```

## Every field

This guide shows the fields you need for the task. For the complete list of
`BootstrapSpec` fields, with types and defaults, see the
[API reference](../reference/api.md#bootstrapspec).
