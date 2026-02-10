#!/bin/sh
set -e

REPO="ehrlich-b/wingthing"
INSTALL_DIR="${WT_INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
    linux|darwin) ;;
    *) echo "unsupported OS: $OS"; exit 1 ;;
esac

BINARY="wt-${OS}-${ARCH}"

# Get latest release tag
echo "fetching latest release..."
TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$TAG" ]; then
    echo "error: could not find latest release"
    exit 1
fi

URL="https://github.com/${REPO}/releases/download/${TAG}/${BINARY}"
echo "downloading wt ${TAG} for ${OS}/${ARCH}..."

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

curl -fsSL -o "$TMP" "$URL"
chmod +x "$TMP"

# Install
mkdir -p "$INSTALL_DIR"
mv "$TMP" "${INSTALL_DIR}/wt"

echo "installed wt ${TAG} to ${INSTALL_DIR}/wt"

# Check if INSTALL_DIR is in PATH
case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *) echo ""
       echo "add to your PATH:"
       echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
       ;;
esac

echo ""
echo "get started:"
echo "  wt login"
echo "  wt wing -d"
