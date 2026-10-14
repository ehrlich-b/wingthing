.DEFAULT_GOAL := check
.PHONY: build-permission-spike test-permission-spike test-permission-spike-race

# Separately built finite hook/broker experiment; no wing control integration.
build-permission-spike:
	@mkdir -p .scratch
	$(GO) build -p 2 -o .scratch/permission-spike ./cmd/permission-spike

test-permission-spike:
	$(GO) test -p 2 -count=1 ./cmd/permission-spike ./internal/permissionbridge ./internal/egg -timeout=180s -v

test-permission-spike-race:
	$(GO) test -p 2 -race -count=1 ./internal/permissionbridge ./internal/egg -run 'PermissionBridge|PermissionSpike' -timeout=60s -v
.PHONY: browser-pake check-browser-pake test-pairing

# Build the browser PAKE asset from the vendored native BoringSSL sources.
browser-pake:
	@test -n "$(strip $(WASI_SDK_PATH))" || { echo 'Set WASI_SDK_PATH to the extracted wasi-sdk-25.0 directory' >&2; exit 1; }
	sh web/pake/build.sh

check-browser-pake: check-browser-pake-env
	@mkdir -p .scratch/browser-pake/home
	HOME="$(CURDIR)/.scratch/browser-pake/home" $(GO) build -p 2 -o .scratch/browser-pake/go-peer ./experiments/browser-pake/go-peer
	HOME="$(CURDIR)/.scratch/browser-pake/home" $(NODE) experiments/browser-pake/run.mjs

.PHONY: check-browser-pake-env
check-browser-pake-env:
	@test -n "$(strip $(CHROMIUM_PATH))" || { echo 'Set CHROMIUM_PATH to a local Chromium executable; browser checks are required' >&2; exit 1; }
	@$(MAKE) --no-print-directory browser-pake

test-pairing:
	$(GO) test -p 2 -count=1 ./internal/pake ./internal/phonepair

.PHONY: check gate test-parent-lifecycle-offline test-parent-lifecycle-protocol test-parent-lifecycle-protocol-race test-parent-lifecycle-built test-run-inspect test-run-inspect-race test-run-inspect-roundtrip test-run-recovery test-run-recovery-race test-run-load test-run-load-full test-run-canary e2e-linux e2e-web e2e-mac release deploy ops proto web serve clean

GO ?= nice -n 15 go
NODE ?= nice -n 15 node
NPM ?= nice -n 15 npm
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PREVIEW_VERSION ?= v0.148.0-preview.$(shell date -u +%Y%m%d).g$(shell git rev-parse --short HEAD)
PREVIEW_LDFLAGS := -X main.version=$(PREVIEW_VERSION) -X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=preview
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
CHANNEL ?= stable
GATE ?= integration compat static
GOVULNCHECK_VERSION ?= v1.7.0
COVERAGE_OUT ?= /tmp/wingthing-coverage.out
RACE_BASE ?= HEAD~1
# Override for release-wide coverage or a specific package list. Otherwise use
# Buildable untagged Go packages touched since RACE_BASE, including local files.
RACE_PACKAGES ?=
RUN_RECOVERY_PACKAGES ?= ./internal/wingsession ./internal/localmcp ./internal/wingconnect ./internal/egg
RUN_RECOVERY_TESTS ?= ^(TestRunRecovery.*|TestMCPTaskCancellationRecoversStopIntentAfterWingRestart|TestStopIntentAndQueuedCancellationSurviveWingRestart|TestLostRunSubmissionAcknowledgementDoesNotResendAfterRestart|TestRememberedPool(LostAdmissionReconcilesOriginalKey|UnreconciledAdmissionReportsOriginalKey)|TestProviderFailureContentAbsentFromLifecycleAndArchive)$$
PARENT_LIFECYCLE_ROOT := $(CURDIR)/.scratch/parent-lifecycle
PARENT_LIFECYCLE_PATH ?= /opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
PARENT_LIFECYCLE_PACKAGES := ./internal/testprovider ./internal/egg ./internal/localmcp ./cmd/wt
PARENT_LIFECYCLE_PROTOCOL_NAMES := TestParentLifecycle.*|TestHostMailbox.*|TestScopedParentLocalAndSSHChildrenSurviveReconnect|TestRunTurnSurvivesClientDisconnect|TestRunTurnRPCAuthenticatesEveryOperationAndStrictlyDecodes|TestRunTurnStopIsIdempotentAndDisarmsDeadline|TestCodexStopHookAloneNeverCompletesRun|TestCodexRunIDReplayAndConflictingInput|TestRunRecovery.*|TestLostRunSubmissionAcknowledgementDoesNotResendAfterRestart|TestSocketPath.*|TestAttachStandaloneEggRetainsDaemonlessContract
PARENT_LIFECYCLE_BUILT_NAMES := TestBuiltWTStdioWingEggFakeCodex(Result|TaskResult|UnboundedResult)|TestBuiltWTLocalOnlyStdioWingEggFakeCodexResult
PARENT_LIFECYCLE_NATIVE_NAMES := TestBuiltClientScopedParentFakeProvidersSurviveDisconnect|TestBuiltWTClaudeScopedMailboxTwoLocalOnlyWings|TestUnboundedStopKillsProcessGroupAndReportsSurvivors
# Only this clone's disposable home, state and staged caches are visible to the
# gate. Drop ambient provider credentials and account/relay selectors entirely.
PARENT_LIFECYCLE_ENV = env -i PATH="$(PARENT_LIFECYCLE_PATH)" HOME="$(PARENT_LIFECYCLE_ROOT)/home" \
	TMPDIR="$(CURDIR)/.scratch/t" GOTMPDIR="$(CURDIR)/.scratch/t" \
	GOCACHE="$(CURDIR)/.scratch/gocache" GOMODCACHE="$(CURDIR)/.scratch/modcache" GOPATH="$(CURDIR)/.scratch/gopath" \
	GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-p=2 GOMAXPROCS=2 LEAN_NUM_THREADS=2 \
	npm_config_offline=true npm_config_cache="$(CURDIR)/.scratch/npm" PYTHONDONTWRITEBYTECODE=1 \
	CLANG_MODULE_CACHE_PATH="$(CURDIR)/.scratch/clang-cache"

