#!/usr/bin/env python3
"""Run release gates and bind verified mirror assets to their source and logs.

No tag creation, network publication or deployment is performed here.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

ASSETS = [f"wt-{system}-{arch}" for system in ("linux", "darwin") for arch in ("amd64", "arm64")]
GATES = {
    "linux": [
        ["make", "check"],
        ["npm", "install", "-g", "@anthropic-ai/claude-code@2.1.260"],
        ["make", "gate", "GATE=claude"],
        ["make", "gate", "GATE=static", "RACE_PACKAGES=./..."],
        ["make", "gate", "GATE=integration"],
        ["make", "gate", "GATE=compat"],
        ["make", "e2e-linux", "LINUX_DISTROS=debian"],
        ["make", "e2e-linux", "LINUX_DISTROS=ubuntu"],
        ["make", "e2e-web"],
        ["git", "diff", "--check"],
    ],
    "macos": [["make", "e2e-mac"]],
    # Local evidence is explicitly ineligible for publication; it cannot replace
    # the provider, Docker, compatibility or native macOS release gates.
    "local": [["make", "check"]],
}


def run(args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def source(tag, dry_run):
    subprocess.run(["sh", "scripts/check-release-tag.sh", tag], check=True)
    head = run(["git", "rev-parse", "HEAD"])
    if run(["git", "status", "--porcelain", "--untracked-files=no"]):
        raise ValueError("release source has tracked changes")
    if not dry_run and run(["git", "rev-parse", f"refs/tags/{tag}^{{commit}}"] ) != head:
        raise ValueError("release tag does not resolve to HEAD")
    if os.environ.get("CI_COMMIT_SHA", head) != head:
        raise ValueError("CI source does not match HEAD")
    return head


def digest(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_size == 0:
        raise ValueError(f"missing nonempty regular artifact: {path}")
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    # Refuse to overwrite a receipt from another invocation.
    with path.open("x") as out:
        json.dump(value, out, indent=2)
        out.write("\n")


def gate(args):
    dry_run = args.profile == "local"
    head = source(args.tag, dry_run)
    toolchain = {"go": run(["go", "version"]), "node": run(["node", "--version"])}
    if not dry_run and (" go1.26.9 " not in toolchain["go"] or not toolchain["node"].startswith("v22.")):
        raise ValueError("release runners require Go 1.26.9 and Node 22")
    records = []
    for number, command in enumerate(GATES[args.profile]):
        log = Path(args.output).with_suffix(f".{number}.log")
        log.parent.mkdir(parents=True, exist_ok=True)
        with log.open("x") as output:
            result = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT)
        records.append({"command": command, "exit_code": result.returncode,
                        "log": str(log), "sha256": digest(log)})
        print(f"{command}: exit {result.returncode}; {log}", flush=True)
        if result.returncode:
            raise ValueError("release gate failed; no passing receipt written")
    if source(args.tag, dry_run) != head:
        raise ValueError("source changed during gates")
    write_json(args.output, {"tag": args.tag, "head": head, "profile": args.profile,
                            "dry_run": dry_run, "commands": records,
                            "toolchain": toolchain,
                            "pipeline_url": os.environ.get("CI_PIPELINE_URL")})


def package(args):
    head = source(args.tag, args.dry_run)
    gates = []
    for filename in args.gate:
        value = json.loads(Path(filename).read_text())
        profile = value["profile"]
        if (value["head"] != head or value["tag"] != args.tag
                or profile not in GATES or value["dry_run"] != (profile == "local")
                or [c["command"] for c in value["commands"]] != GATES[profile]
                or any(c["exit_code"] != 0 for c in value["commands"])):
            raise ValueError(f"incompatible gate receipt: {filename}")
        # CI retains the complete logs alongside their receipt, including macOS.
        for command in value["commands"]:
            if digest(command["log"]) != command["sha256"]:
                raise ValueError("gate log checksum mismatch")
        gates.append({"receipt": value, "sha256": digest(filename)})
    expected = {"local"} if args.dry_run else {"linux", "macos"}
    if len(gates) != len(expected) or {g["receipt"]["profile"] for g in gates} != expected:
        raise ValueError(f"required gate profiles: {sorted(expected)}")
    hashes = {name: digest(Path(args.dist) / name) for name in ASSETS + ["SHA256SUMS"]}
    root = Path(args.output)
    root.mkdir()  # Never replace an earlier staged release.
    for gate_record in gates:
        profile = gate_record["receipt"]["profile"]
        logs = root / "gate-logs" / profile
        logs.mkdir(parents=True)
        for number, command in enumerate(gate_record["receipt"]["commands"]):
            shutil.copyfile(command["log"], logs / f"{number}.log")
    downloads = root / "downloads"
    target = downloads / args.tag
    target.mkdir(parents=True)
    for name in hashes:
        shutil.copyfile(Path(args.dist) / name, target / name)
    base = Path("internal/downloads/base.txt").read_text().strip()
    write_json(downloads / "latest.json", {
        "tag_name": args.tag, "prerelease": False, "draft": False,
        "assets": [{"name": name, "browser_download_url": f"{base}/{args.tag}/{name}"}
                   for name in hashes],
    })
    subprocess.run(["go", "run", "-p", "2", "./cmd/check-downloads", str(downloads)], check=True)
    if source(args.tag, args.dry_run) != head or any(digest(target / n) != h for n, h in hashes.items()):
        raise ValueError("source or staged assets changed during verification")
    write_json(root / "receipt.json", {"tag": args.tag, "head": head,
               "dry_run": args.dry_run, "assets": hashes, "gates": gates,
               "download_validator": "passed", "published": False,
               "hosted_homepage_after_promotion": "pending authorized promotion"})
    print(f"Verified {args.tag} at {head}: {root}/receipt.json; nothing published")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    check = sub.add_parser("gate")
    check.add_argument("profile", choices=GATES)
    check.add_argument("--tag", required=True)
    check.add_argument("--output", required=True)
    stage = sub.add_parser("package")
    stage.add_argument("--tag", required=True)
    stage.add_argument("--dist", required=True)
    stage.add_argument("--output", required=True)
    stage.add_argument("--gate", action="append", required=True)
    stage.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    try:
        (gate if args.action == "gate" else package)(args)
    except (ValueError, OSError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Release verification refused: {error}\n")


if __name__ == "__main__":
    main()
