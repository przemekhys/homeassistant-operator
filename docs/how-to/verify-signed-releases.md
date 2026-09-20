# Verify signed releases

*How-to — check that a release artifact really came from this project. Assumes `cosign` is installed.*

Every release from **v1.2.0** onwards is signed: the container image, the Helm
chart OCI artifact, and a `checksums.txt` bundle on the GitHub Release. Releases
that include `install.yaml` also record its SHA-256 checksum in that bundle.
Signing is keyless, so verification needs no key from the maintainer — only the
commands below.


## Prerequisites

- [`cosign`](https://docs.sigstore.dev/cosign/installation/) installed
- The version tag you want to verify

## What is signed

| Artifact | What it is |
|---|---|
| Container image | The multi-architecture (amd64/arm64) manifest list pushed to `ghcr.io/przemekhys/homeassistant-operator` |
| Helm chart | The chart packaged and pushed as an OCI artifact to `oci://ghcr.io/przemekhys/charts/homeassistant-operator` |
| `checksums.txt` | A signed text file listing the image digest, chart digest, and SHA-256 checksum for `install.yaml` |

All three are signed **keyless**, using [Sigstore](https://www.sigstore.dev/)/`cosign`,
bound to this repository's own GitHub Actions release workflow identity. There is
no long-lived signing key anywhere — the maintainer never generates, stores, or
rotates one. Each signature is backed by a short-lived certificate from Sigstore's
Fulcio and a public transparency-log entry in Rekor, which is what the verification
commands below check against.

## A note on version tags

Two different tags name the same release, and mixing them up is the usual reason
a verification command fails:

| What | Tag | Example |
|------|-----|---------|
| Git tag and GitHub Release | with `v` | `v1.4.0` |
| Container image and Helm chart in the registry | **without** `v` | `1.4.0` |

The release pipeline strips the `v` when it publishes to the registry, so
`ghcr.io/przemekhys/homeassistant-operator:v1.4.0` does not exist — only
`:1.4.0` does.

## Verify everything at once

The quickest route: `checksums.txt` lists the digests of both artifacts and is
itself signed, so verifying it once gives you trustworthy digests for the rest.

```bash
VERSION=v1.4.0
IDENTITY='https://github.com/przemekhys/homeassistant-operator/\.github/workflows/release\.yml@refs/tags/.*'
ISSUER=https://token.actions.githubusercontent.com

gh release download "$VERSION" \
  --repo przemekhys/homeassistant-operator \
  -p 'install.yaml' -p 'checksums.txt*'

cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  checksums.txt
```

Expected output: `Verified OK`.

`--repo` matters: without it `gh` looks at the git remote of the directory you
are standing in, so the command only works inside a clone. Verification should
not require one.

The verified file names the image and chart digests plus the installation-manifest checksum:

```bash
cat checksums.txt
```
```
sha256:2e0b7dd8...  container-image  ghcr.io/przemekhys/homeassistant-operator
sha256:147bee7a...  helm-chart       oci://ghcr.io/przemekhys/charts/homeassistant-operator
sha256:4e71a9c3...  kustomize-manifest  install.yaml
```

## Verify the container image

Take the digest from the file you just verified — no separate tooling needed:

```bash
IMAGE=ghcr.io/przemekhys/homeassistant-operator
DIGEST=$(awk '$2=="container-image" {print $1}' checksums.txt)

cosign verify \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  "$IMAGE@$DIGEST"
```

To resolve the digest from the tag yourself instead — which also checks that the
tag still points where you expect — use [`crane`](https://github.com/google/go-containerregistry):

```bash
DIGEST=$(crane digest "$IMAGE:${VERSION#v}")   # note: ${VERSION#v}, the registry tag has no "v"
```

## Verify the Helm chart

```bash
CHART=ghcr.io/przemekhys/charts/homeassistant-operator
CHART_DIGEST=$(awk '$2=="helm-chart" {print $1}' checksums.txt)

cosign verify \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  "$CHART@$CHART_DIGEST"
```

Note that the reference passed to `cosign` has no `oci://` prefix, even though
`helm` wants one when installing the same artifact.

This check needs no cluster and no Kyverno — anywhere `cosign` can reach the
registry will do, which makes it a natural preflight step in a GitOps pipeline
before `helm install`/`upgrade` ever runs.

## Verify the installation manifest

After verifying `checksums.txt`, validate the downloaded `install.yaml` against
its signed checksum before applying it:

```bash
awk '$2=="kustomize-manifest" && $3=="install.yaml" {print substr($1, 8) "  install.yaml"; found=1} END {exit !found}' checksums.txt \
  | sha256sum -c -
```

Expected output: `install.yaml: OK`. The manifest belongs to the GitHub Release,
so use its release URL rather than a raw source-file URL when installing or
removing the operator.

!!! tip "All of the above in one command"
    [`hack/verify-signatures.sh`](https://github.com/przemekhys/homeassistant-operator/blob/main/hack/verify-signatures.sh)
    runs the same checks for the image, chart, signed checksum bundle, and
    installation manifest: `hack/verify-signatures.sh v1.4.0`. It needs
    `cosign`, `gh` and `crane` on your PATH.

## Verify with Kyverno

!!! note "Not part of the supported API"
    [Kyverno](https://kyverno.io/) is a third-party admission controller that
    this project does not ship or control. The policy below reflects the state at
    the time of writing and may need adjusting for a different Kyverno version.

    **Tested with**: Kyverno 1.13.


Cluster operators can enforce this automatically at admission time with
[Kyverno](https://kyverno.io/), so an unsigned or tampered image is rejected before
it ever runs.

!!! note "Minimum Kyverno version"
    This sample was validated against the classic `kyverno.io/v1 ClusterPolicy` API,
    supported since **Kyverno 1.9**. It intentionally does not use the newer
    CEL-based `ImageValidatingPolicy` (Kyverno 1.14+) so it works on a broader range
    of clusters.

The full sample lives at
[`hack/kyverno/verify-homeassistant-operator-image.yaml`](https://github.com/przemekhys/homeassistant-operator/blob/main/hack/kyverno/verify-homeassistant-operator-image.yaml):

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: verify-homeassistant-operator-image
  annotations:
    policies.kyverno.io/title: Verify homeassistant-operator image signature
    policies.kyverno.io/category: Software Supply Chain
    policies.kyverno.io/severity: high
    policies.kyverno.io/description: >-
      Requires that any ghcr.io/przemekhys/homeassistant-operator image was signed
      by the project's own GitHub Actions release workflow (keyless Sigstore
      signing), rejecting unsigned or tampered images at admission time.
spec:
  validationFailureAction: Enforce
  webhookTimeoutSeconds: 30
  rules:
    - name: verify-signature
      match:
        any:
          - resources:
              kinds:
                - Pod
      verifyImages:
        - imageReferences:
            - "ghcr.io/przemekhys/homeassistant-operator*"
          attestors:
            - count: 1
              entries:
                - keyless:
                    issuer: "https://token.actions.githubusercontent.com"
                    subject: "https://github.com/przemekhys/homeassistant-operator/.github/workflows/release.yml@refs/tags/*"
                    rekor:
                      url: https://rekor.sigstore.dev
```

Apply it, then try both a genuine and a tampered image:

```bash
kubectl apply -f hack/kyverno/verify-homeassistant-operator-image.yaml

# Genuine image: admitted.
kubectl run ha-genuine --image=ghcr.io/przemekhys/homeassistant-operator:v1.2.0 --restart=Never

# Tampered/re-tagged image: rejected by the admission webhook.
kubectl run ha-tampered --image=<attacker-controlled-retag> --restart=Never
```

!!! warning "Roll out with Audit first"
    The sample defaults to `validationFailureAction: Enforce`. For a first adoption
    in an existing cluster, consider switching it to `Audit` (log-only) to confirm
    it matches as expected before enforcing rejections.

!!! warning "Keep this policy in sync if the signing identity changes"
    The `issuer`/`subject` values above are pinned to this repository's release
    workflow file path and tag-ref pattern. If that workflow is ever renamed or
    moved, both this doc page and the policy file must be updated together in the
    same change — otherwise the policy would silently stop matching new releases
    instead of failing loudly.
