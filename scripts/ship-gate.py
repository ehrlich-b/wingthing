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
    return Path(os.environ.get("WT_RELEASE_SKEW_BINARY",
                               f"/work/cache/wingthing-releases/v0.149.5/wt-linux-{arch}"))


# Published SHA256SUMS for the original artifacts, never rebuilt tag binaries.
BASELINE_HASHES = {
    "amd64": "dfbe82d1f0fad9ba8eb408acc83cc2ff78aa2f019942cb2bd694db498e7cbccf",
    "arm64": "bcf6fe0d6f8584785e98d2ad31837e33cc6a21fa92758f8411465520c632b63b",
}


def prepare_baseline():
    artifact = baseline()
    actual = digest(artifact)
    expected = BASELINE_HASHES[{"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()]]
    manifest = artifact.parent / "SHA256SUMS"
    entries = [line.split() for line in manifest.read_text().splitlines()]
    matches = [fields[0] for fields in entries
               if len(fields) == 2 and fields[1].lstrip("*") == artifact.name]
    if matches != [actual]:
        raise ValueError(f"release baseline checksum mismatch against {manifest}: {artifact}")
    if actual != expected:
        raise ValueError("release baseline does not match published v0.149.5 checksum")
    if not os.access(artifact, os.X_OK):
        raise ValueError(f"released CLI is not executable: {artifact}")
    return artifact


def digest(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"missing regular release artifact: {path}; set WT_RELEASE_SKEW_BINARY to the original v0.149.5 binary with adjacent SHA256SUMS")
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
    artifact = Path(data.get("baseline", ""))
    asset = data.get("baseline_asset", artifact.name)
    if asset not in ("wt-linux-amd64", "wt-linux-arm64"):
        raise ValueError("receipt has an invalid release baseline")
    # Hosts may retain the same original artifact at different paths.
    artifact = Path(os.environ.get("WT_RELEASE_SKEW_BINARY",
                                  f"/work/cache/wingthing-releases/v0.149.5/{asset}"))
    if data.get("baseline_sha256") != digest(artifact):
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
    artifact = prepare_baseline()
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
                       baseline=str(artifact), baseline_asset="wt-linux-" +
                       {"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()],
                       baseline_sha256=artifact_hash), stream, indent=2)
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
    sub.add_parser("prepare-baseline")
    args = parser.parse_args()
    try:
        if args.action == "run":
            promote(args.output)
        elif args.action == "verify":
            verify(args.receipt)
        elif args.action == "prepare-baseline":
            print(prepare_baseline())
        else:
            stage_downloads()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"ship-gate: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
