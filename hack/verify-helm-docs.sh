#!/usr/bin/env bash
# verify-helm-docs.sh — fail if the committed chart README drifted from
# values.yaml.
#
# Regenerates the README into a temp copy and diffs it against the committed one,
# so values.yaml can never change without the docs table being updated.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/lib-helm.sh
source "$ROOT/hack/lib-helm.sh" # provides the HELM_CHART_DIR default

: "${HELM_DOCS:=$ROOT/bin/helm-docs}"
command -v "$HELM_DOCS" >/dev/null 2>&1 || [ -x "$HELM_DOCS" ] || {
  echo "❌ helm-docs not found (set HELM_DOCS or run 'make helm-docs-bin')" >&2; exit 2; }

readme="$HELM_CHART_DIR/README.md"
[ -f "$readme" ] || { echo "❌ missing $readme — run 'make helm-docs'" >&2; exit 1; }

backup="$(mktemp)"; docs_diff="$(mktemp)"
cp "$readme" "$backup"
trap 'mv -f "$backup" "$readme"; rm -f "$docs_diff"' EXIT # always restore the working-tree copy

"$HELM_DOCS" --chart-search-root="$HELM_CHART_DIR" --skip-version-footer >/dev/null

if ! diff -u "$backup" "$readme" >"$docs_diff" 2>&1; then
  echo "❌ Chart README is out of date with values.yaml:" >&2
  sed 's/^/   /' "$docs_diff" >&2
  echo "" >&2
  echo "👉 Run 'make helm-docs', review charts/homeassistant-operator/README.md, then rerun 'make docs-verify'." >&2
  exit 1
fi

echo "✅ Chart README is up to date with values.yaml"