define parent-lifecycle-prepare
mkdir -p "$(PARENT_LIFECYCLE_ROOT)/home" "$(PARENT_LIFECYCLE_ROOT)/logs" \
	"$(CURDIR)/.scratch/t" "$(CURDIR)/.scratch/gocache" "$(CURDIR)/.scratch/modcache" "$(CURDIR)/.scratch/npm"
endef

# Fresh JSON logs plus a checked receipt: a missing test/package or any skip is
# a failed gate. The receipt binds the source tree, log and optional wt binary.
define parent-lifecycle-test
@set -eu; $(parent-lifecycle-prepare); \
	log=$$(mktemp "$(PARENT_LIFECYCLE_ROOT)/logs/$(1).XXXXXX"); result=0; \
	source_head=$$(git rev-parse HEAD); source_diff=$$(git diff HEAD -- . ':!.scratch' | shasum -a 256 | cut -d ' ' -f 1); \
	$(PARENT_LIFECYCLE_ENV) $(if $(4),WT_TEST_BINARY="$(PARENT_LIFECYCLE_ROOT)/wt") \
		$(GO) test -p 1 -parallel=2 -json $(2) -run '^($(3))$$' -count=1 -timeout=5m $(5) > "$$log" 2>&1 || result=$$?; \
	if [ "$$source_head" != "$$(git rev-parse HEAD)" ] || [ "$$source_diff" != "$$(git diff HEAD -- . ':!.scratch' | shasum -a 256 | cut -d ' ' -f 1)" ]; then \
		echo 'Source changed during the lifecycle gate' >&2; result=1; fi; \
	$(PARENT_LIFECYCLE_ENV) python3 scripts/check-parent-lifecycle.py "$$log" \
		--profile $(1) --packages $(2) --source "$$source_head" --diff-sha "$$source_diff" \
		--gate-exit "$$result" \
		--go-version "$$($(PARENT_LIFECYCLE_ENV) $(GO) version)" \
		$(if $(4),--binary "$(PARENT_LIFECYCLE_ROOT)/wt") || result=1; \
	exit "$$result"
endef

