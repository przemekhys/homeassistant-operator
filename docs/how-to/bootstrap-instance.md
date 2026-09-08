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
    createApiToken: true
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
# Retrieve the API token
kubectl get secret home-homeassistant-api-token -o jsonpath='{.data.token}' | base64 -d
```

## API token Secret

The token Secret is named `<ha-name>-homeassistant-api-token` by default:

```sh
kubectl get secret home-homeassistant-api-token -o yaml
```
```yaml
data:
  token: <base64-encoded long-lived token>
```

Other CRDs (`HomeAssistantAutomation`, `HomeAssistantIntegration`, etc.) automatically use this Secret — no manual wiring required.

## Re-running bootstrap

Bootstrap is idempotent. If HA is already onboarded, the operator detects it and skips onboarding steps. If the API token Secret is missing, the operator re-creates it.

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

When `true`, the operator creates a long-lived API token after onboarding and stores it in a Secret named `<ha-name>-homeassistant-api-token` (or the value of `apiTokenSecretName`).

```yaml
bootstrap:
  createApiToken: true
  apiTokenSecretName: home-homeassistant-api-token   # optional, default: <ha-name>-homeassistant-api-token
```

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
