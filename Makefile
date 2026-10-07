.DEFAULT_GOAL := check
.PHONY: check gate e2e-linux e2e-web e2e-mac release deploy ops proto web serve clean

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
	$(GO) test -p 2 ./...
	$(stable-build)

web:
	cd web && $(NPM) ci
	cd web && for f in src/chat-view.js src/conversation-view.js src/parent-dot.js; do $(NODE) --check $$f || exit 1; done
	cd web && $(NPM) test
	cd web && $(NPM) run build

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
	if [ -n "$$packages" ]; then $(GO) test -p 2 -race $$packages; \
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
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) build -p 2 -o test/linux/mock-agent ./test/mock-agent/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -tags 'e2e linux' -o test/linux/run-tests ./test/linux/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -tags integration -o test/linux/sandbox-tests ./internal/sandbox/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_TEST_ARCH) $(GO) test -p 2 -c -tags integration -o test/linux/wt-tests ./cmd/wt/
	@set -e; for distro in $(LINUX_DISTROS); do \
		case "$$distro" in debian) dockerfile= testimage=wt-test-linux ;; ubuntu) dockerfile=.ubuntu2404 testimage=wt-test-ubuntu ;; *) echo "Unknown distro: $$distro"; exit 1 ;; esac; \
		docker build -t "$$testimage" -f "test/linux/Dockerfile$$dockerfile" test/linux/; \
		docker run --rm --privileged "$$testimage" sh -lc \
			'/root/run-tests -test.v -test.timeout 120s && /root/sandbox-tests -test.v -test.timeout 120s && /root/wt-tests -test.v -test.timeout 120s'; \
	done

e2e-web: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) $(GO) build -p 2 -buildvcs=false -ldflags "-X main.version=test" -o test/web/wt ./cmd/wt
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) $(GO) build -p 2 -o test/web/claude ./test/web/canary-agent/
	test/web/run.sh

e2e-mac: web
	@test "$$(uname -s)" = Darwin || { echo 'e2e-mac requires the macOS Seatbelt parent sandbox'; exit 1; }
	$(stable-build)
	$(preview-build)
	WT_TEST_PREVIEW_BINARY="$(CURDIR)/wt-preview" $(GO) test -p 2 -count=1 -tags integration -v -timeout 120s ./internal/sandbox ./cmd/wt
	$(GO) test -p 2 ./internal/egg -run 'TestPreviewClaude(GuardRejectsSymlinkAliases|ContextRejectsAliasedForeignSelector|LifecycleSettingsAndHooksKeepDataHome)$$' -count=1
	python3 -B -m unittest discover -s test/preview -p test_mac_provider_context.py
	python3 test/preview/mac_provider_context.py "$(PREVIEW_CONTEXT_BINARY)"
	$(NODE) test/conversation/proof.mjs ./wt-preview --expect-protected-state-refusal
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
	rm -f test/linux/wt test/linux/mock-agent test/linux/run-tests test/linux/sandbox-tests test/linux/wt-tests test/web/wt test/web/claude
