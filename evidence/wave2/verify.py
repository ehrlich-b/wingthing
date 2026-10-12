#!/usr/bin/env python3
"""Check unchanged acceptance, complete three-run outcomes and required gates."""
import collections
import hashlib
import json
import re
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent


def outcomes(name, include_subtests=False):
    result = collections.defaultdict(list)
    for line in (HERE / name).read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        test = event.get("Test", "")
        action = event.get("Action")
        if test.startswith("TestStoryboard") and (include_subtests or "/" not in test) and action in ("pass", "fail"):
            result[test].append(action)
    return dict(result)


before = outcomes("before.jsonl")
after = outcomes("final-storyboard-3x.jsonl")
assert len(before) == len(after) == 51, "missing card verdicts"
assert all(len(v) == 1 for v in before.values()), "incomplete baseline"
assert all(len(v) == 3 and len(set(v)) == 1 for v in after.values()), "missing runs or flakiness"
regressions = [test for test, old in before.items() if old == ["pass"] and after[test] != ["pass"] * 3]
assert not regressions, regressions
before_all = outcomes("before.jsonl", include_subtests=True)
after_all = outcomes("final-storyboard-3x.jsonl", include_subtests=True)
subtest_regressions = [test for test, old in before_all.items() if old == ["pass"] and after_all.get(test) != ["pass"] * 3]
assert not subtest_regressions, subtest_regressions
fixed = [test for test, old in before.items() if old == ["fail"] and after[test] == ["pass"] * 3]
failures = [test for test, new in after.items() if new == ["fail"] * 3]
sources = json.loads((HERE / "acceptance-source.json").read_text())
for entry in sources["original_tests"]:
    current = (ROOT / entry["path"]).read_bytes()
    original = subprocess.check_output(["git", "show", "580d4d9:" + entry["path"]], cwd=ROOT)
    assert current == original
    assert hashlib.sha256(current).hexdigest() == entry["sha256"]
for gate in ("final-vet", "final-race", "final-focused-3x", "phone-policy-cli-final-3x", "web-build"):
    assert (HERE / (gate + ".exit")).read_text().strip() == "0", gate
assert (HERE / "final-storyboard-3x.exit").read_text().strip() == "1", "expected retained failures"

summary = {
    "gated_head": (HERE / "gated-head.txt").read_text().strip(),
    "before": dict(collections.Counter(v[0] for v in before.values())),
    "after_each_of_three_runs": dict(collections.Counter(v[0] for v in after.values())),
    "fixed": fixed,
    "regressions": regressions,
    "subtest_regressions": subtest_regressions,
    "flaky_cards": [],
    "remaining_failures": failures,
    "verdicts": after,
    "original_acceptance_files_unchanged": len(sources["original_tests"]),
    "native_macos_and_physical_ios": "REQUIRED-UNRUN",
    "go_flags": "-p=4",
    "nice": 15,
    "binary_sha256": hashlib.sha256((ROOT / ".scratch/storyboard-wt").read_bytes()).hexdigest(),
}
(HERE / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")

for log in ("before.jsonl", "final-storyboard-3x.jsonl", "phone-policy-cli-final-3x.jsonl"):
    for match in re.finditer(r"PTY (?:screen )?transcript: ([^\\\n\"]+)", (HERE / log).read_text()):
        path = Path(match.group(1))
        if path.is_file() and ROOT / ".scratch" in path.parents:
            relative = path.relative_to(ROOT / ".scratch")
            target = HERE / "transcripts" / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
            ansi = path.with_suffix(".ansi")
            if ansi.is_file():
                shutil.copy2(ansi, target.with_suffix(".ansi"))
print(json.dumps({key: summary[key] for key in ("before", "after_each_of_three_runs", "fixed", "regressions", "flaky_cards")}, indent=2))
