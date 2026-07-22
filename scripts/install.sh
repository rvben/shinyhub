#!/bin/sh
# ShinyHub installer.
# Usage: curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh
set -e

REPO="rvben/shinyhub"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin) ;;
  *) printf 'Unsupported OS: %s\n' "$OS" >&2; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) printf 'Unsupported architecture: %s\n' "$ARCH" >&2; exit 1 ;;
esac

# Resolve latest version tag from GitHub API
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' \
  | sed 's/.*"tag_name": *"//;s/".*//')

if [ -z "$VERSION" ]; then
  printf 'Could not determine latest version\n' >&2
  exit 1
fi

TARBALL="shinyhub_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

printf 'Installing shinyhub %s (%s/%s) to %s...\n' "$VERSION" "$OS" "$ARCH" "$INSTALL_DIR"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "${BASE_URL}/${TARBALL}"    -o "${TMP}/${TARBALL}"
curl -fsSL "${BASE_URL}/checksums.txt" -o "${TMP}/checksums.txt"

cd "$TMP"

# Trust model:
#   1. checksums.txt binds the SHA-256 of every release artifact. The tarball
#      hash is verified against it below. This is MANDATORY and fails closed:
#      if no SHA-256 tool is present we abort rather than install unverified
#      bytes.
#   2. checksums.txt itself is (optionally) signed at release time with cosign
#      keyless signing (Sigstore). If cosign is installed locally and the
#      signature artifacts are published, we verify that the checksums file was
#      produced by this repo's GitHub Actions release workflow before trusting
#      any hash in it. cosign being absent only downgrades to plain checksum
#      trust; it never silently skips the checksum itself.

# Step 1: mandatory checksum verification (fail closed).
if command -v sha256sum >/dev/null 2>&1; then
  grep "${TARBALL}" checksums.txt | sha256sum -c -
elif command -v shasum >/dev/null 2>&1; then
  grep "${TARBALL}" checksums.txt | shasum -a 256 -c -
else
  printf 'Error: no SHA-256 tool (sha256sum or shasum) found; refusing to install unverified binary\n' >&2
  exit 1
fi

# Step 2: optional signature verification of checksums.txt via cosign.
#
# Releases from v0.10.14 publish a Sigstore bundle (checksums.txt.bundle), which
# carries the signature and the signing certificate in one file. Earlier releases
# published a detached checksums.txt.sig plus checksums.txt.pem, so both are
# accepted: pinning an older version must not quietly lose signature checking.
#
# Either way the identity is asserted, not just the maths - an unbound signature
# proves only that somebody signed this, not that this repo's release workflow
# did. A failed verification aborts the install (set -e); only the ABSENCE of any
# signature downgrades to checksum-only trust.
if command -v cosign >/dev/null 2>&1; then
  IDENTITY_RE="^https://github.com/${REPO}/\.github/workflows/.+@refs/tags/${VERSION}$"
  OIDC_ISSUER="https://token.actions.githubusercontent.com"
  if curl -fsSL "${BASE_URL}/checksums.txt.bundle" -o "${TMP}/checksums.txt.bundle" 2>/dev/null; then
    printf 'Verifying checksums.txt signature with cosign (bundle)...\n'
    cosign verify-blob \
      --bundle "${TMP}/checksums.txt.bundle" \
      --certificate-identity-regexp "${IDENTITY_RE}" \
      --certificate-oidc-issuer "${OIDC_ISSUER}" \
      "${TMP}/checksums.txt"
  elif curl -fsSL "${BASE_URL}/checksums.txt.sig" -o "${TMP}/checksums.txt.sig" 2>/dev/null \
     && curl -fsSL "${BASE_URL}/checksums.txt.pem" -o "${TMP}/checksums.txt.pem" 2>/dev/null; then
    printf 'Verifying checksums.txt signature with cosign (detached, pre-v0.10.14)...\n'
    cosign verify-blob \
      --certificate "${TMP}/checksums.txt.pem" \
      --signature "${TMP}/checksums.txt.sig" \
      --certificate-identity-regexp "${IDENTITY_RE}" \
      --certificate-oidc-issuer "${OIDC_ISSUER}" \
      "${TMP}/checksums.txt"
  else
    printf 'Note: cosign present but no signature published for %s; relying on checksum only\n' "$VERSION" >&2
  fi
else
  printf 'Note: cosign not installed; skipping signature verification (checksum still enforced)\n' >&2
fi

tar -xzf "${TARBALL}"

# Install to INSTALL_DIR, using sudo if the directory is not writable.
if [ -w "$INSTALL_DIR" ]; then
  install -m 755 shinyhub "$INSTALL_DIR/shinyhub"
else
  sudo install -m 755 shinyhub "$INSTALL_DIR/shinyhub"
fi

printf 'Installed shinyhub to %s\n' "$INSTALL_DIR"
printf "Run 'shinyhub --help' to get started, or 'shinyhub serve' to start the server.\n"
