#!/usr/bin/env bash
# helm-e2e.sh — Helm install/upgrade e2e on k3d.
#
# Part 1 (fresh install): install the HEAD chart on a clean cluster and assert
#   the operator becomes Ready and all CRDs exist.
# Part 2 (upgrade):        install the previous release (N-1) from the OCI
#   registry, then follow the documented upgrade path — `kubectl apply -f crds/`
#   followed by `helm upgrade` to HEAD — and assert the operator is Ready and
#   pre-existing CRs survive.
#
# The upgrade steps mirror the published upgrade guide exactly. Requires: k3d,
# docker, helm, kubectl.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/lib-helm.sh
source "$ROOT/hack/lib-helm.sh"

CLUSTER="${HELM_E2E_CLUSTER:-helm-e2e}"
NS="${HELM_E2E_NAMESPACE:-homeassistant-operator-system}"
RELEASE="${HELM_E2E_RELEASE:-ha-operator}"
IMG="${IMG:-ghcr.io/przemekhys/homeassistant-operator:v0.9.0}"
IMG_REPO="${IMG%:*}"
IMG_TAG="${IMG##*:}"
K3D_MEMORY="${HELM_E2E_MEMORY:-4g}"
# renovate: datasource=docker depName=rancher/k3s
K3S_VERSION="${K3S_VERSION:-v1.36.2-k3s1}"
# renovate: datasource=github-releases depName=cert-manager/cert-manager
CERT_MANAGER_VERSION="v1.21.2"

for t in k3d docker helm kubectl; do hh_require "$t"; done
[ -n "$HELM_CHART_DIR" ] || { echo "❌ HELM_CHART_DIR is empty" >&2; exit 2; }
[ -n "${HELM_REGISTRY:-}" ] || { echo "❌ HELM_REGISTRY must be set (e.g. ghcr.io/<owner>)" >&2; exit 2; }

trap 'hh_k3d_cleanup "$CLUSTER"' EXIT

echo "==> Creating fresh k3d cluster '$CLUSTER'"
hh_k3d_create "$CLUSTER" "$K3D_MEMORY"

echo "==> Building and importing the HEAD operator image ($IMG)"
make docker-build IMG="$IMG" >/dev/null
k3d image import "$IMG" -c "$CLUSTER"

# ---- Part 1: fresh install of HEAD ---------------------------------------------
echo ""
echo "==> PART 1: fresh install of HEAD chart"
helm install "$RELEASE" "$HELM_CHART_DIR" \
  --namespace "$NS" --create-namespace \
  --set image.repository="$IMG_REPO" --set image.tag="$IMG_TAG" --set image.pullPolicy=IfNotPresent \
  --wait --timeout 180s
hh_wait_ready "$NS"
hh_assert_crds
# Part 1 already proves the default install (webhook off) needs no cert-manager
# on a clean cluster — the chart never installs or requires cert-manager.
echo "    ✅ fresh install OK (no cert-manager required)"

# ---- Part 1b: webhook TLS via cert-manager -------------------------------------
echo ""
echo "==> PART 1b: webhook TLS via cert-manager"
echo "    installing cert-manager ($CERT_MANAGER_VERSION)"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
# Wait for all three cert-manager components so its API is actually ready before
# the release upgrade requests a Certificate.
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  kubectl wait --for=condition=Available "deployment/$d" -n cert-manager --timeout=300s
done

echo "    upgrading release with webhook.enabled + webhook.certManager.enabled"
helm upgrade "$RELEASE" "$HELM_CHART_DIR" \
  --namespace "$NS" \
  --set image.repository="$IMG_REPO" --set image.tag="$IMG_TAG" --set image.pullPolicy=IfNotPresent \
  --set webhook.enabled=true --set webhook.certManager.enabled=true \
  --wait --timeout 180s
hh_wait_ready "$NS"

# Scope to the operator's own ValidatingWebhookConfiguration — cert-manager ships
# its own VWC, so a cluster-wide check could false-positive on that instead.
VWC_NAME="${RELEASE}-homeassistant-operator-validating-webhook-configuration"

if kubectl get validatingwebhookconfiguration "$VWC_NAME" >/dev/null 2>&1; then
  echo "    ✅ ValidatingWebhookConfiguration present"
else
  echo "❌ webhook configuration '$VWC_NAME' missing after enabling webhook" >&2
  exit 1
fi

# In cert-manager mode the CA is injected by cert-manager (inject-ca-from) — assert
# the wiring is present. The webhook's admission behaviour is covered by the Go E2E
# suite; asserting it here on a self-managed→cert-manager transition is timing-fragile.
if kubectl get validatingwebhookconfiguration "$VWC_NAME" -o yaml | grep -q "cert-manager.io/inject-ca-from"; then
  echo "    ✅ cert-manager CA injection annotation present"
else
  echo "❌ cert-manager inject-ca-from annotation missing" >&2
  exit 1
fi
echo "    ✅ PART 1b OK (cert-manager webhook wiring)"

echo "==> Tearing down fresh install to prepare the upgrade scenario"
helm uninstall "$RELEASE" --namespace "$NS" --wait || true

# ---- Part 2: upgrade from N-1 --------------------------------------------------
N1="$(hh_previous_version)"
if [ -z "$N1" ]; then
  echo "⏭️  PART 2 skipped: no previous release (N-1) available."
  echo "✅ helm-e2e (fresh install) passed."
  exit 0
fi

echo ""
echo "==> PART 2: upgrade from N-1 ($N1) to HEAD"
OCI="oci://${HELM_REGISTRY}/charts/homeassistant-operator"
echo "    installing N-1 chart from $OCI --version $N1"
helm install "$RELEASE" "$OCI" --version "$N1" \
  --namespace "$NS" --create-namespace --wait --timeout 180s
hh_wait_ready "$NS"

echo "    creating a sample CR to verify it survives the upgrade"
kubectl -n "$NS" apply -f - >/dev/null <<'EOF'
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantConfiguration
metadata:
  name: e2e-upgrade-probe
spec:
  homeAssistantRef:
    name: e2e-upgrade-probe
  configuration: |
    default_config:
EOF

echo "    [CRD update step] kubectl apply -f $HELM_CHART_DIR/crds/"
kubectl apply -f "$HELM_CHART_DIR/crds/"

echo "    [release upgrade step] helm upgrade to HEAD"
helm upgrade "$RELEASE" "$HELM_CHART_DIR" \
  --namespace "$NS" \
  --set image.repository="$IMG_REPO" --set image.tag="$IMG_TAG" --set image.pullPolicy=IfNotPresent \
  --wait --timeout 180s
hh_wait_ready "$NS"
hh_assert_crds

if kubectl -n "$NS" get homeassistantconfiguration e2e-upgrade-probe >/dev/null; then
  echo "    ✅ pre-existing CR survived the upgrade"
else
  echo "❌ pre-existing CR was lost during upgrade" >&2
  exit 1
fi

echo ""
echo "✅ helm-e2e passed: fresh install + upgrade N-1 ($N1) -> HEAD."
