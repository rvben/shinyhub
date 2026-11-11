#!/usr/bin/env bash
# smoke-wheel.sh <version>
#
# Installs the built wheel from packaging/python/dist into a fresh venv and
# requires `shinyhub --version` to contain <version>.
set -euo pipefail
python -m venv /tmp/venv
/tmp/venv/bin/pip install packaging/python/dist/*.whl
OUT="$(/tmp/venv/bin/shinyhub --version)"
echo "Binary reported: $OUT"
EXPECTED="${1#v}"
echo "$OUT" | grep -q "$EXPECTED" \
  || { echo "shinyhub --version output '$OUT' does not contain '$EXPECTED'"; exit 1; }
