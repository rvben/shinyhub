#!/usr/bin/env bash
# Run scripts/verify-release-asset.sh against a published release: it must
# accept the release's own tarball and refuse every way that tarball or its
# provenance can be swapped. Needs gh (authenticated), cosign and network
# access to GitHub and the Sigstore trust root.
#
#   SHINYHUB_VERIFY_TAG  release to check (default: the latest release)
#   COSIGN               cosign binary (default: cosign on PATH)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERIFY="${ROOT}/scripts/verify-release-asset.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

fail() {
  echo "RELEASE ASSET E2E FAIL: $*" >&2
  exit 1
}

export COSIGN="${COSIGN:-cosign}"
command -v "${COSIGN}" >/dev/null 2>&1 || fail "cosign is required (set COSIGN to its path)"
TAG="${SHINYHUB_VERIFY_TAG:-$(gh release view --repo rvben/shinyhub --json tagName -q .tagName)}"
ASSET="shinyhub_linux_amd64.tar.gz"

gh release download "${TAG}" --repo rvben/shinyhub \
  -p "${ASSET}" -p checksums.txt -p checksums.txt.bundle -D "${WORK}/genuine"

# scenario <name>: a fresh copy of the genuine download to mutate.
scenario() {
  DIR="${WORK}/$1"
  cp -R "${WORK}/genuine" "${DIR}"
}

# expect_refused <name> <tag> <reason pattern> [env...]: the verifier must exit
# non-zero and say why.
expect_refused() {
  local name="$1" tag="$2" reason="$3"
  shift 3
  if env "$@" "${VERIFY}" "${DIR}" "${ASSET}" "${tag}" > "${WORK}/${name}.out" 2>&1; then
    fail "${name}: accepted"
  fi
  grep -Eq "${reason}" "${WORK}/${name}.out" \
    || fail "${name}: refused without the expected reason /${reason}/:
$(cat "${WORK}/${name}.out")"
}

scenario genuine-check
"${VERIFY}" "${DIR}" "${ASSET}" "${TAG}" > "${WORK}/genuine.out" 2>&1 \
  || fail "the genuine ${TAG} asset was refused:
$(cat "${WORK}/genuine.out")"

scenario tampered-tarball
printf 'x' >> "${DIR}/${ASSET}"
expect_refused tampered-tarball "${TAG}" "FAILED|did not match"

# An attacker who can replace the tarball can usually replace checksums.txt
# beside it; only the signature over checksums.txt catches that.
scenario forged-checksums
printf 'x' >> "${DIR}/${ASSET}"
forged="$(shasum -a 256 "${DIR}/${ASSET}" | awk '{print $1}')"
awk -v f="${ASSET}" -v h="${forged}" '$2 == f { print h "  " $2; next } { print }' \
  "${WORK}/genuine/checksums.txt" > "${DIR}/checksums.txt"
expect_refused forged-checksums "${TAG}" "signature|verif"

scenario wrong-tag
expect_refused wrong-tag "v0.0.1" "identity|certificate|verif"

scenario missing-bundle
rm "${DIR}/checksums.txt.bundle"
expect_refused missing-bundle "${TAG}" "checksums.txt.bundle is missing"

scenario no-cosign
expect_refused no-cosign "${TAG}" "cosign" COSIGN=/nonexistent/cosign

echo "release asset e2e: ok (${TAG} ${ASSET})"
