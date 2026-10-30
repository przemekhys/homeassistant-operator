#!/usr/bin/env bash
# verify-rbac-upgrade.sh — hard security gate on RBAC growth across releases,
# enforcing least-privilege.
#
# Computes the set of (apiGroup/resource/verb) triples granted by the chart's
# ClusterRole + leader-election Role at HEAD and at the previous release (N-1),
# and FAILS if HEAD grants any triple that N-1 did not, unless that triple is
# explicitly justified in hack/rbac-allowlist.txt.
#
# N-1 is resolved via hack/lib-helm.sh (HELM_N1_VERSION override or latest stable
# GitHub Release). If no stable release exists, the gate passes.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/lib-helm.sh
source "$ROOT/hack/lib-helm.sh"

YQ="$(hh_yq)"
ALLOWLIST="$ROOT/hack/rbac-allowlist.txt"
TEMPLATES=("templates/clusterrole.yaml" "templates/role.yaml")

# triples_from_rules_yaml — read a YAML `rules:` sequence on stdin, emit sorted
# unique apiGroup/resource/verb triples.
triples_from_rules_yaml() {
  "$YQ" ea '.[] | .apiGroups[] as $g | .resources[] as $r | .verbs[] as $v | ($g + "/" + $r + "/" + $v)' - \
    | sort -u
}

# rules_yaml_from_template <file-content-on-stdin> — strip the Helm header, keep
# the pure-YAML rules sequence after the first `rules:` line (works for both the
# generated banner format and older 0-indent formats).
rules_yaml_from_template() {
  sed -n '/^rules:/,$p' | sed '1d' | grep -v '^#'
}

collect_triples() {
  # $1 = "HEAD" or a git ref; reads each template accordingly
  local ref="$1" tmpl content
  for tmpl in "${TEMPLATES[@]}"; do
    if [ "$ref" = "HEAD" ]; then
      [ -f "$HELM_CHART_DIR/$tmpl" ] || continue
      content="$(cat "$HELM_CHART_DIR/$tmpl")"
    else
      content="$(git show "${ref}:${HELM_CHART_DIR}/${tmpl}" 2>/dev/null || true)"
      [ -n "$content" ] || continue
    fi
    printf '%s\n' "$content" | rules_yaml_from_template | triples_from_rules_yaml
  done | sort -u
}

n1="$(hh_previous_version)"
if [ -z "$n1" ]; then
  echo "⏭️  No previous release (N-1) found — RBAC upgrade gate passes (baseline)."
  exit 0
fi
n1_tag="v${n1}"
if ! git rev-parse -q --verify "refs/tags/${n1_tag}" >/dev/null 2>&1; then
  echo "⏭️  Tag ${n1_tag} not found locally — cannot compare RBAC; gate passes."
  echo "    (ensure CI checks out with fetch-depth: 0 for a real comparison)"
  exit 0
fi

echo "==> Comparing RBAC: ${n1_tag} (N-1) vs HEAD"
head_set="$(collect_triples HEAD)"
n1_set="$(collect_triples "$n1_tag")"

# new = head - n1
new_triples="$(comm -23 <(printf '%s\n' "$head_set") <(printf '%s\n' "$n1_set") || true)"

if [ -z "$new_triples" ]; then
  echo "✅ HEAD does not expand RBAC vs ${n1_tag}."
  exit 0
fi

# Filter out allowlisted (justified) triples.
unjustified=""
while IFS= read -r t; do
  [ -n "$t" ] || continue
  if [ -f "$ALLOWLIST" ] && grep -qxF "$t" <(grep -vE '^\s*#|^\s*$' "$ALLOWLIST"); then
    echo "🔓 justified (allowlist): $t"
  else
    unjustified+="$t"$'\n'
  fi
done <<< "$new_triples"

if [ -n "${unjustified//[$'\n']/}" ]; then
  echo "" >&2
  echo "❌ Chart expands RBAC vs ${n1_tag} without justification:" >&2
  printf '%s' "$unjustified" | sed '/^$/d;s/^/   + /' >&2
  echo "" >&2
  echo "👉 Narrow the permission, or add each triple with a justification comment to" >&2
  echo "   hack/rbac-allowlist.txt (format: apiGroup/resource/verb — empty apiGroup = core)." >&2
  exit 1
fi

echo "✅ All new RBAC triples are justified in the allowlist."
