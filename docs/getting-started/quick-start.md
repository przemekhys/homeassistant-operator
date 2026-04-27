# Quick Start

Deploy a fully bootstrapped Home Assistant instance in under 5 minutes.

## 1. Create the admin credentials Secret

```sh
kubectl create secret generic ha-admin \
  --from-literal=username=admin \
  --from-literal=password=changeme \
  -n default
```

## 2. Create a HomeAssistantConfiguration

```yaml
apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantConfiguration
metadata:
  name: home
  namespace: default
spec:
  homeAssistantRef:
    name: home
  reloadStrategy: auto
  configuration: |
    homeassistant:
      name: Home
      latitude: 52.237703
      longitude: 20.989075
      unit_system: metric
      time_zone: Europe/Warsaw
```

## 3. Create the HomeAssistant instance

```yaml
apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistant
metadata:
  name: home
  namespace: default
spec:
  version: "2025.6"
  storage:
    size: 5Gi
  service:
    type: NodePort
  bootstrap:
    enabled: true
    credentials:
      secretRef:
        name: ha-admin
    createAPIToken: true
```

Apply both:

```sh
kubectl apply -f haconfig.yaml -f homeassistant.yaml
```

## 4. Watch bootstrap progress

```sh
kubectl get ha home -w
```

Bootstrap takes 2-3 minutes. When `READY` shows `True` the operator has:

1. Waited for HA to start
2. Created the admin user
3. Completed onboarding (location, timezone, analytics)
4. Generated a long-lived API token (stored in `home-homeassistant-api-token` Secret — pattern: `{homeassistant-name}-homeassistant-api-token`)

## 5. Access Home Assistant

```sh
# Get the NodePort
kubectl get svc home -o jsonpath='{.spec.ports[0].nodePort}'
```

Open `http://<node-ip>:<port>` in your browser and log in with the credentials from step 1.
