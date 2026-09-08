#!/usr/bin/env bash
# verify-docs-selfcontained.sh — fail if published content points at files that
# are not part of this repository.
#
# Some planning and tooling artefacts live outside this repository entirely.
# A sentence like "see the research notes, decision R4" or "per requirement
# FR-008" is a dead reference for everyone who clones this repo: the file it
# names is simply not there. Content that ships here must therefore explain its
# own reasoning in place, rather than delegating it to a document the reader
# cannot open.
#
# The patterns below are matched literally against published content. This
# script is excluded from its own scan for the obvious reason that it has to
# spell the forbidden patterns out in order to look for them.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Paths whose content is published or shipped to users of this repository.
#
# api/ is here because crd-ref-docs copies its Go doc comments verbatim into
# docs/reference/api.md — a dead reference written in a package comment is
# published just as surely as one written in Markdown, and this gate is the only
# thing that looks at it before it ships. CHANGELOG.md and the chart README are
# both rendered into the site as well.
TARGETS=(docs mkdocs.yml README.md CONTRIBUTING.md api CHANGELOG.md
         charts/homeassistant-operator/README.md)

PATTERN='FR-[0-9]{3}'
PATTERN+='|SC-[0-9]{3}'
PATTERN+='|\bUS[0-9]+\b'
PATTERN+='|\bT[0-9]{3}\b'
PATTERN+='|research\.md'
PATTERN+='|data-model\.md'
PATTERN+='|quickstart\.md'
PATTERN+='|(^|[^a-z.])spec\.md'
PATTERN+='|(^|[^a-z.])plan\.md'
PATTERN+='|(^|[^a-z.])tasks\.md'
PATTERN+='|NEEDS CLARIFICATION'
PATTERN+='|specs/[0-9]{3}-[a-z0-9-]+'
PATTERN+='|checklists/requirements\.md'
# .claude/ is a filesystem link to a separate tooling repository. It is never
# committed here, so "see CLAUDE.md" is unresolvable for anyone who clones this
# repo — the same dead reference as the planning artefacts above.
PATTERN+='|CLAUDE\.md'
PATTERN+='|\.claude/'

existing=()
for t in "${TARGETS[@]}"; do
  [ -e "$t" ] && existing+=("$t")
done

if [ ${#existing[@]} -eq 0 ]; then
  echo "❌ nothing to scan — expected at least one of: ${TARGETS[*]}" >&2
  exit 2
fi

if hits="$(grep -rEn "$PATTERN" "${existing[@]}" 2>/dev/null)"; then
  echo "❌ published content references files that are not in this repository:" >&2
  echo "$hits" >&2
  echo >&2
  echo "Explain the reasoning in place instead of pointing at an external document." >&2
  exit 1
fi

echo "✅ published content is self-contained"
