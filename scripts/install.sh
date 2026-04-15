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

# Verify checksum. sha256sum on Linux; shasum on macOS.
cd "$TMP"
if command -v sha256sum >/dev/null 2>&1; then
  grep "${TARBALL}" checksums.txt | sha256sum -c -
elif command -v shasum >/dev/null 2>&1; then
  grep "${TARBALL}" checksums.txt | shasum -a 256 -c -
else
  printf 'Warning: no checksum tool found; skipping verification\n' >&2
fi

tar -xzf "${TARBALL}"

# Install to INSTALL_DIR, using sudo if the directory is not writable.
do_install() {
  install -m 755 shinyhub "$INSTALL_DIR/shinyhub"
  install -m 755 shiny    "$INSTALL_DIR/shiny"
}

if [ -w "$INSTALL_DIR" ]; then
  do_install
else
  sudo sh -c "$(declare -f do_install); INSTALL_DIR='$INSTALL_DIR'; do_install"
fi

printf 'Installed shinyhub and shiny to %s\n' "$INSTALL_DIR"
printf "Run 'shinyhub --help' to get started.\n"
