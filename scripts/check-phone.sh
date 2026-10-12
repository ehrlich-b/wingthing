#!/bin/sh
# Sandbox-safe gates; real listeners/Chromium/device acceptance run separately.
set -eu
test -f go.mod && test -f web/package.json || { echo 'Run from the clone root.' >&2; exit 2; }
task_root=$PWD
phone_go=${WT_PHONE_GO:-go}
mkdir -p .scratch/go-build .scratch/gomod .scratch/npm .scratch/bin
export GOTOOLCHAIN=local GOMAXPROCS=2 GOCACHE="$task_root/.scratch/go-build" GOMODCACHE="$task_root/.scratch/gomod"
export GOPROXY=${GOPROXY:-off} GOSUMDB=${GOSUMDB:-off}
(cd web && npm ci --ignore-scripts --no-audit --no-fund --cache "$task_root/.scratch/npm" && npm test && npm run build)
"$phone_go" test -p 2 -race ./internal/phoneweb
"$phone_go" test -p 2 ./cmd/wt -run '^TestPhoneServeRejectsUnsafeConfiguration$'
"$phone_go" build -p 2 -buildvcs=false -o .scratch/bin/wt-phone ./cmd/wt
.scratch/bin/wt-phone phone serve --help >/dev/null
git diff --check