HOST_ARCH := $(shell uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
HOST_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
LINUX_ARCH ?= amd64
# Lazy: ordinary check/gate commands do not contact Docker.
DOCKER_ARCH = $(shell arch=$$(docker info --format '{{.Architecture}}' 2>/dev/null); \
	if [ -n "$$arch" ]; then echo "$$arch" | sed 's/x86_64/amd64/; s/aarch64/arm64/'; \
	else echo $(HOST_ARCH); fi)
LINUX_TEST_ARCH ?= $(DOCKER_ARCH)
LINUX_DISTROS ?= debian ubuntu
WEB_TEST_ARCH ?= $(DOCKER_ARCH)
CONVERSATION_FIXTURE_ROOT ?= $(abspath ..)
CONVERSATION_BINARY ?=
CONVERSATION_BINARY_SHA256 ?=
PREVIEW_CONTEXT_BINARY ?= ./wt-preview
ACTION ?= status
COUNT ?= 1
LOGIN ?= 1
EDGE ?= 0

# Go embeds web/dist. A fresh clone needs a placeholder; web replaces it with
# real assets. This is an internal file prerequisite, not a public build target.
web/dist:
	@mkdir -p $@
	@printf '%s\n' '<!doctype html><meta charset="utf-8"><title>wingthing</title>' \
		'<p>Web assets were not built. Run <code>make web</code>.' > $@/index.html

define stable-build
$(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o wt ./cmd/wt
endef
define preview-build
$(GO) build -p 2 -buildvcs=false -ldflags "$(PREVIEW_LDFLAGS)" -o wt-preview ./cmd/wt
endef
# Local artifacts only: installer/checksums/channel metadata are also consumed by
# the integration channel-isolation proof. No install, tag or upload occurs.
define preview-package
mkdir -p dist-preview
cp wt-preview dist-preview/wt-preview-$(HOST_OS)-$(HOST_ARCH)
cp scripts/preview-package.sh dist-preview/preview-package.sh
cd dist-preview && shasum -a 256 wt-preview-* > SHA256SUMS
./wt-preview channel --json > dist-preview/channel.json
endef

# All untagged Go tests include android-contract and every former focused unit
# selection (preview, conversation, socket, protected-target, remote and wake).
check: web
	# Build once outside the PTY interaction deadlines, including CGO pairing.
	$(stable-build)
	$(preview-build)
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 ./...

web:
	cd web && $(NPM) ci
	cd web && for f in src/chat-view.js src/conversation-view.js src/parent-dot.js; do $(NODE) --check $$f || exit 1; done
	cd web && $(NPM) test
	cd web && $(NPM) run build

# Hash-verified offline snapshots; no socket, daemon or provider is opened.
test-run-inspect: | web/dist
	$(GO) test -p 2 ./internal/runinspect -run '^TestRunInspect' -v -count=1 -parallel=2 -timeout=2m
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 ./cmd/wt -run '^(TestRunInspect.*|TestDashboardNoTTYHelpUnchanged)$$' -v -count=1 -parallel=2 -timeout=2m

test-run-inspect-race: | web/dist
	$(GO) test -p 2 -race ./internal/runinspect -run '^TestRunInspect' -v -count=1 -parallel=2 -timeout=2m
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -race ./cmd/wt -run '^(TestRunInspect.*|TestDashboardNoTTYHelpUnchanged)$$' -v -count=1 -parallel=2 -timeout=2m

# Fixture-only serializer adapters are excluded from the ordinary built CLI.
# Every invocation writes fresh, retained tiny artifacts below this scratch root.
test-run-inspect-roundtrip: | web/dist
	$(GO) test -p 2 $(INSPECT_TEST_FLAGS) ./internal/store -run '^(TestConversationDeliveryReplayAndCheckpointCAS|TestConversationLineageReplayAndResumePersist)$$' -v -count=1 -parallel=2 -timeout=2m
	@mkdir -p "$(CURDIR)/.scratch/runinspect-roundtrip"
	$(stable-build)
	WT_INSPECT_FIXTURE_ROOT="$(CURDIR)/.scratch/runinspect-roundtrip" WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -tags runinspectfixture $(INSPECT_TEST_FLAGS) ./internal/runinspect ./cmd/wt -run '^TestRunInspectRoundTrip' -v -count=1 -parallel=2 -timeout=3m

# Finite, offline native-provider fixtures; no live provider or remote launches.
test-parent-lifecycle-protocol: | web/dist
	$(call parent-lifecycle-test,protocol,$(PARENT_LIFECYCLE_PACKAGES),$(PARENT_LIFECYCLE_PROTOCOL_NAMES),,)

test-parent-lifecycle-protocol-race: | web/dist
	$(call parent-lifecycle-test,protocol-race,$(PARENT_LIFECYCLE_PACKAGES),$(PARENT_LIFECYCLE_PROTOCOL_NAMES),,-race)

# These protocol fixtures use an explicit trusted outer process boundary and
# Unix IPC. The two native parent tests below retain actual sandbox enforcement.
test-parent-lifecycle-built: | web/dist
	@$(parent-lifecycle-prepare)
	$(PARENT_LIFECYCLE_ENV) $(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=parent-lifecycle.$(VERSION)" -o "$(PARENT_LIFECYCLE_ROOT)/wt" ./cmd/wt
	$(call parent-lifecycle-test,built,./cmd/wt,$(PARENT_LIFECYCLE_BUILT_NAMES),binary,)

# Complete ordinary-host acceptance, including both original parent-exit modes,
# native proxy binding and /bin/ps descendant inventory. Do not substitute the
# protocol target when reporting this gate; restricted workers leave it unrun.
test-parent-lifecycle-offline: | web/dist
	@$(parent-lifecycle-prepare)
	$(PARENT_LIFECYCLE_ENV) $(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=parent-lifecycle.$(VERSION)" -o "$(PARENT_LIFECYCLE_ROOT)/wt" ./cmd/wt
	$(call parent-lifecycle-test,offline,$(PARENT_LIFECYCLE_PACKAGES),$(PARENT_LIFECYCLE_PROTOCOL_NAMES)|$(PARENT_LIFECYCLE_BUILT_NAMES)|$(PARENT_LIFECYCLE_NATIVE_NAMES),binary,)

test-run-recovery: | web/dist
	$(GO) test -p 2 $(RUN_RECOVERY_PACKAGES) -run '$(RUN_RECOVERY_TESTS)' -count=$(COUNT) -timeout=5m

test-run-recovery-race: | web/dist
	$(GO) test -p 2 -race $(RUN_RECOVERY_PACKAGES) -run '$(RUN_RECOVERY_TESTS)' -count=$(COUNT) -timeout=5m

test-run-load: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 ./cmd/wt -run '^TestBuiltWTLocalOnlyDaemonBootsWithoutTokens$$' -count=10 -timeout=5m
	$(GO) test -p 2 ./internal/wingconnect -run '^TestRememberedPoolCanceledCallIsBounded$$' -count=10 -timeout=5m
	$(MAKE) --no-print-directory test-run-load-full

test-run-load-full: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 ./cmd/wt ./internal/wingconnect -count=1 -timeout=15m

test-run-canary: | web/dist
	$(stable-build)
	scripts/dogfood/run.sh

# Select one or more profiles, e.g. make gate GATE='integration compat'. Real
# provider profiles are opt-in and fail when their required fixtures are absent.
# Compatibility uses configured historical baselines v0.144.1 and v0.147.0;
# WT_COMPAT_BASELINE_REF selects one override (tags must exist in the clone).
gate: | web/dist
ifeq ($(strip $(GATE)),)
	$(error GATE must select at least one profile)
endif
ifneq ($(filter-out integration compat static claude provider-swap coverage input,$(GATE)),)
	$(error Unknown GATE profile: $(filter-out integration compat static claude provider-swap coverage input,$(GATE)))
endif
ifneq ($(filter integration static provider-swap,$(GATE)),)
	$(stable-build)
endif
ifneq ($(filter integration input,$(GATE)),)
	$(preview-build)
endif
ifneq ($(filter integration,$(GATE)),)
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 -tags e2e -v -timeout 120s ./test/integ/...
	$(preview-package)
	python3 test/preview/channel_isolation.py ./wt ./wt-preview ./dist-preview
	python3 test/preview/provider_onboarding.py ./wt-preview
	python3 test/preview/remote_reader_drain_regression.py
	@if [ "$$(uname -s)" = Linux ]; then python3 test/preview/remote_isolation.py ./wt ./wt-preview; fi
	@if [ "$$(uname -s)" = Linux ]; then $(GO) test -p 2 -c -o test/linux/eggclient-tests ./internal/eggclient/ && sh test/linux/roost-pin-tests.sh test/linux/eggclient-tests; fi
endif
ifneq ($(filter compat,$(GATE)),)
	scripts/test-backward-compat.sh
endif
ifneq ($(filter static,$(GATE)),)
	$(GO) vet -p 2 ./...
	@set -eu; packages='$(RACE_PACKAGES)'; \
	if [ -z "$$packages" ]; then \
		git rev-parse --verify '$(RACE_BASE)^{commit}' >/dev/null; \
		packages=$$({ git diff --name-only '$(RACE_BASE)' -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } | \
			sed 's|/[^/]*$$||; s|^[^/]*\.go$$|.|' | sort -u | \
			while IFS= read -r dir; do [ ! -d "$$dir" ] || printf './%s\n' "$$dir"; done); \
		if [ -n "$$packages" ]; then \
			packages=$$($(GO) list -p 2 -e -f '{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' $$packages); \
		fi; \
	fi; \
	if [ -n "$$packages" ]; then WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -race $$packages; \
	else echo 'No touched Go packages to race-test'; fi
	$(GO) run -p 2 golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	cd web && $(NPM) ci && $(NPM) audit --audit-level=high
	scripts/check-release-contract.sh ./wt
endif
ifneq ($(filter claude,$(GATE)),)
	WT_REQUIRE_REAL_CLAUDE=1 $(GO) test -p 2 -count=1 -tags integration -v -timeout 180s ./cmd/wt -run '^TestRealClaudeModelPolicyPreservesPersonalSettings$$'
endif
ifneq ($(filter provider-swap,$(GATE)),)
	WT_SMOKE_WT_BIN="$(or $(WT_SMOKE_WT_BIN),$(CURDIR)/wt)" python3 test/live/provider_swap_smoke.py
endif
ifneq ($(filter coverage,$(GATE)),)
	$(GO) test -p 2 -coverprofile=$(COVERAGE_OUT) ./...
	$(GO) tool cover -func=$(COVERAGE_OUT)
endif
ifneq ($(filter input,$(GATE)),)
	WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 -tags integration -v -timeout 60s ./cmd/wt -run '^TestPreviewBrowserLeaseOnRealEgg$$'
endif

e2e-linux: | web/dist
	@if [ "$(LINUX_TEST_ARCH)" != "$(DOCKER_ARCH)" ]; then \
		echo "cross-architecture seccomp tests are invalid (docker=$(DOCKER_ARCH), requested=$(LINUX_TEST_ARCH)); run this battery on a native $(LINUX_TEST_ARCH) Docker daemon"; exit 1; \
	fi
ifeq ($(strip $(LINUX_DISTROS)),)
	$(error LINUX_DISTROS must name debian and/or ubuntu)
endif
ifneq ($(filter-out debian ubuntu,$(LINUX_DISTROS)),)
	$(error Unknown LINUX_DISTROS: $(filter-out debian ubuntu,$(LINUX_DISTROS)))
endif
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=test" -o test/linux/wt ./cmd/wt
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=test -X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=preview" -o test/linux/wt-preview ./cmd/wt
	@mkdir -p test/linux/runinspect-orphan
	@cp -R internal/runinspect/testdata/orphan/. test/linux/runinspect-orphan/
	@cp scripts/install.sh test/linux/install.sh
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) build -p 2 -o test/linux/mock-agent ./test/mock-agent/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -tags 'e2e linux' -o test/linux/run-tests ./test/linux/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -tags integration -o test/linux/sandbox-tests ./internal/sandbox/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -tags integration -o test/linux/wt-tests ./cmd/wt/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -o test/linux/eggclient-tests ./internal/eggclient/
	@set -e; for distro in $(LINUX_DISTROS); do \
		case "$$distro" in debian) dockerfile= testimage=wt-test-linux ;; ubuntu) dockerfile=.ubuntu2404 testimage=wt-test-ubuntu ;; *) echo "Unknown distro: $$distro"; exit 1 ;; esac; \
		docker build -t "$$testimage" -f "test/linux/Dockerfile$$dockerfile" test/linux/; \
		docker run --rm --privileged \
   --tmpfs /provider-guard-fixtures:rw,exec,size=64m --tmpfs /tmp:rw,exec \
   -e WT_PROVIDER_GUARD_FIXTURE_ROOT=/provider-guard-fixtures "$$testimage" sh -lc \
			'/root/run-tests -test.v -test.timeout 120s && /root/sandbox-tests -test.v -test.timeout 120s && /root/wt-tests -test.v -test.timeout 120s && sh /root/roost-pin-tests.sh /root/eggclient-tests'; \
	done

