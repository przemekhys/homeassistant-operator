# External secret management

*Ecosystem guide — how external secret tooling works together with the operator. Not part of the operator's supported API.*

Source the Kubernetes Secrets that Home Assistant needs from a secret manager or
a Git-safe encrypted format, instead of creating them by hand with
`kubectl create secret`.

!!! note "Not part of the supported API"
    This guide shows one way to integrate the operator with another tool in the
    Kubernetes ecosystem. Unlike the rest of this documentation, it is **not**
    part of the operator's supported API — it reflects the state at the time of
    writing and may need adjusting for a different tool version or cluster setup.

**Tested with**: External Secrets Operator 0.12, Sealed Secrets 0.27,
Vault Secrets Operator 0.9, operator 1.x.

## What this gives you

`HomeAssistantSecrets` only ever reads a plain Kubernetes
[`Secret`](../reference/api.md#homeassistantsecretsspec) — it does not care how
that Secret came to exist. That is what makes all of these work without the
operator knowing anything about them.

## Security considerations

Whatever you put in front of it, the values end up in a Kubernetes Secret and
then, composed into `secrets.yaml`, on the instance's volume. These tools change
where the secret is *stored and rotated*, not how exposed it is once Home
Assistant is using it — see
[the security model](../explanation/security-model.md).

## External Secrets Operator

[External Secrets Operator](https://external-secrets.io/) (ESO) is the recommended layer if you use a cloud secret manager (AWS Secrets Manager, GCP Secret Manager, Azure Key Vault, HashiCorp Vault, and others) — one `SecretStore`/`ClusterSecretStore` abstracts the backend, and `ExternalSecret` resources pull values into ordinary Kubernetes Secrets.

```yaml
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: my-backend
spec:
  provider:
    aws: # swap for gcpsm / azurekv / vault / etc. — see the ESO provider docs
      service: SecretsManager
      region: eu-west-1
      auth:
        jwt:
          serviceAccountRef:
            name: external-secrets
            namespace: external-secrets
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: ha-integrations
  namespace: homeassistant
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: my-backend
  target:
    name: ha-integrations
    creationPolicy: Owner
  data:
    - secretKey: mqtt_password
      remoteRef:
        key: homeassistant/mqtt
        property: password
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantSecrets
metadata:
  name: home-secrets
  namespace: homeassistant
spec:
  homeAssistantRef:
    name: home
  secretRefs:
    - name: ha-integrations
```

**One remote entry, multiple keys.** If your secret manager stores several related values as a single JSON blob (e.g. one entry holding both a database password and an API key), `ExternalSecret` can flatten it into separate Secret keys with a template instead of one `ExternalSecret` per value:

```yaml
spec:
  target:
    name: ha-integrations
    creationPolicy: Owner
    template:
      data:
        postgres_password: '{{ .blob | fromJson | dig "postgres_password" "" }}'
        openweathermap_api_key: '{{ .blob | fromJson | dig "openweathermap_api_key" "" }}'
  data:
    - secretKey: blob
      remoteRef:
        key: homeassistant/integrations
```

`HomeAssistantSecrets` then just references the resulting `ha-integrations` Secret by name — it has no idea ESO (or a JSON template) was involved.

## Sealed Secrets

[Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets) takes the opposite approach: instead of pulling from an external store at runtime, you encrypt a Secret client-side (`kubeseal`) against the cluster's public key, and commit the resulting ciphertext to Git. The in-cluster controller decrypts it into a normal Secret on apply.

```yaml
apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: mqtt-credentials
  namespace: homeassistant
spec:
  encryptedData:
    mqtt_user: AgBy8h... # output of `kubeseal --raw ...`
    mqtt_password: AgCd3k...
  template:
    metadata:
      name: mqtt-credentials
      namespace: homeassistant
---
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantSecrets
metadata:
  name: home-secrets
spec:
  homeAssistantRef:
    name: home
  secretRefs:
    - name: mqtt-credentials
```

The `SealedSecret` controller creates a plain `Secret` named `mqtt-credentials` — `HomeAssistantSecrets` reads it exactly like any other Secret, no changes needed. This is a good fit if you want secrets committed to Git (GitOps-native) without depending on an external secret manager.

## Vault

HashiCorp Vault is supported through two different mechanisms — pick one, they are **not** interchangeable:

- **Vault Secrets Operator** (VSO) — a `VaultStaticSecret` (or dynamic equivalent) CR creates and keeps a Kubernetes `Secret` in sync with Vault. Works with `HomeAssistantSecrets` the same way as the ESO example above.
- **ESO with the Vault provider** — same `ExternalSecret` pattern as above, with `spec.provider.vault` instead of a cloud provider.

!!! warning "Vault Agent Sidecar is not compatible"
    The classic **Vault Agent Sidecar** injects secrets as **files** into the pod filesystem, not as a Kubernetes `Secret` object. `HomeAssistantSecrets` only reads Kubernetes Secrets via `spec.secretRefs` — it has no way to consume file-injected secrets. Use VSO or ESO+Vault instead.

## See also

- [Manage secrets](../how-to/manage-secrets.md) for the task itself, and the
  [API reference](../reference/api.md#homeassistantsecretsspec) for every field of
  `HomeAssistantSecrets`.
- [CRD API Reference](../reference/api.md) for every field on every CRD.
