.PHONY: build test coverage check clean web serve release release-contract proto deploy deploy-edge scale status jail \
	build-linux build-mock-agent build-linux-tests build-linux-sandbox-tests test-linux test-linux-ubuntu test-integ test-e2e \
	build-linux-wt-tests \
	test-provider-swap build-web-e2e test-web test-vuln test-compat

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
.PHONY: android-contract android-vectors android-build android-check
android-contract: | web/dist
	go test ./test/androidcontract
android-vectors:
	go run ./android/contract/generate.go
android-build:
	$(MAKE) -C android build
android-check:
	$(MAKE) -C android check

PREVIEW_VERSION ?= v0.148.0-preview.$(shell date -u +%Y%m%d).g$(shell git rev-parse --short HEAD)
PREVIEW_LDFLAGS := -X main.version=$(PREVIEW_VERSION) -X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=preview
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# internal/relay embeds web/dist (web/embed.go), but the built assets are
# generated and gitignored, so a fresh clone has none and every Go build fails
# with "pattern dist: no matching files found". Seed a placeholder when it is
# missing; `make web` overwrites it with the real vite output.
web/dist:
	@mkdir -p $@
	@printf '%s\n' '<!doctype html><meta charset="utf-8"><title>wingthing</title>' \
		'<p>Web assets were not built. Run <code>make web</code>.' > $@/index.html

build: | web/dist
	go build -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o wt ./cmd/wt

.PHONY: build-preview build-preview-linux preview-package test-preview test-preview-unit test-preview-reentry test-preview-provider test-preview-context test-socket-path
build-preview: | web/dist
	go build -buildvcs=false -ldflags "$(PREVIEW_LDFLAGS)" -o wt-preview ./cmd/wt

build-preview-linux: | web/dist
	@mkdir -p dist-preview
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -buildvcs=false -ldflags "$(PREVIEW_LDFLAGS)" -o dist-preview/wt-preview-linux-$(LINUX_ARCH) ./cmd/wt
	@cd dist-preview && shasum -a 256 wt-preview-* > SHA256SUMS

# Deliberately local artifacts. No install, tag, upload, or deployment occurs.
preview-package: build-preview
	@mkdir -p dist-preview
	cp wt-preview dist-preview/wt-preview-$(shell go env GOOS)-$(shell go env GOARCH)
	cp scripts/preview-package.sh dist-preview/preview-package.sh
	@cd dist-preview && shasum -a 256 wt-preview-* > SHA256SUMS
	./wt-preview channel --json > dist-preview/channel.json

# Black-box state/process/install/upgrade/uninstall isolation proof.
test-preview-unit: | web/dist
	go test ./internal/config ./cmd/wt ./internal/egg ./internal/relay -run "TestPreview" -count=1

# Temporary fixture bindings and fake vendors only; never reads a real provider home.
test-preview-provider-binding: | web/dist
	go test ./internal/config ./cmd/wt -run 'TestPreviewProviderBinding' -count=1

# Synthetic temp states and malformed pid files only: never signals or spawns a daemon.
.PHONY: test-preview-lifecycle-binding
test-preview-lifecycle-binding: GO ?= go
test-preview-lifecycle-binding: | web/dist
	$(GO) test ./cmd/wt -run 'TestPreviewLifecycleBinding|TestPreviewProviderBinding|TestDaemonLifecycleLock|TestReadPidFrom|TestWingStatus|TestWriteDaemonMetadata' -count=1

# Temp spools and fixture hook payloads only; no provider, PTY, or network.
.PHONY: test-lifecycle-hook-order
test-lifecycle-hook-order: GO ?= go
test-lifecycle-hook-order:
	$(GO) test ./internal/egg -run 'TestLifecycle|TestClaudeLifecycleArgs' -count=1
	$(GO) test -tags e2e ./test/integ -run 'TestSessionNativeLifecycleProtocolReconnect|TestSessionPromptNativeTranscriptReceiptAfterLostConnection' -count=1

