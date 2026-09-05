#!/usr/bin/env bash
# verify-signatures.sh — local reproduction of the release pipeline's signature
# checks (docs/how-to/verify-signed-releases.md).
#
# Verifies, for one published version tag: the container image signature, the
# Helm chart OCI artifact signature, and the signed checksums.txt.sigstore.json
# attached to the GitHub Release. It also validates the release installation
# manifest against the signed checksum, all pinned to the release workflow identity.
#
# Requires: cosign, gh, crane (or skopeo) to resolve the image manifest digest.
set -euo pipefail

REPO="${REPO:-przemekhys/homeassistant-operator}"
REGISTRY="${REGISTRY:-ghcr.io}"
IMAGE="${REGISTRY}/${REPO}"
CHART_REF="oci://${REGISTRY}/$(echo "${REPO%%/*}" | tr '[:upper:]' '[:lower:]')/charts/homeassistant-operator"
IDENTITY_REGEXP="https://github.com/${REPO}/\\.github/workflows/release\\.yml@refs/tags/.*"
ISSUER="https://token.actions.githubusercontent.com"

usage() {
  echo "Usage: $0 <version-tag>" >&2
  echo "Example: $0 v0.7.0" >&2
  exit 2
}

[ $# -eq 1 ] || usage
VERSION="$1"

for t in cosign gh crane; do
  command -v "$t" >/dev/null 2>&1 || { echo "❌ required tool not found: $t" >&2; exit 2; }
done

# Registry tags carry no leading "v": docker/metadata-action's {{version}}
# pattern strips it, so git tag v1.2.0 publishes image tag 1.2.0. The GitHub
# Release, in contrast, is still named v1.2.0.
IMAGE_TAG="${VERSION#v}"
echo "==> Verifying container image ${IMAGE}:${IMAGE_TAG}"
IMAGE_DIGEST="$(crane digest "${IMAGE}:${IMAGE_TAG}")"
cosign verify \
  --certificate-identity-regexp "$IDENTITY_REGEXP" \
  --certificate-oidc-issuer "$ISSUER" \
  "${IMAGE}@${IMAGE_DIGEST}"

echo "==> Verifying Helm chart ${CHART_REF}:${VERSION#v}"
CHART_DIGEST="$(crane digest "${CHART_REF#oci://}:${VERSION#v}")"
cosign verify \
  --certificate-identity-regexp "$IDENTITY_REGEXP" \
  --certificate-oidc-issuer "$ISSUER" \
  "${CHART_REF#oci://}@${CHART_DIGEST}"

echo "==> Verifying checksums.txt bundle and installation manifest from the ${VERSION} GitHub Release"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
gh release download "$VERSION" --repo "$REPO" -p 'install.yaml' -p 'checksums.txt*' --dir "$WORKDIR"
cosign verify-blob \
  --bundle "$WORKDIR/checksums.txt.sigstore.json" \
  --certificate-identity-regexp "$IDENTITY_REGEXP" \
  --certificate-oidc-issuer "$ISSUER" \
  "$WORKDIR/checksums.txt"
awk '$2 == "kustomize-manifest" && $3 == "install.yaml" { print substr($1, 8) "  install.yaml"; found = 1 } END { exit !found }' \
  "$WORKDIR/checksums.txt" | (cd "$WORKDIR" && sha256sum -c -)

echo "✅ all signatures verified for ${VERSION} (image, chart, checksums bundle, installation manifest)."