e2e-web: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) $(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=test" -o test/web/wt ./cmd/wt
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) $(GO) build -p 2 -o test/web/claude ./test/web/canary-agent/
	test/web/run.sh

e2e-mac: web
	@test "$$(uname -s)" = Darwin || { echo 'e2e-mac requires the macOS Seatbelt parent sandbox'; exit 1; }
	$(stable-build)
	$(preview-build)
	# Keep the two-minute deadline for each group while running every CLI test.
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 -tags integration -v -timeout 120s ./internal/sandbox ./cmd/wt -skip '^Test(Northstar|NativeTerminal)'
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 -tags integration -v -timeout 120s ./cmd/wt -run '^Test(Northstar|NativeTerminal)'
	$(GO) test -p 2 ./internal/egg -run 'TestPreviewClaude(GuardRejectsSymlinkAliases|ContextRejectsAliasedForeignSelector|LifecycleSettingsAndHooksKeepDataHome)$$' -count=1
	python3 -B -m unittest discover -s test/preview -p test_mac_provider_context.py
	python3 test/preview/mac_provider_context.py "$(PREVIEW_CONTEXT_BINARY)"
	$(NODE) test/conversation/proof.mjs ./wt-preview --expect-bound-socket-block
	$(NODE) test/conversation/proof.mjs "$(or $(strip $(CONVERSATION_BINARY)),./wt-preview)" --state-root "$(CONVERSATION_FIXTURE_ROOT)"$(if $(strip $(CONVERSATION_BINARY_SHA256)), --expect-sha256 "$(strip $(CONVERSATION_BINARY_SHA256))")