# Temporary fixture directories only; alias cases skip on case-sensitive volumes.
.PHONY: test-preview-provider-binding-case
test-preview-provider-binding-case: GO ?= go
test-preview-provider-binding-case:
	$(GO) test ./internal/config -run 'TestPreviewProviderBinding' -count=1 -v

# Fake vendor only: no real login, browser, credentials, model, or vendor network.
test-preview-provider: build-preview
	go test ./cmd/wt -run "TestPreviewProvider" -count=1
	python3 test/preview/provider_onboarding.py ./wt-preview

# Fake vendor final processes and synthetic selector/guard tests only.
PREVIEW_CONTEXT_BINARY ?= ./wt-preview
ifeq ($(PREVIEW_CONTEXT_BINARY),./wt-preview)
test-preview-context: build-preview
endif
test-preview-context: | web/dist
	go test ./internal/egg -run 'TestPreviewClaude(GuardRejectsSymlinkAliases|ContextRejectsAliasedForeignSelector)$$' -count=1
	python3 test/preview/mac_provider_context.py "$(PREVIEW_CONTEXT_BINARY)"

# Actual preview parent -> configured MCP -> linked child reservation. The
# existing macOS parent sandbox still denies the nested proxy bind, as expected.
test-preview-reentry: build-preview
	@test "$$(go env GOOS)" = darwin || { echo 'test-preview-reentry requires macOS for the existing nested-proxy denial'; exit 1; }
	node test/conversation/proof.mjs ./wt-preview --expect-nested-proxy-block

# Host mailbox transport for a sandboxed personal parent. The unit tier covers
# envelopes, the protected-state model, policy intersection, journal replay and
# tree-bound targets. The e2e tier runs the fake native-protocol provider in an
# unchanged Seatbelt parent: injected stdio -> host broker -> two child eggs.
# Fixture state lives under CONVERSATION_FIXTURE_ROOT (outside /tmp and every
# provider-writable root); no model, login or credential is used.
.PHONY: test-conversation-transport test-conversation-e2e test-conversation-broker-launch
test-conversation-transport: | web/dist
	go test ./internal/store -run 'TestConversation' -count=1
	go test ./cmd/wt -run 'TestConversation|TestBound|TestAutomaticParentMCP|TestPublicCoordinator|TestCoordinator|TestHostMailbox|TestProviderWrite' -count=1

# Broker launch contract only: optional browser-bridge omission, whole-state
# and controller protected targets, provider data home outside state, argv.
# In-process; no binary build, egg spawn or Seatbelt execution.
test-conversation-broker-launch: GO ?= go
test-conversation-broker-launch: | web/dist
	$(GO) test ./internal/egg -run 'TestInstallBrowserBridge|TestProtectedWriteTargets' -count=1
	$(GO) test ./cmd/wt -run 'TestHostMailbox|TestProviderWrite|TestProtectedWriteTargetArgs|TestEggRunWithoutProtectedTargets' -count=1

#
# CONVERSATION_BINARY=/abs/path/wt-preview runs that frozen preview binary
# as-is and skips build-preview; CONVERSATION_BINARY_SHA256 pins its bytes.
# Unset, the target builds ./wt-preview as before.
CONVERSATION_FIXTURE_ROOT ?= $(abspath ..)
CONVERSATION_BINARY ?=
CONVERSATION_BINARY_SHA256 ?=
ifeq ($(strip $(CONVERSATION_BINARY)),)
test-conversation-e2e: build-preview
endif
test-conversation-e2e:
	@test "$$(uname -s)" = Darwin || { echo 'test-conversation-e2e requires the macOS Seatbelt parent sandbox'; exit 1; }
	node test/conversation/proof.mjs "$(or $(strip $(CONVERSATION_BINARY)),./wt-preview)" --state-root "$(CONVERSATION_FIXTURE_ROOT)"$(if $(strip $(CONVERSATION_BINARY_SHA256)), --expect-sha256 "$(strip $(CONVERSATION_BINARY_SHA256))")

