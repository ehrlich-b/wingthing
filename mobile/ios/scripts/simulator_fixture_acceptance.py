#!/usr/bin/env python3
"""Run native Send/replay/Stop against the DEBUG in-process encrypted fixture.

No home address, bearer, provider, listener, credential or signing change is
accepted. Only the explicit dedicated simulator's temporary font size changes.
"""
import argparse
import datetime
import json
import os
import pathlib
import plistlib
import subprocess
import sys
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("device", "derived-data", "results"):
        parser.add_argument("--" + field, required=True)
    parser.add_argument("--suite", choices=("interactive", "visual", "persistence", "first-connection", "creation"), default="interactive")
    parser.add_argument("--appearance", choices=("light", "dark"))
    args = parser.parse_args()
    uuid.UUID(args.device)
    developer = os.environ.get("DEVELOPER_DIR", "")
    if not developer or not pathlib.Path(developer, "usr/bin/xcodebuild").is_file():
        parser.error("DEVELOPER_DIR must name the existing full Xcode")
    devices = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "available", "--json"]))
    if not any(d.get("udid") == args.device for group in devices["devices"].values() for d in group):
        parser.error("An explicit existing available dedicated simulator is required")
    products = pathlib.Path(args.derived_data).resolve() / "Build/Products"
    candidates = sorted(p for p in products.glob("*.xctestrun") if p.name not in (
        "Wingthing-fixture-acceptance.xctestrun", "Wingthing-visual-acceptance.xctestrun", "Wingthing-local-state-acceptance.xctestrun", "Wingthing-read-only-acceptance.xctestrun", "Wingthing-first-connection-acceptance.xctestrun", "Wingthing-creation-acceptance.xctestrun"))
    if len(candidates) != 1:
        parser.error("Expected one generated test specification in the selected build products")
    spec = plistlib.loads(candidates[0].read_bytes())
    if spec.get("__xctestrun_metadata__", {}).get("FormatVersion") == 1:
        targets = [v for k, v in spec.items() if k != "__xctestrun_metadata__"]
    else:
        targets = [t for c in spec.get("TestConfigurations", []) for t in c.get("TestTargets", [])]
    if len(targets) != 1 or targets[0].get("BlueprintName") != "WingthingUIAcceptance":
        parser.error("Refusing an unexpected test target set")
    target = targets[0]
    environment = target.setdefault("EnvironmentVariables", {})
    if any(k.startswith("WT_IOS_PREVIEW_") or k.startswith("WT_IOS_QA_") for k in environment):
        parser.error("Fixture specification may not inherit a real preview profile")
    content_size = "large" if args.suite in ("visual", "persistence") else "accessibility-extra-extra-extra-large"
    environment["WT_IOS_QA_CONTENT_SIZE"] = content_size
    environment["WT_IOS_QA_APPEARANCE"] = args.appearance or "device"
    target["OnlyTestIdentifiers"] = [{"visual": "NativeVisualTargetTests", "persistence": "NativeLocalStateTests", "interactive": "NativeInteractiveTests", "first-connection": "NativeFreshHomeTests", "creation": "NativeConversationCreationTests"}[args.suite]]
    if args.suite == "creation": target["OnlyTestIdentifiers"].append("NativeFreshHomeTests")
    selected = products / ({"visual": "Wingthing-visual-acceptance.xctestrun", "persistence": "Wingthing-local-state-acceptance.xctestrun", "interactive": "Wingthing-fixture-acceptance.xctestrun", "first-connection": "Wingthing-first-connection-acceptance.xctestrun", "creation": "Wingthing-creation-acceptance.xctestrun"}[args.suite])
    results = pathlib.Path(args.results).resolve()
    results.mkdir(parents=True, exist_ok=False)
    selected.write_bytes(plistlib.dumps(spec))
    command = ["xcodebuild", "-jobs", "2", "test-without-building", "-xctestrun", str(selected),
        "-destination", "platform=iOS Simulator,id=" + args.device, "-parallel-testing-enabled", "NO",
        "-resultBundlePath", str(results / "native.xcresult")]
    receipt = {"startedAtUTC": datetime.datetime.now(datetime.timezone.utc).isoformat(), "device": args.device,
        "selectedSpecification": str(selected), "generatedSpecification": str(candidates[0]),
        "resultBundle": str(results / "native.xcresult"), "transport": "in-process encrypted synthetic fixture",
        "networkOrProviderCalls": 0, "realTaskMutations": 0, "credentialOrSigningChanges": 0,
        "acceptanceContentSize": content_size, "suite": args.suite, "appearance": args.appearance or "device"}
    ui = ["xcrun", "simctl", "ui", args.device, "content_size"]
    prior = subprocess.check_output(ui, text=True).strip()
    if prior in ("unknown", "unsupported"):
        parser.error("Dedicated simulator needs a readable/restorable text-size setting")
    receipt["priorContentSize"] = prior
    appearance = ["xcrun", "simctl", "ui", args.device, "appearance"]
    prior_appearance = subprocess.check_output(appearance, text=True).strip() if args.appearance else None
    if args.appearance and prior_appearance not in ("light", "dark"):
        parser.error("Dedicated simulator appearance must be readable/restorable")
    receipt["priorAppearance"] = prior_appearance
    try:
        if args.appearance: subprocess.run(appearance + [args.appearance], check=True)
        subprocess.run(ui + [receipt["acceptanceContentSize"]], check=True)
        with (results / "xcodebuild.log").open("w") as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=600)
        receipt["exitCode"] = result.returncode
    except subprocess.TimeoutExpired:
        receipt["exitCode"] = 124
        receipt["blocker"] = "Fixture UI acceptance exceeded 600 seconds"
    finally:
        if prior_appearance:
            subprocess.run(appearance + [prior_appearance], check=True)
            receipt["restoredAppearance"] = subprocess.check_output(appearance, text=True).strip()
            if receipt["restoredAppearance"] != prior_appearance:
                receipt["exitCode"] = 1
                receipt["blocker"] = "Dedicated simulator appearance did not restore"
        subprocess.run(ui + [prior], check=True)
        receipt["restoredContentSize"] = subprocess.check_output(ui, text=True).strip()
        if receipt["restoredContentSize"] != prior:
            receipt["exitCode"] = 1
            receipt["blocker"] = "Dedicated simulator text size did not restore"
        receipt["finishedAtUTC"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (results / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({"exitCode": receipt["exitCode"], "receipt": str(results / "receipt.json")}))
    return receipt["exitCode"]


if __name__ == "__main__":
    sys.exit(main())