release: web
ifeq ($(CHANNEL),preview)
	$(preview-build)
	$(preview-package)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) $(GO) build -p 2 -buildvcs=false -ldflags "$(PREVIEW_LDFLAGS)" -o dist-preview/wt-preview-linux-$(LINUX_ARCH) ./cmd/wt
	cd dist-preview && shasum -a 256 wt-preview-* > SHA256SUMS
else ifeq ($(CHANNEL),stable)
	@echo "Building $(VERSION) for all platforms..."
	@mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; output="dist/wt-$$os-$$arch"; tmp="$$output.tmp"; \
		echo "  $$os/$$arch"; rm -f "$$tmp"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -p 2 -buildvcs=false -ldflags="$(LDFLAGS)" -o "$$tmp" ./cmd/wt; \
		mv "$$tmp" "$$output"; \
	done
	@CGO_ENABLED=0 $(GO) build -p 2 -buildvcs=false -ldflags="$(LDFLAGS)" -o dist/wt-contract ./cmd/wt
	@scripts/check-release-contract.sh dist/wt-contract
	@rm -f dist/wt-contract
	@cd dist && set -e; assets="wt-linux-amd64 wt-linux-arm64 wt-darwin-amd64 wt-darwin-arm64"; if command -v sha256sum >/dev/null 2>&1; then sha256sum $$assets > SHA256SUMS.tmp; else shasum -a 256 $$assets > SHA256SUMS.tmp; fi; mv SHA256SUMS.tmp SHA256SUMS
	@echo "Built $(VERSION) -> dist/ (publish via gh release create)"
else
	$(error CHANNEL must be stable or preview)
endif

deploy: check gate
	fly deploy

# Local builds/vector generation and explicitly selected Fly operations.
ops: | web/dist
ifeq ($(ACTION),build)
	$(stable-build)
else ifeq ($(ACTION),android-vectors)
	$(GO) run -p 2 ./android/contract/generate.go
else ifeq ($(ACTION),status)
	fly machines list
	fly scale show
else ifeq ($(ACTION),scale)
	fly scale count login=$(LOGIN) edge=$(EDGE) --yes
else ifeq ($(ACTION),deploy-edge)
	@test -n "$(REGIONS)" || { echo 'REGIONS is required. Example: make ops ACTION=deploy-edge REGIONS=nrt,lhr'; exit 1; }
	@grep -Eq '^[[:space:]]*edge[[:space:]]*=' fly.toml || \
		{ echo 'edge process is disabled in fly.toml; follow docs/fly-ops.md before scaling' >&2; exit 1; }
	@awk 'BEGIN { section = 0; found = 0 } /^\[http_service\]$$/ { section = 1; next } /^\[/ { section = 0 } section && /^[[:space:]]*processes[[:space:]]*=/ && /"edge"/ { found = 1 } END { exit !found }' fly.toml || \
		{ echo 'edge is not attached to http_service.processes in fly.toml' >&2; exit 1; }
	fly scale count edge=$(COUNT) --region $(REGIONS) --yes
