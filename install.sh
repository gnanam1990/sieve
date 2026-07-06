#!/usr/bin/env bash
#
# install.sh — download, verify, and install the sieve binary.
#
#   curl -fsSL https://raw.githubusercontent.com/gnanam1990/sieve/v0/install.sh | bash
#
# Overrides via env: SIEVE_VERSION (default: latest), PREFIX (install prefix).
set -euo pipefail

REPO="gnanam1990/sieve"
VERSION="${SIEVE_VERSION:-latest}"

die() {
	echo "install.sh: $*" >&2
	exit 1
}

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch="amd64" ;;
aarch64 | arm64) arch="arm64" ;;
*) die "unsupported architecture: ${arch}" ;;
esac
case "$os" in
linux | darwin) ;;
*) die "unsupported OS: ${os} (Windows is on the backlog)" ;;
esac

asset="sieve_${os}_${arch}"
if [ "$VERSION" = "latest" ]; then
	base="https://github.com/${REPO}/releases/latest/download"
else
	base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

echo "downloading ${asset} (${VERSION})..."
curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}" || die "download failed: ${base}/${asset}"
curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt" || die "download failed: checksums.txt"

# `|| true` keeps a no-match (grep exit 1) from aborting under `set -e`/pipefail
# before the friendly die message below.
want="$(grep " ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}' || true)"
[ -n "${want}" ] || die "no checksum entry for ${asset}"
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
else
	got="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
fi
[ "${want}" = "${got}" ] || die "checksum mismatch for ${asset} (expected ${want}, got ${got})"
echo "checksum ok"

if [ -n "${PREFIX:-}" ]; then
	dest="${PREFIX}/bin"
elif [ -w /usr/local/bin ]; then
	dest="/usr/local/bin"
else
	dest="${HOME}/.local/bin"
fi
mkdir -p "${dest}"
install -m 0755 "${tmp}/${asset}" "${dest}/sieve"
echo "installed sieve to ${dest}/sieve"

case ":${PATH}:" in
*":${dest}:"*) ;;
*) echo "note: ${dest} is not on your PATH — add it to run 'sieve' directly" >&2 ;;
esac

"${dest}/sieve" version || true