test-socket-path: | web/dist
	go test ./cmd/wt ./internal/egg -run "TestSocketPath" -count=1

# Static/in-process checks of the protected-write-target policy contract. Seatbelt
# profiles are built and inspected, never executed.
.PHONY: test-protected-write-targets
test-protected-write-targets: | web/dist
	go test ./internal/sandbox ./internal/egg ./cmd/wt -run 'ProtectedWriteTarget|ProtectedTargets|CheckedProfile|RefusesProtectedWriteTargets|RefusesUnenforceableProtected' -count=1

test-preview: build build-preview preview-package
	python3 test/preview/channel_isolation.py ./wt ./wt-preview ./dist-preview

test: | web/dist
	go test ./...

# Pin the scanner for reproducible parsing while intentionally consulting the
# current Go vulnerability database. This is a promotion gate, not part of the
# offline `make check` path.
GOVULNCHECK_VERSION ?= v1.7.0
test-vuln: | web/dist
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	cd web && npm audit --audit-level=high

COVERAGE_OUT ?= /tmp/wingthing-coverage.out
coverage: | web/dist
	go test -coverprofile=$(COVERAGE_OUT) ./...
	go tool cover -func=$(COVERAGE_OUT)

check: web test build

# Fake-only conversation reader/recovery/parent-dot tests. No npm install,
# browser, tunnel, provider, model or network; `make web` runs these too.
# DOM-bound view modules cannot be imported by node:test; parse them instead.
.PHONY: test-conversation-ui
test-conversation-ui:
	cd web && for f in src/chat-view.js src/conversation-view.js src/parent-dot.js; do node --check $$f || exit 1; done
	cd web && node --test src/conversation-recovery.test.js test/conversation-state.test.js test/conversation-response.test.js test/parent-dot.test.js

web:
	cd web && npm ci && npm test && npm run build

serve: build
	./wt serve

release: web
	@echo "Building $(VERSION) for all platforms..."
	@mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		output="dist/wt-$$os-$$arch"; \
		tmp="$$output.tmp"; \
		echo "  $$os/$$arch"; \
		rm -f "$$tmp"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -buildvcs=false -ldflags="$(LDFLAGS)" -o "$$tmp" ./cmd/wt; \
		mv "$$tmp" "$$output"; \
	done
	@CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o dist/wt-contract ./cmd/wt
	@scripts/check-release-contract.sh dist/wt-contract
	@rm -f dist/wt-contract
	@cd dist && set -e; assets="wt-linux-amd64 wt-linux-arm64 wt-darwin-amd64 wt-darwin-arm64"; if command -v sha256sum >/dev/null 2>&1; then sha256sum $$assets > SHA256SUMS.tmp; else shasum -a 256 $$assets > SHA256SUMS.tmp; fi; mv SHA256SUMS.tmp SHA256SUMS
	@echo "Built $(VERSION) -> dist/ (publish via gh release create)"

release-contract: build
	scripts/check-release-contract.sh ./wt

jail: build
	go test -tags integration -v ./internal/sandbox/ -run TestJail

deploy: check release-contract test-compat
	fly deploy

# Add edge nodes to a region. Usage: make deploy-edge REGIONS=nrt,lhr COUNT=1
COUNT ?= 1
deploy-edge:
ifndef REGIONS
	$(error REGIONS is required. Example: make deploy-edge REGIONS=nrt,lhr)
endif
	@grep -Eq '^[[:space:]]*edge[[:space:]]*=' fly.toml || \
		{ echo 'edge process is disabled in fly.toml; follow docs/fly-ops.md before scaling' >&2; exit 1; }
	@awk 'BEGIN { section = 0; found = 0 } /^\[http_service\]$$/ { section = 1; next } /^\[/ { section = 0 } section && /^[[:space:]]*processes[[:space:]]*=/ && /"edge"/ { found = 1 } END { exit !found }' fly.toml || \
		{ echo 'edge is not attached to http_service.processes in fly.toml' >&2; exit 1; }
	fly scale count edge=$(COUNT) --region $(REGIONS) --yes

