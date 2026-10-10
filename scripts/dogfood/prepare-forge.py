#!/usr/bin/env python3
"""Render a finite, reviewable systemd user package; never install or activate it."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shlex
import sys

HERE = Path(__file__).resolve().parent


def utc(value):
    result = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None or result.utcoffset() != datetime.timedelta(0):
        raise ValueError("timestamp must be explicit UTC (Z or +00:00)")
    return result.astimezone(datetime.timezone.utc)


def timestamp(value):
    return value.isoformat().replace("+00:00", "Z")


def systemd_quote(value):
    return '"' + value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'


def script(action, repo):
    preamble = '''#!/usr/bin/env bash
# Review before execution; isolated personal development host only.
set -euo pipefail
umask 077
package_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
unit_dir=${1:-"${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"}
[[ "$unit_dir" = /* ]] || { echo 'Unit directory must be absolute' >&2; exit 2; }
[[ ! -L "$unit_dir" && ! -L "$unit_dir/wingthing-dogfood.service.d" ]] || { echo 'Refusing symlinked unit directory' >&2; exit 2; }
python3 -B - "$package_dir" <<'VERIFY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / 'manifest.json').read_text())
for name, digest in manifest['files'].items():
    file = root / name
    if file.is_symlink() or not file.is_file() or hashlib.sha256(file.read_bytes()).hexdigest() != digest:
        sys.exit('Edited package file; regenerate in a new directory: ' + name)
VERIFY
sources=(wingthing-dogfood.service wingthing-dogfood.timer 10-forge.conf)
targets=(wingthing-dogfood.service wingthing-dogfood.timer wingthing-dogfood.service.d/10-forge.conf)
# Preflight every target before any write or systemctl call.
for i in "${!sources[@]}"; do
    target="$unit_dir/${targets[$i]}"
    [[ ! -L "$target" ]] || { echo "Refusing symlink: $target" >&2; exit 2; }
    if [[ -e "$target" ]]; then
        cmp -s -- "$package_dir/${sources[$i]}" "$target" || { echo "Operator edits preserved; refusing: $target" >&2; exit 2; }
    fi
 done
'''
    if action == "install":
        return preamble + "repo=" + shlex.quote(str(repo)) + '''
[[ -x "$repo/wt" && -x "$repo/scripts/dogfood/run.sh" ]] || { echo 'Missing checkout-local executable' >&2; exit 2; }
mkdir -p -- "$repo/.scratch/tmp" "$unit_dir/wingthing-dogfood.service.d"
for i in "${!sources[@]}"; do
    install -m 600 -- "$package_dir/${sources[$i]}" "$unit_dir/${targets[$i]}"
done
systemctl --user daemon-reload
printf '%s\\n' 'Installed without activation. Coordinator: review expiry, then enable the timer and run one service invocation.'
'''
    return preamble + '''systemctl --user disable --now wingthing-dogfood.timer
systemctl --user stop wingthing-dogfood.service
for target in "${targets[@]}"; do
    rm -f -- "$unit_dir/$target"
done
rmdir -- "$unit_dir/wingthing-dogfood.service.d" 2>/dev/null || true
systemctl --user daemon-reload
# Receipts, retained fixture state and failure evidence are preserved.
'''


def prepare(repo, out, until):
    if not repo.is_absolute():
        raise ValueError("--repo must be an absolute checkout path")
    repo = repo.resolve(strict=True)
    if any(char in str(repo) + str(out) for char in "\n\r\x00"):
        raise ValueError("paths must not contain control characters")
    for name in ("wt", "scripts/dogfood/run.sh", "scripts/dogfood/canary.sh"):
        target = repo / name
        if not target.is_file() or not os.access(target, os.X_OK):
            raise ValueError("missing checkout-local executable: " + str(target))
        if not target.resolve().is_relative_to(repo):
            raise ValueError("executable escapes checkout: " + str(target))
    if not (repo / "scripts/dogfood/run.py").is_file():
        raise ValueError("missing runner source")
    end = utc(until)
    epoch = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
    if end <= epoch:
        raise ValueError("--until must be in the future")
    if out.exists():
        raise ValueError("output already exists; operator files preserved: " + str(out))
    environment = {"WT_DOGFOOD_REPO": str(repo), "WT_DOGFOOD_EPOCH": timestamp(epoch),
                   "WT_DOGFOOD_UNTIL": timestamp(end), "TMPDIR": str(repo / ".scratch/tmp"),
                   "GOTMPDIR": str(repo / ".scratch/tmp"), "GOMAXPROCS": "2",
                   "GOFLAGS": "-p=2", "PYTHONDONTWRITEBYTECODE": "1"}
    dropin = "[Service]\n" + "".join("Environment=" + systemd_quote(k + "=" + v) + "\n"
                                             for k, v in environment.items())
    files = {"wingthing-dogfood.service": (HERE / "wingthing-dogfood.service").read_text(),
             "wingthing-dogfood.timer": (HERE / "wingthing-dogfood.timer").read_text(),
             "10-forge.conf": dropin,
             "forge.env": "# Source for a finite one-shot in this same checkout.\n" +
                          "".join("export " + k + "=" + shlex.quote(v) + "\n" for k, v in environment.items()),
             "install.sh": script("install", repo), "remove.sh": script("remove", repo)}
    manifest = {"schema": 1, "repo": str(repo), "epoch": timestamp(epoch), "until": timestamp(end),
                "files": {name: hashlib.sha256(data.encode()).hexdigest() for name, data in files.items()}}
    os.umask(0o077)
    out.mkdir(mode=0o700, parents=True)
    for name, data in files.items():
        target = out / name
        target.write_text(data)
        target.chmod(0o700 if name.endswith(".sh") else 0o600)
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--until", required=True)
    args = parser.parse_args()
    try:
        manifest = prepare(args.repo, args.out.absolute(), args.until)
    except (ValueError, OSError) as error:
        parser.exit(2, str(error) + "\n")
    print(json.dumps({"package": str(args.out.absolute()), "epoch": manifest["epoch"],
                      "until": manifest["until"], "installed": False}))


if __name__ == "__main__":
    main()
