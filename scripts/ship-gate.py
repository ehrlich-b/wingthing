#!/usr/bin/env python3
"""Linux promotion receipt and cheap deploy preflight; never deploys itself."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile

GATES = [
    ["make", "web"],
    ["go", "vet", "-p", "2", "./..."],
    ["make", "test-launch-plan"],
    ["git", "diff", "--check", "537b462e", "HEAD"],
    ["make", "test-ship-gate"],
    ["make", "test-release-skew"],
]


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def source():
    if git("status", "--porcelain", "--untracked-files=normal"):
        raise ValueError("ship receipt requires committed, clean source")
    return git("rev-parse", "HEAD")


def baseline():
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if not arch:
        raise ValueError("release skew requires Linux amd64 or arm64")
    return Path("downloads/v0.149.5") / f"wt-linux-{arch}"


def digest(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"missing regular release artifact: {path}")
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def verify(receipt):
    if not receipt:
        raise ValueError("RECEIPT is required; run make ship-gate on Linux")
    data = json.loads(Path(receipt).read_text())
    if (data.get("version") != 1 or data.get("head") != source()
            or data.get("platform") != "Linux" or data.get("gates") != GATES):
        raise ValueError("receipt does not match HEAD and the Linux promotion set")
    # The deploy host may be macOS; verify the Linux artifact named by the gate.
    artifact = data.get("baseline", "")
    if artifact not in ("downloads/v0.149.5/wt-linux-amd64", "downloads/v0.149.5/wt-linux-arm64"):
        raise ValueError("receipt has an invalid release baseline")
    if data.get("baseline_sha256") != digest(Path(artifact)):
        raise ValueError("released CLI changed after the ship gate")
    print(f"ship receipt verified for {data['head']}")


def promote(output):
    if platform.system() != "Linux":
        raise ValueError("ship-gate requires Linux; deploy accepts its receipt on any host")
    head = source()
    output.mkdir(parents=True, exist_ok=True)
    # Do not allow a failed rerun to leave an older receipt looking successful.
    output = Path(tempfile.mkdtemp(prefix="run-", dir=output))
    receipt = output / f"ship-gate-{head}.txt"
    artifact = baseline()
    artifact_hash = digest(artifact)
    for i, command in enumerate(GATES, 1):
        log = output / f"{i:02d}.log"
        print("+ " + " ".join(command), flush=True)
        with log.open("w") as stream:
            result = subprocess.run(command, stdout=stream, stderr=subprocess.STDOUT)
        print(log.read_text(), end="", flush=True)
        if result.returncode:
            raise ValueError(f"promotion gate failed ({result.returncode}): {log}")
    if source() != head or digest(artifact) != artifact_hash:
        raise ValueError("source or released CLI changed during promotion")
    with receipt.open("x") as stream:
        json.dump(dict(version=1, head=head, platform="Linux", gates=GATES,
                       baseline=str(artifact), baseline_sha256=artifact_hash), stream, indent=2)
        stream.write("\n")
    print(receipt)


def stage_downloads():
    # Docker never receives the source downloads tree or other local scratch.
    # check-downloads validates this tree before make deploy invokes this step.
    source_root = Path("downloads")
    destination = Path(".ship-downloads")
    if source_root.is_symlink() or not source_root.is_dir():
        raise ValueError("downloads must be a staged release directory")
    expected = {p.relative_to(source_root) for p in source_root.rglob("*") if p.is_file()}
    if destination.is_symlink():
        raise ValueError("download context must not be a symlink")
    if destination.exists():
        actual = {p.relative_to(destination) for p in destination.rglob("*") if p.is_file()}
        if actual - expected:
            raise ValueError("download context contains stale files; use a fresh checkout")
    for relative in sorted(expected):
        src, dst = source_root / relative, destination / relative
        if (src.is_symlink() or any(p.is_symlink() for p in src.parents)
                or any(p.is_symlink() for p in dst.parents) or dst.is_symlink()):
            raise ValueError("download context must contain regular files")
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(src, dst)
    print("staged validated release downloads for Docker")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    sub.add_parser("run").add_argument("--output", type=Path, required=True)
    sub.add_parser("verify").add_argument("--receipt", default="")
    sub.add_parser("stage-downloads")
    args = parser.parse_args()
    try:
        if args.action == "run":
            promote(args.output)
        elif args.action == "verify":
            verify(args.receipt)
        else:
            stage_downloads()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"ship-gate: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
