#!/usr/bin/env bash
set -euo pipefail

# install-server.sh — install/upgrade sieve serve on a Linux host.
# Usage: curl -fsSL https://sievereview.dev/install-server.sh | bash
# Flags:
#   -d DIR   install directory (default: /usr/local/bin)
#   -v VER   version tag (default: latest)
#   -s       skip cosign verification

VERSION="latest"
INSTALL_DIR="/usr/local/bin"
VERIFY=1

while getopts "d:v:sh" opt; do
  case "$opt" in
    d) INSTALL_DIR="$OPTARG" ;;
    v) VERSION="$OPTARG" ;;
    s) VERIFY=0 ;;
    h)
      sed -n '2,8p' "$0"
      exit 0
      ;;
    *) exit 1 ;;
  esac
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

if [[ "$OS" != linux ]]; then
  echo "install-server.sh is meant for Linux servers; detected $OS"
  exit 1
fi

if [[ "$VERSION" == "latest" ]]; then
  VERSION=$(curl -fsSL https://api.github.com/repos/gnanam1990/sieve/releases/latest | grep '"tag_name":' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

BASE="https://github.com/gnanam1990/sieve/releases/download/${VERSION}"
BIN="sieve_${VERSION#v}_${OS}_${ARCH}"

curl -fsSL "${BASE}/${BIN}.tar.gz" -o "${TMP}/${BIN}.tar.gz"
curl -fsSL "${BASE}/${BIN}_checksums.txt" -o "${TMP}/checksums.txt"

if command -v cosign >/dev/null 2>&1 && [[ "$VERIFY" -eq 1 ]]; then
  curl -fsSL "${BASE}/${BIN}.tar.gz.sig" -o "${TMP}/${BIN}.tar.gz.sig"
  curl -fsSL "${BASE}/${BIN}.tar.gz.cert" -o "${TMP}/${BIN}.tar.gz.cert"
  cosign verify-blob --certificate "${TMP}/${BIN}.tar.gz.cert" --signature "${TMP}/${BIN}.tar.gz.sig" \
    --certificate-identity-regexp '^https://github.com/gnanam1990/sieve/.github/workflows/release.yml@refs/tags/' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
    "${TMP}/${BIN}.tar.gz" || { echo "cosign verification failed"; exit 1; }
fi

cd "$TMP"
sha256sum --check --ignore-missing "${TMP}/checksums.txt" || { echo "checksum verification failed"; exit 1; }
tar -xzf "${TMP}/${BIN}.tar.gz" -C "$TMP"

mkdir -p "$INSTALL_DIR"
cp -f "${TMP}/sieve" "${INSTALL_DIR}/sieve"
chmod +x "${INSTALL_DIR}/sieve"

echo "sieve ${VERSION} installed to ${INSTALL_DIR}/sieve"
echo "Run: sieve serve --help"
