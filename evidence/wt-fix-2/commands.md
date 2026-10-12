# Validation commands

Run from `/work/lanes/wt-fix-2/wingthing` on Linux. All Go invocations use `GOFLAGS=-p=4` and `nice -n 15`, with clone-local `GOCACHE=$PWD/.scratch/northstar/go-build` and `GOMODCACHE=$PWD/.scratch/northstar/go-mod`. Existing baseline caches were copied with hardlinks; baseline evidence remains available under `evidence/run-{1,2,3}`. `make web/dist` supplied the supported Go-embed placeholder; this is not browser acceptance.

- `go test -json -count=3 -run '^TestStoryboard(NavigatorCard(2|5)|MachinesCard3)$' ./cmd/wt` → `cards-3x.jsonl` (exit 1 because of the documented 03/card-2 dispute).
- `go test -json -count=1 -timeout=10m -run '^TestStoryboard' ./cmd/wt` → `storyboard.jsonl`, `storyboard.exit`.
- `go test -json -count=1 -run '^TestDashboard' ./cmd/wt` → `cli-focused.jsonl`, `cli-focused.exit`.
- `go vet ./...` → `vet.log`, `vet.exit`.
- `go test -race -count=1 ./internal/dashboard ./internal/eggclient ./internal/config` → `focused.log`, `focused.exit`.

Acceptance tests, Northstar tests and fixture behavior remain byte-identical to baseline 580d4d9. Added package tests cover durable exact-route acknowledgement, concurrent observers, profile replacement, private cursor-file permissions, corruption reporting and offline action gating.

Native macOS terminal/provider behavior: **REQUIRED-UNRUN** on this Linux host. Linux kernel PTY fixtures exercise native terminal transport; they establish no physical macOS, vendor authentication, browser or phone acceptance.
