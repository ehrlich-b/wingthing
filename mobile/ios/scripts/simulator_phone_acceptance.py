#!/usr/bin/env python3
"""Drive native phone storyboard tests using disposable real Go PAKE wings."""
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
    for field in ("device", "derived-data", "results", "peer"):
        parser.add_argument("--" + field, required=True)
    parser.add_argument("--only-test")
    args = parser.parse_args()
    uuid.UUID(args.device)
    developer = pathlib.Path(os.environ.get("DEVELOPER_DIR", ""))
    if not (developer / "usr/bin/xcodebuild").is_file():
        parser.error("Select the existing full Xcode with DEVELOPER_DIR")
    devices = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "available", "--json"]))
    selected = [d for runtime, group in devices["devices"].items() for d in group if d["udid"] == args.device and runtime.endswith("iOS-26-3") and d["deviceTypeIdentifier"].endswith("iPhone-17-Pro")]
    if len(selected) != 1:
        parser.error("An explicit iPhone 17 Pro / iOS 26.3 simulator is required")
    products = pathlib.Path(args.derived_data).resolve() / "Build/Products"
    candidates = [p for p in products.glob("*.xctestrun") if "acceptance" not in p.name]
    if len(candidates) != 1:
        parser.error("Expected one generated test specification")
    spec = plistlib.loads(candidates[0].read_bytes())
    if spec.get("__xctestrun_metadata__", {}).get("FormatVersion") == 1:
        targets = [v for k, v in spec.items() if k != "__xctestrun_metadata__"]
    else:
        targets = [t for c in spec.get("TestConfigurations", []) for t in c.get("TestTargets", [])]
    if len(targets) != 1 or targets[0].get("BlueprintName") != "WingthingUIAcceptance":
        parser.error("Unexpected target set")
    target = targets[0]
    results = pathlib.Path(args.results).resolve(); results.mkdir(parents=True, exist_ok=False)
    peer = subprocess.Popen([str(pathlib.Path(args.peer).resolve())], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env={**os.environ, "WT_PAIRING_UI_FIXTURE": "1"})
    receipt = {"startedAtUTC": datetime.datetime.now(datetime.timezone.utc).isoformat(), "device": args.device, "transport": "real Go SPAKE2/device-proof loopback fixture; synthetic session handler", "physicalPhone": False, "productionMutations": 0, "providerCalls": 0, "signingChanges": 0}
    content = ["xcrun", "simctl", "ui", args.device, "content_size"]
    appearance = ["xcrun", "simctl", "ui", args.device, "appearance"]
    prior_size = subprocess.check_output(content, text=True).strip()
    prior_appearance = subprocess.check_output(appearance, text=True).strip()
    try:
        bootstrap = json.loads(peer.stdout.readline())
        # Generated one-time fixture values live only in the disposable test spec.
        environment = target.setdefault("EnvironmentVariables", {})
        if any(k.startswith("WT_IOS_PREVIEW_") for k in environment):
            raise ValueError("Refusing a real preview profile")
        environment["WT_IOS_PHONE_TEST_CONTROL"] = bootstrap["control"]
        environment["WT_IOS_PHONE_TEST_TOKEN"] = bootstrap["token"]
        target["OnlyTestIdentifiers"] = ["NativePhoneStoryboardTests" + ("/" + args.only_test if args.only_test else "")]
        generated = products / "Wingthing-phone-acceptance.xctestrun"
        generated.write_bytes(plistlib.dumps(spec))
        subprocess.run(content + ["large"], check=True); subprocess.run(appearance + ["dark"], check=True)
        command = ["xcodebuild", "-jobs", "2", "test-without-building", "-xctestrun", str(generated), "-destination", "platform=iOS Simulator,id=" + args.device, "-parallel-testing-enabled", "NO", "-resultBundlePath", str(results / "native.xcresult")]
        with (results / "xcodebuild.log").open("w") as log:
            receipt["exitCode"] = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=900).returncode
        subprocess.run(["xcrun", "xcresulttool", "export", "attachments", "--path", str(results / "native.xcresult"), "--output-path", str(results / "attachments")], check=True)
    except subprocess.TimeoutExpired:
        receipt["exitCode"] = 124; receipt["blocker"] = "Native phone acceptance exceeded 900 seconds"
    finally:
        if peer.poll() is None:
            peer.stdin.write("stop\n"); peer.stdin.flush()
            try: peer.wait(timeout=10)
            except subprocess.TimeoutExpired: peer.terminate(); peer.wait(timeout=10)
        subprocess.run(content + [prior_size], check=True); subprocess.run(appearance + [prior_appearance], check=True)
        receipt["finishedAtUTC"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        receipt["restoredContentSize"] = subprocess.check_output(content, text=True).strip()
        receipt["restoredAppearance"] = subprocess.check_output(appearance, text=True).strip()
        if receipt["restoredContentSize"] != prior_size or receipt["restoredAppearance"] != prior_appearance:
            receipt["exitCode"] = 1
        (results / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({"exitCode": receipt["exitCode"], "receipt": str(results / "receipt.json")}))
    return receipt["exitCode"]

if __name__ == "__main__":
    sys.exit(main())
