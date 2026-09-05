#!/bin/bash
set -euo pipefail

if [ "$#" -ne 1 ] || [ ! -x "$1" ]; then
  echo 'usage: scripts/install-dogfood.sh PATH-TO-PRIVATE-MAC-BINARY' >&2
  exit 2
fi
dogfood_version=$("$1" --version)
dogfood_version=${dogfood_version#wt version }
if [[ ! "$dogfood_version" =~ ^dogfood-[0-9a-f]{12}-[0-9a-f]{12}$ ]]; then
  echo 'Refusing to install a non-dogfood build.' >&2
  exit 1
fi
dogfood_repo=$(cd "$(dirname "$0")/.." && pwd)
dogfood_root="${HOME}/.local/share/wingthing-dogfood"
dogfood_target="$dogfood_root/builds/$dogfood_version/wt"
mkdir -p "$dogfood_root/builds/$dogfood_version" "$dogfood_root/bin" "${HOME}/.local/bin"
if [ -e "$dogfood_target" ]; then
  cmp -s "$1" "$dogfood_target" || { echo 'Existing private version has different bytes; rebuild with a new digest.' >&2; exit 1; }
else
  install -m 755 "$1" "$dogfood_target"
fi
if [ -e "$dogfood_root/bin/wt" ] && [ ! -L "$dogfood_root/bin/wt" ]; then
  echo 'Refusing to overwrite an unrecognized private binary entry.' >&2
  exit 1
fi
ln -sfn "$dogfood_target" "$dogfood_root/bin/wt"
install -m 755 "$dogfood_repo/scripts/wt-dogfood" "${HOME}/.local/bin/wt-dogfood"
install -m 644 "$dogfood_repo/docs/dogfood-operator.md" "$dogfood_root/README.md"
printf 'Installed %s as %s\n' "$dogfood_version" "${HOME}/.local/bin/wt-dogfood"
