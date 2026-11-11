#!/usr/bin/env bash
# verify-pypi-install.sh <version>
#
# Installs exactly shinyhub==<version> from PyPI into a fresh venv and requires
# `shinyhub --version` to report it.
set -euo pipefail
VERSION="${1#v}"
python -m venv /tmp/pypi-venv
/tmp/pypi-venv/bin/pip install --no-cache-dir --index-url https://pypi.org/simple/ "shinyhub==${VERSION}"
OUT="$(/tmp/pypi-venv/bin/shinyhub --version)"
echo "Installed binary reported: ${OUT}"
echo "${OUT}" | grep -qF "${VERSION}" \
  || { echo "::error::shinyhub --version printed '${OUT}', not ${VERSION}"; exit 1; }
