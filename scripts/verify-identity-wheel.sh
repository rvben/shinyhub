#!/usr/bin/env bash
# verify-identity-wheel.sh
#
# Installs the built shinyhub-identity wheel from packaging/python-identity/dist
# into a fresh venv, imports it and runs its tests.
set -euo pipefail
python -m venv /tmp/id-venv
/tmp/id-venv/bin/pip install --upgrade pip pytest
/tmp/id-venv/bin/pip install packaging/python-identity/dist/*.whl
/tmp/id-venv/bin/python -c "from shinyhub_identity import current_user; print('import ok')"
/tmp/id-venv/bin/pytest packaging/python-identity/tests/ -q
