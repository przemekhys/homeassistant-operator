#!/usr/bin/env bash
# Regression checks for the published-release upgrade baseline; no network needed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/lib-helm.sh
source "$ROOT/hack/lib-helm.sh"
unset HELM_N1_VERSION
export GITHUB_REPOSITORY="example/operator"

gh() {
  if [ "$*" != "release list --repo example/operator --exclude-drafts --exclude-pre-releases --limit 1 --json tagName --jq .[0].tagName // empty" ]; then
    echo "Unexpected release discovery arguments: $*" >&2
    return 2
  fi
  printf '%s\n' "${TEST_RELEASE:-}"
  return "${TEST_STATUS:-0}"
}

assert_version() {
  local actual
  actual="$(hh_previous_version)"
  [ "$actual" = "$1" ] || {
    echo "Expected '$1', got '$actual'" >&2
    exit 1
  }
}

# Use the published release even when local tags or a branch name suggest a
# newer version, and do not skip it when the current ref has the same name.
TEST_RELEASE=v1.6.1
GITHUB_REF_NAME=v99.0.0 assert_version 1.6.1
GITHUB_REF_NAME=v1.6.1 assert_version 1.6.1

# An empty release list is distinct from a failed API request.
TEST_RELEASE="" assert_version ""
TEST_STATUS=7
if hh_previous_version >/dev/null; then
  echo "Release API failure was silently ignored" >&2
  exit 1
else
  [ "$?" -eq 7 ]
fi

# An explicit baseline works offline, even if GitHub discovery would fail.
HELM_N1_VERSION=v1.5.1 assert_version 1.5.1
HELM_N1_VERSION=1.5.0 assert_version 1.5.0

echo "✅ Helm release baseline regression checks passed."
