#!/usr/bin/env bash
# verify-release-notes.sh
#
# Reads back the release body GitHub published for $GITHUB_REF_NAME and compares
# it with the CHANGELOG.md section in tmp/release-notes.md (written by
# `make release-notes`). On a mismatch it rewrites the body from that file and
# appends drift=true to $GITHUB_OUTPUT. Needs gh authenticated (GH_TOKEN) and
# runs from the repository root.
set -euo pipefail
# GitHub round-trips neither line endings nor trailing blank lines
# reliably, so normalize both before comparing.
norm() {
  sed 's/\r$//' "$1" | awk 'NF {last = NR} {line[NR] = $0} END {for (i = 1; i <= last; i++) print line[i]}'
}
published() {
  gh release view "$GITHUB_REF_NAME" --json body -q .body > tmp/published-body.md
  norm tmp/published-body.md > tmp/notes-published.md
}
norm tmp/release-notes.md > tmp/notes-sent.md
published
if cmp -s tmp/notes-sent.md tmp/notes-published.md; then
  echo "Release body matches CHANGELOG.md ($(wc -l < tmp/notes-sent.md | tr -d ' ') lines)"
  exit 0
fi
echo "::warning::Published release body is not the CHANGELOG.md section handed to GoReleaser; rewriting it"
diff -u tmp/notes-sent.md tmp/notes-published.md || true
echo "drift=true" >> "$GITHUB_OUTPUT"
gh release edit "$GITHUB_REF_NAME" --notes-file tmp/release-notes.md
published
if ! cmp -s tmp/notes-sent.md tmp/notes-published.md; then
  echo "::error::Rewriting the release body from CHANGELOG.md did not take"
  diff -u tmp/notes-sent.md tmp/notes-published.md || true
  exit 1
fi
echo "Release body corrected from CHANGELOG.md"
