#!/bin/sh
# Install dsui-agent from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/northgraindata/dsui-agent/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.2.0 --bin-dir ~/.local/bin
#
# Options (or environment variables):
#   --version <tag>   release tag, default latest         DSUI_AGENT_VERSION
#   --bin-dir <dir>   install directory, default below   DSUI_AGENT_BIN_DIR
#
# The download is verified against the release's checksums.txt.
# Default directory: /usr/local/bin when writable, otherwise ~/.local/bin.

set -eu

REPO="northgraindata/dsui-agent"
VERSION="${DSUI_AGENT_VERSION:-}"
BIN_DIR="${DSUI_AGENT_BIN_DIR:-}"

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
    --bin-dir) [ $# -ge 2 ] || die "--bin-dir needs a value"; BIN_DIR="$2"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

command -v curl >/dev/null 2>&1 || die "curl is required"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS: $(uname -s). On Windows see examples/windows.md" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || die "could not determine the latest release"
fi
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
num="${VERSION#v}"

if [ -z "$BIN_DIR" ]; then
  if [ -w /usr/local/bin ]; then BIN_DIR=/usr/local/bin; else BIN_DIR="$HOME/.local/bin"; fi
fi
mkdir -p "$BIN_DIR"

asset="dsui-agent_${num}_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading $asset ($VERSION)"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "download failed: $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "download failed: $base/checksums.txt"

want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "$asset not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
  got="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[ "$want" = "$got" ] || die "checksum mismatch for $asset"

chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$BIN_DIR/dsui-agent"

say "Installed $BIN_DIR/dsui-agent"
"$BIN_DIR/dsui-agent" version >&2 || true
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "Note: $BIN_DIR is not on your PATH." ;;
esac