else
	$(error Unknown ops ACTION: $(ACTION))
endif

proto:
	protoc -I proto --go_out=paths=source_relative:internal/egg/pb --go-grpc_out=paths=source_relative:internal/egg/pb proto/egg.proto

serve: | web/dist
	$(stable-build)
	./wt serve

clean:
	rm -f wt wt-preview
	rm -rf dist/ dist-preview/
	rm -f test/linux/wt test/linux/mock-agent test/linux/run-tests test/linux/sandbox-tests test/linux/wt-tests test/linux/install.sh test/web/wt test/web/claude

# Offline metadata inventory gates: no daemon, browser, provider or listener.
RUN_INVENTORY_TESTS ?= RunInventory|AgentList|ServerLiteralsSetVersion|Dashboard|RootHelp
.PHONY: test-run-inventory-store test-run-inventory test-run-inventory-race build-run-inventory-linux
test-run-inventory-store: | web/dist
	$(GO) test -p 2 -json -count=1 ./internal/store ./internal/control

test-run-inventory: | web/dist
	$(GO) test -p 2 -json -count=1 ./internal/localmcp ./internal/wingsession ./internal/runinventory ./cmd/wt -run '$(RUN_INVENTORY_TESTS)' -timeout=5m

test-run-inventory-race: | web/dist
	$(GO) test -race -p 2 -json -count=1 ./internal/store ./internal/wingsession ./internal/localmcp ./internal/runinventory ./cmd/wt -run '$(RUN_INVENTORY_TESTS)' -timeout=5m

build-run-inventory-linux: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -p 2 -buildvcs=false -o .scratch/wt-inventory-linux ./cmd/wt

.PHONY: test-run-inventory-retention
test-run-inventory-retention: | web/dist
	$(GO) test -race -p 2 -json -count=1 ./internal/localmcp -run '^TestRunInventoryReopenRetentionRemovedSessionsAndFiniteConsumer$$' -timeout=5m

.PHONY: vet-run-inventory
vet-run-inventory:
	$(GO) vet -p 2 ./...

.PHONY: test-run-inventory-fixture-safety
test-run-inventory-fixture-safety: | web/dist
	$(GO) test -race -p 2 -json -count=1 ./internal/localmcp -run '^Test(AgentListSerializedAdaptersReturnSameDurableIdentities|AgentListGrantBoundsAndUnsupportedErrors|RunInventoryTwoWingsIdenticalIDsRespectLiveOwnerAndPathPolicies)$$' -timeout=5m

# Bounded offline launch-plan gates: no built CLI execution, provider, listener,
# native sandbox, browser, installs, or global cache access.
.PHONY: test-launch-plan test-launch-plan-contract vet-launch-plan build-launch-plan test-launch-plan-parity
test-launch-plan: test-launch-plan-parity
	$(GO) test -p 2 ./internal/agent ./internal/config
	$(GO) test -p 2 ./internal/egg -run 'LaunchConfig|LaunchCommand|LaunchPreparation|LaunchState|CodexLifecycle(Args|Supported|Skips|Native)|CodexRunArgs|MergeEggConfig|ResolveEggConfig|DiscoverEggConfig|SectionMask|BaseField|BuildEnv|ParseFSRules|ToSandboxConfig|ResolvePolicy|ProviderURLGuardrails|InferLocalPorts|DefaultEggConfig|UnsandboxedEggConfig|WildcardNetwork|TaskYAML' -count=1
	$(GO) test -p 2 ./internal/launchplan ./internal/fsutil -count=1

# Pure differential oracles: no native sandbox, provider execution or listener.
test-launch-plan-parity:
	$(GO) test -p 2 ./internal/sandbox -run '^(TestReview|TestLaunchPlan|TestLaunchPreparation|TestBuildProfile|TestCanonicalSandboxPath)' -count=1
	$(GO) test -p 2 ./internal/eggclient -run '^TestMCP(SubdirectoryRetainsRootPolicy|LaunchPreservesExplicitNestedRootPolicy)$$' -count=1

test-launch-plan-contract: | web/dist
	$(GO) test -p 2 ./cmd/wt -run 'Doctor.*Launch|LaunchPlan' -count=1

vet-launch-plan: | web/dist
	$(GO) vet -p 2 ./...

build-launch-plan: | web/dist
	$(GO) build -p 2 -buildvcs=false -o .scratch/wt-launch-plan ./cmd/wt

# Coordinator-only: invokes native sandbox children and disposable loopback.
.PHONY: accept-launch-plan-native
accept-launch-plan-native:
	python3 -B scripts/launch-plan-native-gate.py --go '$(shell command -v go)'

# Pinned role contracts and installed Claude SessionStart/compaction proof.
.PHONY: test-coordinator-role test-coordinator-role-vendor
test-coordinator-role: | web/dist
	$(GO) test -p 2 -count=1 ./internal/sessionrole ./internal/egg ./internal/eggclient ./internal/localmcp ./internal/dashboard ./internal/control ./internal/wingpolicy ./cmd/wt -run 'Role|Pin|Pinned|SessionPS|RegistryDefines|ScopedParent|IsUnderPathsRoot' -timeout 180s