# Show all machines, regions, and process groups.
status:
	@echo "=== Machines ==="
	@fly machines list
	@echo ""
	@echo "=== Scale ==="
	@fly scale show

# Shorthand: make scale LOGIN=1 EDGE=3
LOGIN ?= 1
EDGE ?= 0
scale:
	fly scale count login=$(LOGIN) edge=$(EDGE) --yes

proto:
	protoc -I proto --go_out=paths=source_relative:internal/egg/pb --go-grpc_out=paths=source_relative:internal/egg/pb proto/egg.proto

# Deploy artifacts target the x86-64 shared hosts by default. Security tests use
# the Docker daemon's native architecture because qemu/Rosetta translate
# syscall numbers below seccomp and produce invalid sandbox results. The daemon
# may differ from the CLI host (for example an amd64 Colima VM on an arm64 Mac).
HOST_ARCH := $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
LINUX_ARCH ?= amd64
DOCKER_ARCH := $(shell arch=$$(docker info --format '{{.Architecture}}' 2>/dev/null); \
	if [ -n "$$arch" ]; then echo "$$arch" | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/'; \
	else echo $(HOST_ARCH); fi)
LINUX_TEST_ARCH ?= $(DOCKER_ARCH)

build-linux: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -buildvcs=false \
		-ldflags "-X main.version=test" -o test/linux/wt ./cmd/wt

build-mock-agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -o test/linux/mock-agent ./test/mock-agent/

build-linux-tests: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go test -c -tags 'e2e linux' \
		-o test/linux/run-tests ./test/linux/

build-linux-sandbox-tests: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go test -c -tags integration \
		-o test/linux/sandbox-tests ./internal/sandbox/

build-linux-wt-tests: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go test -c -tags integration \
		-o test/linux/wt-tests ./cmd/wt/

# Browser E2E tier: seeded shared-roost (org mode) + Playwright in Docker.
# Binaries must match the Docker daemon, which may differ from the client host
# (for example an arm64 Mac pointed at an amd64 Colima/remote daemon). Fall back
# to the host only when Docker is unavailable; test-web itself will then report
# the ordinary daemon error.
WEB_TEST_ARCH ?= $(DOCKER_ARCH)

build-web-e2e: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) go build -buildvcs=false \
		-ldflags "-X main.version=test" -o test/web/wt ./cmd/wt
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) go build -o test/web/claude ./test/web/canary-agent/

test-web: build-web-e2e
	test/web/run.sh

test-linux:
	@if [ "$(LINUX_TEST_ARCH)" != "$(DOCKER_ARCH)" ]; then \
		echo "cross-architecture seccomp tests are invalid (docker=$(DOCKER_ARCH), requested=$(LINUX_TEST_ARCH)); run this battery on a native $(LINUX_TEST_ARCH) Docker daemon"; \
		exit 1; \
	fi
	$(MAKE) LINUX_ARCH=$(LINUX_TEST_ARCH) build-linux build-mock-agent build-linux-tests build-linux-sandbox-tests build-linux-wt-tests
	docker build -t wt-test-linux -f test/linux/Dockerfile test/linux/
	docker run --rm --privileged wt-test-linux sh -lc \
		'/root/run-tests -test.v -test.timeout 120s && /root/sandbox-tests -test.v -test.timeout 120s && /root/wt-tests -test.v -test.timeout 120s'

