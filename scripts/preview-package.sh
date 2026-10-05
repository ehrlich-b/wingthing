#!/bin/sh
# Install/uninstall a reviewed LOCAL preview package. Never fetch credentials,
# register a daemon, enroll an organization, migrate state, or touch stable wt.
set -eu
ACTION=${1:-install}
INSTALL_DIR=${WT_PREVIEW_INSTALL_DIR:-${HOME}/.local/bin}
PACKAGE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TARGET=${INSTALL_DIR}/wt-preview
case "$ACTION" in
  uninstall)
    # Refuse a symlink so even an accidentally aliased install is preserved.
    if [ -L "$TARGET" ]; then echo "refusing symlink preview executable" >&2; exit 1; fi
    if [ -e "$TARGET" ]; then
      "$TARGET" --expected-channel preview channel --json >/dev/null
      rm -- "$TARGET"
    fi
    echo "removed preview executable; preview sessions/state retained"
    exit 0 ;;
  install) ;;
  *) echo "usage: preview-package.sh [install|uninstall]" >&2; exit 2 ;;
esac
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in arm64|aarch64) ARCH=arm64 ;; amd64|x86_64) ARCH=amd64 ;; *) exit 1 ;; esac
ASSET=wt-preview-${OS}-${ARCH}
SOURCE=${PACKAGE_DIR}/${ASSET}
EXPECTED=$(awk -v asset="$ASSET" '$2 == asset {print $1}' "${PACKAGE_DIR}/SHA256SUMS")
if [ "$(printf '%s\n' "$EXPECTED" | awk 'NF {n++} END {print n+0}')" -ne 1 ]; then echo "missing/duplicate preview checksum" >&2; exit 1; fi
if command -v sha256sum >/dev/null 2>&1; then ACTUAL=$(sha256sum "$SOURCE" | awk '{print $1}'); else ACTUAL=$(shasum -a 256 "$SOURCE" | awk '{print $1}'); fi
if [ "$ACTUAL" != "$EXPECTED" ]; then echo "preview checksum mismatch" >&2; exit 1; fi
"$SOURCE" --expected-channel preview channel --json >/dev/null
mkdir -p -- "$INSTALL_DIR"
if [ -L "$TARGET" ]; then echo "refusing symlink preview executable" >&2; exit 1; fi
if [ -e "$TARGET" ]; then "$TARGET" --expected-channel preview channel --json >/dev/null; fi
TEMP=$(mktemp "${INSTALL_DIR}/.wt-preview-install.XXXXXX")
trap 'rm -f -- "$TEMP"' EXIT
cp -- "$SOURCE" "$TEMP"
chmod 0755 "$TEMP"
mv -- "$TEMP" "$TARGET"
echo "installed preview executable: ${TARGET} (state unchanged)"