test-coordinator-role-vendor: | web/dist
	WT_REQUIRE_REAL_CLAUDE=1 $(GO) test -p 2 -count=1 -tags integration -v ./cmd/wt -run '^TestRealClaudeRoleContextAfterCompaction$$' -timeout 120s

.PHONY: test-coordinator-role-native test-coordinator-role-race
test-coordinator-role-native: | web/dist
	@test "$$(uname -s)" = Darwin || { echo 'protected coordinator acceptance requires macOS'; exit 1; }
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -count=1 -v ./cmd/wt -run '^TestBuiltWTPinnedCoordinatorScopedMailbox$$' -timeout 180s

test-coordinator-role-race: | web/dist
	$(GO) test -race -p 2 -count=1 ./internal/sessionrole ./internal/egg ./internal/eggclient ./internal/localmcp ./internal/dashboard ./internal/control ./internal/wingpolicy ./cmd/wt -run 'Role|Pin|Pinned|SessionPS|RegistryDefines|ScopedParent|IsUnderPathsRoot' -timeout 180s

# Qualified inventory, exact identity and outage routing acceptance; no providers.
.PHONY: test-multiwing test-multiwing-race
test-multiwing: | web/dist
	$(stable-build)
	$(preview-build)
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -v -p 2 ./internal/fleet ./internal/egg ./internal/eggclient ./internal/sessionrole ./internal/control ./internal/localmcp ./internal/wing ./internal/phonepair ./cmd/wt -run 'Test(Registry|LifecycleSourceCompleteness|Merge|SessionCatalog|CoordinatorPresence|SessionInventory|QualifiedSession|CoordinatorHeartbeat|OwnerFleet|PhoneFleet|BuiltMultiwing|RemoteInventoryOrigin)' -count=1 -timeout=3m

test-multiwing-race: | web/dist
	$(GO) test -p 2 -race ./internal/fleet ./internal/egg ./internal/eggclient ./internal/sessionrole ./internal/localmcp ./internal/wing -run 'Test(Registry|LifecycleSourceCompleteness|Merge|SessionCatalog|CoordinatorPresence|SessionInventory|QualifiedSession|CoordinatorHeartbeat|OwnerFleet|PhoneFleet|BuiltMultiwing|RemoteInventoryOrigin)' -count=1 -timeout=3m

# Isolated terminal onboarding and storyboard contract; no vendor login/install.
.PHONY: test-tui-start test-tui-start-race
test-tui-start: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -count=1 ./internal/onboarding ./internal/dashboard ./cmd/wt -run 'Onboarding|Setup|CodexShortcut|NorthstarPTY|Dashboard' -timeout 10m -v

test-tui-start-race: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -race -p 2 -count=1 ./internal/onboarding ./internal/dashboard ./cmd/wt -run 'Onboarding|Setup|CodexShortcut|Dashboard' -timeout 5m

.PHONY: capture-tui-start
capture-tui-start: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -count=1 ./cmd/wt -run '^TestOnboardingPTYStoryboard$$' -v -timeout 3m
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -count=1 ./cmd/wt -run '^TestOnboardingCodexPTYSession$$' -v -timeout 1m

# Storyboard machine/runs journeys: production CLI/TUI with isolated fixture wings.
.PHONY: test-storyboard-machines-runs
test-storyboard-machines-runs: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 ./internal/dashboard ./internal/launchplan ./cmd/wt -run '^Test(Diagnostics|MachineDetails|RunReceipt|LaunchPlanHumanReach|StoryboardMachinesRunsPTY|BuiltWTConnectRememberedSSHRunSurvivesDrop|MCPConnect(Verifies|Upgrade)|BuiltWTVerifiedSSH|VerifiedCLI|VerifiedProfile)' -v -count=1 -timeout=3m

.PHONY: test-storyboard-runs-offline
test-storyboard-runs-offline: | web/dist
	$(stable-build)
	WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -tags runinspectfixture ./cmd/wt -run '^TestStoryboard(RunsOffline|TerminalTask)PTY$$' -v -count=1 -timeout=3m

# Real built loopback roost plus encrypted fixture wings. Optional Playwright
# module/executable selectors let hosts reuse their installed browser tooling.
.PHONY: test-web-reach-share
test-web-reach-share: web
	$(stable-build)
	$(NODE) test/web/reach-share.mjs

# Dormant UI fixtures. No provider, payment, real account or VM acceptance.
.PHONY: test-paid-gate test-paid-ui
test-paid-gate: | web/dist
	$(GO) test -p 2 ./internal/relay ./cmd/wt -run '^TestPaidFixture' -count=1 -timeout=2m

test-paid-ui:
	$(NODE) web/test/paid-ui.mjs

# Real Linux kernel required; missing Landlock/userns and every skip fail this gate.
.PHONY: test-linux-provider-guard accept-linux-provider-guard
test-linux-provider-guard: | web/dist
	$(GO) test -p 2 ./internal/sandbox ./internal/localmcp -run 'LinuxProtected|LinuxProviderWrite' -count=1

accept-linux-provider-guard:
	@test "$$(uname -s)" = Linux || { echo 'accept-linux-provider-guard requires Linux'; exit 1; }
	python3 -B scripts/linux-provider-guard-gate.py --go '$(shell command -v go)'

