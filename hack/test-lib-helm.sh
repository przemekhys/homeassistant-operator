#!/usr/bin/env bash
# Regression checks for the published-release upgrade baseline; no network needed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/lib-helm.sh
source "$ROOT/hack/lib-helm.sh"
unset HELM_N1_VERSION
export GITHUB_REPOSITORY="example/operator"

gh() {
  if [ "$*" != "api repos/example/operator/releases?per_page=100 --paginate --slurp" ]; then
    echo "Unexpected release discovery arguments: $*" >&2
    return 2
  fi
  [ "${TEST_STATUS:-0}" -eq 0 ] || return "$TEST_STATUS"
  printf '%s\n' "$TEST_RELEASES"
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
TEST_RELEASES='[[
  {"tag_name":"v1.7.0","draft":false,"prerelease":false,"published_at":"2026-10-20T10:00:00Z"},
  {"tag_name":"v2.0.0","draft":true,"prerelease":false,"published_at":null},
  {"tag_name":"v1.8.0-rc.0","draft":false,"prerelease":true,"published_at":"2026-10-23T10:00:00Z"}
], [
  {"tag_name":"v1.6.1","draft":false,"prerelease":false,"published_at":"2026-10-21T10:00:00Z"}
]]'
# The last-published stable release is on a later page, despite its older tag.
GITHUB_REF_NAME=v99.0.0 assert_version 1.6.1
GITHUB_REF_NAME=v1.6.1 assert_version 1.6.1

# An empty release list is distinct from a failed API request.
TEST_RELEASES='[[]]' assert_version ""
TEST_RELEASES='[[{"tag_name":"v2.0.0-rc.0","draft":false,"prerelease":true,"published_at":"2026-10-23T10:00:00Z"}]]' assert_version ""
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
