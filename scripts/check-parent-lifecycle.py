#!/usr/bin/env python3
"""Validate a finite Go JSON gate and bind its local source/binary evidence."""

import argparse
import hashlib
import json
import platform
from pathlib import Path

PROTOCOL_TESTS = (
    "TestParentLifecycleFixtureSocketPaths",
    "TestParentLifecycleScopedFixtureProtection",
    "TestParentLifecycleAdmissionDisconnectBoundaries",
    "TestParentLifecycleAdmissionDisconnectBoundaries/before_admission",
    "TestParentLifecycleAdmissionDisconnectBoundaries/after_admission_before_ack",
    "TestParentLifecycleAdmissionDisconnectBoundaries/after_ack",
    "TestParentLifecycleTwoOriginalParentsKeepOrdinaryMessagesScoped",
    "TestParentLifecycleMailboxReadinessLoss",
    "TestParentLifecycleExitedCodexAndDelayedResult",
    "TestParentLifecycleStopJoinsDisposableChild",
    "TestAttachStandaloneEggRetainsDaemonlessContract/stable",
    "TestAttachStandaloneEggRetainsDaemonlessContract/preview",
)
BUILT_TESTS = (
    "TestBuiltWTStdioWingEggFakeCodexResult",
    "TestBuiltWTStdioWingEggFakeCodexTaskResult",
    "TestBuiltWTStdioWingEggFakeCodexUnboundedResult",
    "TestBuiltWTLocalOnlyStdioWingEggFakeCodexResult",
)
NATIVE_TESTS = tuple(
    name + "/parent_exit=" + mode
    for name in (
        "TestBuiltClientScopedParentFakeProvidersSurviveDisconnect",
        "TestBuiltWTClaudeScopedMailboxTwoLocalOnlyWings",
    )
    for mode in ("false", "true")
) + ("TestUnboundedStopKillsProcessGroupAndReportsSurvivors",)


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def validate(log, packages, required=()):
    tests, results, output, problems = {}, {}, [], []
    for line in log.read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            problems.append("non-JSON gate output: " + line)
            continue
        package, test, action = event.get("Package"), event.get("Test"), event.get("Action")
        if event.get("Output"):
            output.append(event["Output"])
        if test and action in ("run", "pass", "skip", "fail"):
            tests[(package, test)] = action
        if action == "skip":
            problems.append(f"gate skipped: {package}/{test or '(package)'}")
        if package and not test and action in ("pass", "skip", "fail"):
            results[package] = action
    for package in packages:
        full = "github.com/ehrlich-b/wingthing/" + package.removeprefix("./")
        if results.get(full) != "pass":
            problems.append(f"package did not pass: {package}")
        if not any(p == full and status == "pass" for (p, _), status in tests.items()):
            problems.append(f"package ran no passing tests: {package}")
    for (package, test), status in tests.items():
        if status != "pass":
            problems.append(f"test did not pass: {package}/{test}: {status}")
    if not tests:
        problems.append("gate ran zero tests")
    for name in required:
        if not any(test == name and status == "pass" for (_, test), status in tests.items()):
            problems.append(f"required test did not pass: {name}")
    return tests, results, output, problems


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log", type=Path)
    parser.add_argument("--packages", nargs="+", required=True)
    parser.add_argument("--profile", choices=("protocol", "protocol-race", "built", "offline"), required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--diff-sha", required=True)
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--go-version", required=True)
    parser.add_argument("--gate-exit", type=int, default=0)
    args = parser.parse_args()
    required = BUILT_TESTS if args.profile == "built" else PROTOCOL_TESTS
    if args.profile == "offline":
        required += BUILT_TESTS + NATIVE_TESTS
    tests, results, output, problems = validate(args.log, args.packages, required)
    if args.gate_exit:
        problems.append(f"gate command or source stability check failed: exit {args.gate_exit}")
    receipt = {
        "source_head": args.source,
        "tracked_diff_sha256": args.diff_sha,
        "platform": platform.platform(),
        "go_version": args.go_version,
        "profile": args.profile,
        "log": str(args.log),
        "log_sha256": digest(args.log),
        "passed_tests_including_subtests": sum(value == "pass" for value in tests.values()),
        "total_tests_including_subtests": len(tests),
        "package_results": results,
        "status": "FAIL" if problems else "PASS",
        "problems": problems,
    }
    if args.binary:
        receipt["binary"] = str(args.binary)
        receipt["binary_sha256"] = digest(args.binary)
    destination = Path(str(args.log) + ".receipt.json")
    destination.write_text(json.dumps(receipt, indent=2) + "\n")
    if problems:
        print("".join(output), end="")
        print("\n".join(problems))
    print(f"{receipt['status']}: {receipt['passed_tests_including_subtests']}/{len(tests)} tests (including subtests); receipt: {destination}")
    return int(bool(problems))


if __name__ == "__main__":
    raise SystemExit(main())
