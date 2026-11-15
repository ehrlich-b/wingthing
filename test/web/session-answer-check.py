#!/usr/bin/env python3
"""Report session reader source provenance from a standalone checkout."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', type=Path, help='Optional output file; otherwise print JSON')
args = parser.parse_args()
repo = Path(__file__).resolve().parents[2]
files = [
    'internal/egg/session_prompt.go', 'internal/egg/session_answer_bridge_test.go',
    'internal/localmcp/session_lifecycle.go', 'web/src/conversation-recovery.js',
    'web/src/conversation-recovery-store.js', 'web/src/conversation-recovery.test.js',
    'web/src/shell/views/session.js', 'test/web/look.mjs',
    'test/web/session-states.mjs', 'test/web/stable.mjs',
]
report = {
    'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(),
    'source_hashes': {name: hashlib.sha256((repo / name).read_bytes()).hexdigest() for name in files},
}
text = json.dumps(report, indent=2) + '\n'
if args.output:
    args.output.write_text(text)
print(text, end='')
