#!/usr/bin/env bash
# verify-release-asset.sh <dir> <asset> <tag>
#
# Proves that <dir>/<asset> is the file this repo's release workflow published
# for <tag>, before anything repackages it. <dir> must also hold the release's
# checksums.txt and checksums.txt.bundle.
#
#   1. The Sigstore bundle must verify checksums.txt and bind it to a workflow
#      in this repository running for exactly <tag>. Without that, a swapped
#      tarball arrives with a matching checksums.txt beside it.
#   2. The asset's own line in checksums.txt, matched by exact file name, must
#      match the file. checksums.txt also lists each tarball's SBOM, whose name
#      starts with the tarball's.
#
# Everything fails closed: no cosign, no bundle or no checksum line is a refusal,
# never a downgrade. COSIGN overrides the cosign binary.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <dir> <asset> <tag>" >&2
  exit 2
fi
DIR="$1"
ASSET="$2"
TAG="$3"
COSIGN="${COSIGN:-cosign}"
REPO="rvben/shinyhub"

refuse() {
  echo "verify-release-asset: $*" >&2
  exit 1
}

command -v "${COSIGN}" >/dev/null 2>&1 \
  || refuse "cosign not found (${COSIGN}); refusing to trust an unsigned checksums.txt"
[ -f "${DIR}/checksums.txt" ] || refuse "${DIR}/checksums.txt is missing"
[ -f "${DIR}/checksums.txt.bundle" ] || refuse "${DIR}/checksums.txt.bundle is missing"
[ -f "${DIR}/${ASSET}" ] || refuse "${DIR}/${ASSET} is missing"

# Dots in the tag are literal, not regex wildcards.
TAG_RE="$(printf '%s' "${TAG}" | sed 's/[.]/\\./g')"
"${COSIGN}" verify-blob \
  --bundle "${DIR}/checksums.txt.bundle" \
  --certificate-identity-regexp "^https://github\.com/${REPO}/\.github/workflows/.+@refs/tags/${TAG_RE}$" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${DIR}/checksums.txt" \
  || refuse "checksums.txt signature does not verify as ${REPO}'s release workflow for ${TAG}"

ENTRY="$(awk -v f="${ASSET}" '$2 == f' "${DIR}/checksums.txt")"
[ -n "${ENTRY}" ] || refuse "checksums.txt has no checksum for ${ASSET}"
[ "$(printf '%s\n' "${ENTRY}" | wc -l)" -eq 1 ] || refuse "checksums.txt lists ${ASSET} more than once"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "${DIR}" && printf '%s\n' "${ENTRY}" | sha256sum -c -) \
    || refuse "${ASSET} did not match its signed checksum"
else
  (cd "${DIR}" && printf '%s\n' "${ENTRY}" | shasum -a 256 -c -) \
    || refuse "${ASSET} did not match its signed checksum"
fi

echo "verify-release-asset: ${ASSET} is ${REPO} ${TAG}"