test-linux-ubuntu:
	@if [ "$(LINUX_TEST_ARCH)" != "$(DOCKER_ARCH)" ]; then \
		echo "cross-architecture seccomp tests are invalid (docker=$(DOCKER_ARCH), requested=$(LINUX_TEST_ARCH)); run this battery on a native $(LINUX_TEST_ARCH) Docker daemon"; \
		exit 1; \
	fi
	$(MAKE) LINUX_ARCH=$(LINUX_TEST_ARCH) build-linux build-mock-agent build-linux-tests build-linux-sandbox-tests build-linux-wt-tests
	docker build -t wt-test-ubuntu -f test/linux/Dockerfile.ubuntu2404 test/linux/
	docker run --rm --privileged wt-test-ubuntu sh -lc \
		'/root/run-tests -test.v -test.timeout 120s && /root/sandbox-tests -test.v -test.timeout 120s && /root/wt-tests -test.v -test.timeout 120s'

test-integ: build build-preview | web/dist
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" go test -count=1 -tags e2e -v -timeout 120s ./test/integ/...

# Native protocol/parser fixtures only: no provider process, auth or model call.
.PHONY: test-codex-native
test-codex-native: | web/dist
	go test -count=1 -run '^TestCodexNative' ./internal/egg
	go test -count=1 -tags e2e -run '^TestCodexNative' -v ./test/integ

.PHONY: test-preview-input
test-preview-input: build-preview | web/dist
	WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" go test -count=1 -tags integration -v -timeout 60s ./cmd/wt -run '^TestPreviewBrowserLeaseOnRealEgg$$'

# Remote routing unit contracts: argv, --remote-state quoting, and guards.
.PHONY: test-remote-unit test-preview-remote
test-remote-unit: | web/dist
	go test ./cmd/wt -run '^Test(Remote|ParseRemote|ParseBareRemote|BareRemote|RunRemote|PreviewRemote|NewRootCommandFailsClosed)' -count=1

# Exact durable wake-target identity plus the existing wake contracts. Synthetic
# egg directories and an in-process transport only: no provider process,
# credential, network endpoint, permission reply, or process kill.
.PHONY: test-wake-exact
test-wake-exact: | web/dist
	go test ./cmd/wt -run '^Test(WakeExact|ConversationWake|OpusWakeRecovery)' -count=1

# Fake SSH transport only: real stable/preview receivers and sandboxed /bin/sh
# eggs. No host, credential, install, or unsandboxed fallback.
test-preview-remote: build build-preview
	python3 test/preview/remote_isolation.py ./wt ./wt-preview

# Owned local child only: the remote fixture's reader drain. No build.
.PHONY: test-preview-remote-drain
test-preview-remote-drain:
	python3 test/preview/remote_reader_drain_regression.py

.PHONY: test-claude-policy
test-claude-policy: | web/dist
	WT_REQUIRE_REAL_CLAUDE=1 go test -count=1 -tags integration -v -timeout 180s ./cmd/wt -run '^TestRealClaudeModelPolicyPreservesPersonalSettings$$'

# Black-box rolling-upgrade and rollback gate against the configured historical
# baselines v0.144.1 and v0.147.0 by default; WT_COMPAT_BASELINE_REF selects one
# override baseline. Requires the selected tags to be available in the local clone.
test-compat: | web/dist
	scripts/test-backward-compat.sh

test-e2e: test-linux test-linux-ubuntu test-integ

# Opt-in release gate for real, model-swapped harnesses. Requires the local
# Ollama/LiteLLM services and upstream CLIs documented in docs/release-e2e.md.
test-provider-swap:
	python3 test/live/provider_swap_smoke.py

clean:
	rm -f wt
	rm -rf dist/
	rm -f test/linux/wt test/linux/mock-agent test/linux/run-tests test/linux/sandbox-tests test/linux/wt-tests

# Temporary fixture directories and a fake identity hook for physical aliases.
.PHONY: test-preview-provider-binding-physical
test-preview-provider-binding-physical: GO ?= go
test-preview-provider-binding-physical:
	$(GO) test ./internal/config -run 'TestPreviewProviderBinding' -count=1 -v
