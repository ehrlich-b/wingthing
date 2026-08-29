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
TAG=$(curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 60 --retry 2 "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$TAG" ]; then
    echo "error: could not find latest release"
    exit 1
fi

URL="https://github.com/${REPO}/releases/download/${TAG}/${BINARY}"
SUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/SHA256SUMS"
echo "downloading wt ${TAG} for ${OS}/${ARCH}..."

TMP=$(mktemp)
SUMS_TMP=$(mktemp)
INSTALL_TMP=""
cleanup() {
    rm -f "$TMP" "$SUMS_TMP"
    if [ -n "$INSTALL_TMP" ]; then rm -f "$INSTALL_TMP"; fi
}
trap cleanup EXIT

curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 300 --retry 2 -o "$TMP" "$URL"
if [ "$(wc -c < "$TMP" | tr -d ' ')" -gt 268435456 ]; then
    echo "error: downloaded binary exceeds 256 MiB" >&2
    exit 1
fi
chmod +x "$TMP"

curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 60 --retry 2 -o "$SUMS_TMP" "$SUMS_URL" || {
    echo "error: release ${TAG} has no published SHA256SUMS manifest" >&2
    exit 1
}
if [ "$(wc -c < "$SUMS_TMP" | tr -d ' ')" -gt 1048576 ]; then
    echo "error: SHA256SUMS exceeds 1 MiB" >&2
    exit 1
fi
MATCHES=$(awk -v binary="$BINARY" '$2 == binary || $2 == "*" binary { print $1 }' "$SUMS_TMP")
if [ "$(printf '%s\n' "$MATCHES" | awk 'NF { n++ } END { print n+0 }')" -ne 1 ]; then
    echo "error: SHA256SUMS must contain exactly one entry for ${BINARY}" >&2
    exit 1
fi
EXPECTED=$MATCHES
if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL=$(sha256sum "$TMP" | awk '{print $1}')
else
    ACTUAL=$(shasum -a 256 "$TMP" | awk '{print $1}')
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
    echo "error: checksum mismatch for ${BINARY}" >&2
    exit 1
fi

# The website and installer are one release contract. Refuse to report a
# successful install when GitHub's latest verified binary predates a workflow
# shown by the current site.
if ! "$TMP" mcp connect --help >/dev/null 2>&1 || \
   ! "$TMP" serve --help 2>/dev/null | grep -q -- '--https' || \
   ! "$TMP" roost start --help 2>/dev/null | grep -q -- '--https' || \
   ! "$TMP" local-cert status --help >/dev/null 2>&1; then
    echo "error: latest release ${TAG} predates the workflows documented by wingthing.ai" >&2
    echo "no binary was installed; wait for the matching GitHub release" >&2
    exit 1
fi

# Install
mkdir -p "$INSTALL_DIR"
INSTALL_TMP=$(mktemp "${INSTALL_DIR}/.wt-install.XXXXXX")
cp "$TMP" "$INSTALL_TMP"
chmod 0755 "$INSTALL_TMP"
mv "$INSTALL_TMP" "${INSTALL_DIR}/wt"
INSTALL_TMP=""

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
echo "give your local Codex or Claude tools to manage agents (no account required):"
echo "  codex mcp add wingthing -- wt mcp stdio --client codex"
echo "  claude mcp add --scope user wingthing -- wt mcp stdio --client claude"
echo ""
echo "manage agents across machines with direct encrypted control:"
echo "  wt login"
echo "  wt start"
echo "  # then register: wt mcp connect --client <parent-agent>"
echo ""
echo "or start a private localhost browser portal:"
echo "  wt roost start --https"
