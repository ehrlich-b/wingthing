#!/bin/bash
# Run from the repository root with dependencies available in the environment.
# Optional WT_NAVIGATOR_EVIDENCE and WT_CONTEXT_EVIDENCE keep PTY and render output.
set -euo pipefail
case "${1:-}" in
  fixtures)
    go test -p 2 ./internal/config ./cmd/wt -run '^(TestSetMachineContextFixtures|TestMachineContextMissingAndMalformed|TestContextCLI|TestDashboardNoTTYHelpUnchanged)$' -count=1 -v
    ;;
  pty)
    go test -p 2 ./cmd/wt ./internal/dashboard -run '^(TestContextSetBuiltPTY|TestContextHintAndEvidence|TestContextViewportSelectionAndEvidence|TestDashboardScrollKeepsSelectionVisible)$' -count=1 -v
    ;;
  packages)
    python3 - <<'DOC'
from pathlib import Path
text = Path('docs/daily-driver.md').read_text()
for term in ['wt context set local home', 'wt context set studio work',
             'wt context list', 'wt context set studio none']:
    assert term in text, term
print('daily-driver command documentation: PASS')
DOC
    go vet -p 2 ./...
    go test -p 2 ./cmd/wt/... ./internal/dashboard/... ./internal/config/...
    git diff --check 0120e7bf81feb0dcd233c790d64886dd4a26ca5f HEAD
    ;;
  *)
    echo 'Usage: bash test/context-acceptance.sh fixtures|pty|packages' >&2
    exit 2
    ;;
esac
