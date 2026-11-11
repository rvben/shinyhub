#!/usr/bin/env bash
# docs-check-tracked.sh
#
# Fails when internal planning or report files are tracked, since the
# documentation build would otherwise publish them.
set -e
private_files="$(git ls-files -- docs/superpowers docs/reports 'docs/**/*.html')"
if [[ -n "$private_files" ]]; then
  echo "Internal planning/report files must not be tracked:" >&2
  echo "$private_files" >&2
  exit 1
fi
