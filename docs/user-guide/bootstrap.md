# Bootstrap

Bootstrap automates the full Home Assistant onboarding flow — no manual web UI interaction required. When enabled, the operator:

1. Waits for the HA pod to become healthy
2. Confirms the HA API is fully loaded (avoids the ambiguous 404 during cold start)
3. Checks whether onboarding has already been completed
4. Creates the admin user account
5. Configures location, timezone, and analytics
6. Generates a long-lived API token (10-year validity) and stores it in a Kubernetes Secret

The resulting token is used internally by the operator for hot-reload, Config Flow, and backup configuration.

## Quick setup

```sh
# 1. Create credentials secret
kubectl create secret generic ha-admin \
  --from-literal=username=admin \
  --from-literal=password=changeme
```

```yaml
apiVersion: ha.homeassistant.io/v1alpha1
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
    createAPIToken: true
```

Bootstrap typically completes in **2–5 minutes** on a fresh install.

## Spec reference

### `spec.bootstrap.enabled`

Set to `true` to activate bootstrap. Default: `false`.

### `spec.bootstrap.credentials.secretRef`

Reference to a Kubernetes Secret with admin credentials.

```yaml
bootstrap:
  credentials:
    secretRef:
      name: ha-admin
      usernameKey: username   # optional, default: "username"
      passwordKey: password   # optional, default: "password"
```

### `spec.bootstrap.createAPIToken`

When `true`, the operator creates a long-lived API token after onboarding and stores it in a Secret named `<ha-name>-homeassistant-api-token` (or the value of `apiTokenSecretName`).

```yaml
bootstrap:
  createAPIToken: true
  apiTokenSecretName: home-homeassistant-api-token   # optional, default: <ha-name>-homeassistant-api-token
```

### `spec.bootstrap.ownerName`

Display name for the admin user account. Default: `"Admin"`.

### `spec.bootstrap.language`

Language code for the HA UI. Default: `"en"`.

### `spec.bootstrap.location`

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

### `spec.bootstrap.analytics`

Send anonymous usage analytics to Nabu Casa. Default: `false`.

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

## Bootstrap state machine

```
pod not ready          → requeue 10 s
HA API not loaded      → requeue 5 s  (avoids ambiguous 404 during cold start)
onboarding pending     → perform onboarding → create token
onboarding complete    → ensure token Secret exists → done
```
