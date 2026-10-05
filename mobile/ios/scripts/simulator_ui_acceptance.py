#!/usr/bin/env python3
"""Run read-only native UI acceptance on an explicit dedicated simulator.

The selected generated xctestrun is copied, not overwritten. Only non-secret
loopback preview metadata is supplied. No accounts, grants, provider processes,
signing identities, browser profiles, or shared simulator settings are changed.
"""
import argparse
import datetime
import json
import os
import pathlib
import plistlib
import subprocess
import sys
import urllib.parse
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("device", "derived-data", "results", "origin", "user", "wing", "key", "root", "child"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    uuid.UUID(args.device)
    origin = urllib.parse.urlsplit(args.origin)
    if (origin.scheme != "http" or origin.hostname != "127.0.0.1" or
            origin.username is not None or origin.password is not None or
            origin.path not in ("", "/") or origin.query or origin.fragment or not origin.port):
        parser.error("Choose an exact HTTP 127.0.0.1 preview origin with a port and no credentials")
    pins = {field.upper(): getattr(args, field) for field in ("origin", "user", "wing", "key", "root", "child")}
    if any(not value or len(value.encode()) > 512 or any(ord(c) < 32 for c in value) for value in pins.values()):
        parser.error("All exact non-secret preview identities are required")
    if args.child == args.root:
        parser.error("Choose a distinct child belonging to the selected parent")
    developer = os.environ.get("DEVELOPER_DIR", "")
    if not developer or not pathlib.Path(developer, "usr/bin/xcodebuild").is_file():
        parser.error("DEVELOPER_DIR must identify the installed full Xcode")
    available = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "available", "--json"]))
    if not any(device.get("udid") == args.device for group in available["devices"].values() for device in group):
        parser.error("The explicit dedicated simulator must already exist and be available")
    products = pathlib.Path(args.derived_data).resolve() / "Build/Products"
    selected_spec = products / "Wingthing-read-only-acceptance.xctestrun"
    candidates = sorted(path for path in products.glob("*.xctestrun") if path.name not in (
        selected_spec.name, "Wingthing-local-state-acceptance.xctestrun", "Wingthing-first-connection-acceptance.xctestrun", "Wingthing-fixture-acceptance.xctestrun", "Wingthing-visual-acceptance.xctestrun", "Wingthing-creation-acceptance.xctestrun"))
    if len(candidates) != 1:
        parser.error("Expected exactly one fresh generated xctestrun in the selected derived data")
    spec = plistlib.loads(candidates[0].read_bytes())
    if spec.get("__xctestrun_metadata__", {}).get("FormatVersion") == 1:
        targets = [value for name, value in spec.items() if name != "__xctestrun_metadata__"]
    else:
        targets = [target for configuration in spec.get("TestConfigurations", []) for target in configuration.get("TestTargets", [])]
    if len(targets) != 1 or targets[0].get("BlueprintName") != "WingthingUIAcceptance":
        parser.error("Refusing an unexpected UI test target set")
    target = targets[0]
    target["OnlyTestIdentifiers"] = ["NativeInspectionTests"]
    target.setdefault("EnvironmentVariables", {}).update({"WT_IOS_QA_" + field: value for field, value in pins.items()})
    target["EnvironmentVariables"]["WT_IOS_QA_CONTENT_SIZE"] = "accessibility-extra-extra-extra-large"
    results = pathlib.Path(args.results).resolve()
    results.mkdir(parents=True, exist_ok=False)
    # __TESTROOT__ resolves from the specification's directory. Keep the copy
    # beside its generated build products so app/runner paths remain exact.
    selected_spec.write_bytes(plistlib.dumps(spec))
    command = ["xcodebuild", "-jobs", "2", "test-without-building", "-xctestrun", str(selected_spec),
               "-destination", "platform=iOS Simulator,id=" + args.device,
               "-parallel-testing-enabled", "NO", "-resultBundlePath", str(results / "native.xcresult")]
    receipt = {"startedAtUTC": datetime.datetime.now(datetime.timezone.utc).isoformat(),
               "device": args.device, "preview": pins, "generatedSpecification": str(candidates[0]),
               "selectedSpecification": str(selected_spec), "resultBundle": str(results / "native.xcresult"),
               "readOnly": True, "modelCalls": 0, "credentialOrSigningChanges": 0}
    ui_command = ["xcrun", "simctl", "ui", args.device, "content_size"]
    prior_size = subprocess.check_output(ui_command, text=True).strip()
    if prior_size in ("unknown", "unsupported"):
        parser.error("The dedicated simulator must support a readable/restorable text-size preference")
    receipt["priorContentSize"] = prior_size
    receipt["acceptanceContentSize"] = "accessibility-extra-extra-extra-large"
    try:
        subprocess.run(ui_command + [receipt["acceptanceContentSize"]], check=True)
        with (results / "xcodebuild.log").open("w") as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=600)
            receipt["exitCode"] = result.returncode
    except subprocess.TimeoutExpired:
        receipt["exitCode"] = 124
        receipt["blocker"] = "Dedicated simulator UI acceptance exceeded its 600-second bound"
    finally:
        subprocess.run(ui_command + [prior_size], check=True)
        receipt["restoredContentSize"] = subprocess.check_output(ui_command, text=True).strip()
        if receipt["restoredContentSize"] != prior_size:
            receipt["exitCode"] = 1
            receipt["blocker"] = "Dedicated simulator text-size restoration did not match its prior preference"
    receipt["finishedAtUTC"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    (results / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({"exitCode": receipt["exitCode"], "receipt": str(results / "receipt.json")}))
    return receipt["exitCode"]


if __name__ == "__main__":
    sys.exit(main())
