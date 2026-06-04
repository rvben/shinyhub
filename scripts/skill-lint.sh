#!/usr/bin/env bash
# Static checks for the deploy-shinyhub skill. No network, no build; safe to run
# anywhere and fast. CI runs it on every push so the skill cannot rot silently.
#
# Checks:
#   - SKILL.md exists and declares name + description frontmatter,
#   - the example bundle (app.py + requirements.txt) is present,
#   - no em/en dashes in the published skill content (project house style),
#   - every docs/<file>.md referenced by the skill actually exists.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SKILL_DIR="${ROOT}/skills/deploy-shinyhub"
SKILL="${SKILL_DIR}/SKILL.md"

fail() { echo "SKILL-LINT FAIL: $*" >&2; exit 1; }

[ -f "${SKILL}" ] || fail "missing ${SKILL}"

# Frontmatter must declare name and description (within the leading block).
head -n 20 "${SKILL}" | grep -Eq '^name:[[:space:]]*[^[:space:]]' || fail "frontmatter missing 'name:'"
head -n 20 "${SKILL}" | grep -Eq '^description:[[:space:]]*[^[:space:]]' || fail "frontmatter missing 'description:'"

# Frontmatter must be valid YAML. The common break is an unquoted value that
# contains ": " (colon-space), which YAML reads as a nested mapping and rejects -
# marketplaces then silently drop the skill ("no skills found"). Flag any
# top-level frontmatter value that is unquoted and contains a colon-space.
fm_bad="$(perl -0777 -ne '
  if (/^---\s*$(.*?)^---\s*$/ms) {
    for my $line (split /\n/, $1) {
      next unless $line =~ /^([A-Za-z0-9_-]+):\s+(.*)$/;
      my $val = $2;
      next if $val =~ /^["\x27]/;   # a quoted value is safe
      print "$line\n" if $val =~ /:\s/;
    }
  }
' "${SKILL}" 2>/dev/null || true)"
if [ -n "${fm_bad}" ]; then
  echo "frontmatter value is unquoted and contains \": \" (breaks YAML); quote it or drop the colon:" >&2
  echo "${fm_bad}" >&2
  fail "frontmatter is not valid YAML"
fi

# Example bundle must exist (skill instructs deploying it; smoke test runs it).
[ -f "${SKILL_DIR}/example-app/app.py" ] || fail "missing example-app/app.py"
[ -f "${SKILL_DIR}/example-app/requirements.txt" ] || fail "missing example-app/requirements.txt"

# No em/en dashes anywhere in the published skill (portable check via perl, which
# is present on both macOS and Linux; BSD grep lacks -P).
dash_report="$(find "${SKILL_DIR}" -type f -print0 \
  | xargs -0 perl -CSD -ne 'print "$ARGV:$.: $_" if /[\x{2013}\x{2014}]/' 2>/dev/null || true)"
if [ -n "${dash_report}" ]; then
  echo "em/en dashes found (use a plain hyphen):" >&2
  echo "${dash_report}" >&2
  fail "skill content must not contain em/en dashes"
fi

# Every docs/<file>.md the skill links must exist.
missing=0
for doc in $(grep -oE 'docs/[A-Za-z0-9_-]+\.md' "${SKILL}" | sort -u); do
  if [ ! -f "${ROOT}/${doc}" ]; then
    echo "referenced doc does not exist: ${doc}" >&2
    missing=1
  fi
done
[ "${missing}" = "0" ] || fail "skill references nonexistent docs"

echo "SKILL-LINT PASS"