.PHONY: test-linux-provider-guard-portability
test-linux-provider-guard-portability: | web/dist
	@mkdir -p .scratch/linux-provider-guard-cross
	@set -eu; for platform in darwin/amd64 darwin/arm64 linux/arm64; do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		for package in sandbox egg localmcp; do \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) test -p 2 -c -o .scratch/linux-provider-guard-cross/$$os-$$arch-$$package ./internal/$$package; \
		done; \
	done
.PHONY: test-session-report test-coordinator-home-web
test-session-report: | web/dist
	$(GO) test -p 2 -count=1 ./internal/egg ./internal/localmcp ./internal/control ./internal/wing ./cmd/wt -run 'SessionReport|RegistryDefinesExpectedSurfaceOperations' -timeout 180s

test-coordinator-home-web:
	cd web && $(NODE) test/coordinator-home-browser.mjs

# Incremental search cache and serialized qualified CLI/MCP contracts.
.PHONY: test-session-search test-session-search-race test-session-search-web
test-session-search: | web/dist
	$(GO) test -p 2 ./internal/sessionsearch ./internal/egg ./internal/eggclient ./internal/localmcp ./internal/control ./internal/tunnel ./internal/wing -run 'SessionSearch|LifecycleBounded|SessionCatalog|SessionInventory|Registry|OwnerFleet|PhoneFleetEncryptedSearch' -count=1 -timeout=3m
	$(MAKE) --no-print-directory test-multiwing

test-session-search-race: | web/dist
	$(GO) test -p 2 -race ./internal/sessionsearch ./internal/egg ./internal/eggclient ./internal/localmcp ./internal/control -run 'SessionSearch|LifecycleBounded|SessionCatalog|SessionInventory|Registry' -count=1 -timeout=3m

test-session-search-web: web
	$(stable-build)
	WT_SESSION_SEARCH_BROWSER=1 WT_TEST_BINARY="$(CURDIR)/wt" $(GO) test -p 2 -count=1 ./cmd/wt -run '^TestBuiltSessionSearchDashboard$$' -v -timeout=90s

.PHONY: test-session-takeover test-session-takeover-race test-session-takeover-web
test-session-takeover: | web/dist
	$(GO) test -p 2 ./internal/wing ./internal/localmcp ./internal/tunnel ./internal/phonepair -run 'BrowserFleet|PhoneFleet|QualifiedSession' -count=1 -timeout=3m

test-session-takeover-race: | web/dist
	$(GO) test -p 2 -race ./internal/wing ./internal/localmcp ./internal/tunnel ./internal/phonepair -run 'BrowserFleet|PhoneFleet|QualifiedSession' -count=1 -timeout=3m

test-session-takeover-web: web
	$(preview-build)
	WT_SESSION_TAKEOVER_BROWSER=1 WT_TEST_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 ./cmd/wt -run '^TestBuiltSessionTakeoverDashboard$$' -v -timeout=180s
.PHONY: test-session-search-regression
test-session-search-regression: | web/dist
	$(stable-build)
	$(preview-build)
	WT_TEST_BINARY="$(CURDIR)/wt" WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=5 ./cmd/wt -run '^Test(NorthstarSSHLogSnapshotJoinsInProgressAppend|NorthstarPTYRemoteReload)$$' -timeout=3m

# Finite phone listener startup/shutdown, including immediate context cancellation.
.PHONY: test-phone-discovery-lifecycle
test-phone-discovery-lifecycle: | web/dist
	$(GO) test -v -p 2 -race ./internal/phonepair -run '^TestPhoneListenerImmediateShutdown$$' -count=5 -timeout=60s

.PHONY: test-pwa test-pwa-web
test-pwa: | web/dist
	$(GO) test -p 2 ./internal/tunnidentity ./internal/relay ./internal/localrelay -run 'Tunn|LocalMode|LocalBrowser' -count=1 -timeout=90s
	cd web && $(NODE) --test test/pwa.test.js test/coordinator-thread.test.js test/session-search.test.js test/session-viewer.test.js

test-pwa-web: web
	$(preview-build)
	WT_SESSION_TAKEOVER_BROWSER=1 WT_TEST_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 ./cmd/wt -run '^TestBuiltSessionTakeoverDashboard$$' -v -timeout=180s
	cd web && $(NODE) test/pwa-browser.mjs

.PHONY: test-permissions test-permissions-race
test-permissions: | web/dist
	$(GO) test -p 2 ./internal/permissions ./internal/permissionbridge ./internal/egg ./internal/control ./internal/controlsocket ./internal/dashboard ./internal/localmcp ./internal/tunnel -count=1 -timeout=5m

test-permissions-race: | web/dist
	$(GO) test -race -p 2 ./internal/permissions ./internal/egg ./internal/control ./internal/dashboard ./internal/localmcp ./internal/tunnel -run Permission -count=1 -timeout=3m
# Accountless browser join: native scope gates and the branch-built loopback UI.
.PHONY: test-browser-join-go test-browser-join
test-browser-join-go: | web/dist
	$(GO) test -p 2 -count=1 ./internal/pake ./internal/phonepair ./internal/relay ./internal/wing

test-browser-join: web
	$(stable-build)
	$(NODE) test/web/browser-join.mjs
