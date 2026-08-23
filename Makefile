.PHONY: build test coverage check clean web serve release proto deploy deploy-edge scale status jail \
	build-linux build-mock-agent build-linux-tests build-linux-sandbox-tests test-linux test-linux-ubuntu test-integ test-e2e \
	build-linux-wt-tests \
	test-provider-swap build-web-e2e test-web

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
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

test: | web/dist
	go test ./...

COVERAGE_OUT ?= /tmp/wingthing-coverage.out
coverage: | web/dist
	go test -coverprofile=$(COVERAGE_OUT) ./...
	go tool cover -func=$(COVERAGE_OUT)

check: web test build

web:
	cd web && npm ci && npm run build

serve: build
	./wt serve

release: web
	@echo "Building $(VERSION) for all platforms..."
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		output="dist/wt-$$os-$$arch"; \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $$output ./cmd/wt; \
	done
	@echo "Built $(VERSION) -> dist/ (publish via gh release create)"

jail: build
	go test -tags integration -v ./internal/sandbox/ -run TestJail

deploy: check
	fly deploy

# Add edge nodes to a region. Usage: make deploy-edge REGIONS=nrt,lhr COUNT=1
COUNT ?= 1
deploy-edge:
ifndef REGIONS
	$(error REGIONS is required. Example: make deploy-edge REGIONS=nrt,lhr)
endif
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
# the local machine's native architecture because qemu/Rosetta translate
# syscall numbers below seccomp and produce invalid sandbox results.
HOST_ARCH := $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
LINUX_ARCH ?= amd64
LINUX_TEST_ARCH ?= $(HOST_ARCH)

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
# Binaries are built for the docker host's native arch so the container can
# run them without emulation. Needs the real web dist (it tests the UI).
WEB_TEST_ARCH ?= $(HOST_ARCH)

build-web-e2e: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) go build -buildvcs=false \
		-ldflags "-X main.version=test" -o test/web/wt ./cmd/wt
	CGO_ENABLED=0 GOOS=linux GOARCH=$(WEB_TEST_ARCH) go build -o test/web/mock-agent ./test/mock-agent/

test-web: build-web-e2e
	test/web/run.sh

test-linux:
	@if [ "$(LINUX_TEST_ARCH)" != "$(HOST_ARCH)" ]; then \
		echo "cross-architecture seccomp tests are invalid (host=$(HOST_ARCH), requested=$(LINUX_TEST_ARCH)); run this battery on a native $(LINUX_TEST_ARCH) host"; \
		exit 1; \
	fi
	$(MAKE) LINUX_ARCH=$(LINUX_TEST_ARCH) build-linux build-mock-agent build-linux-tests build-linux-sandbox-tests build-linux-wt-tests
	docker build -t wt-test-linux -f test/linux/Dockerfile test/linux/
	docker run --rm --privileged wt-test-linux sh -lc \
		'/root/run-tests -test.v -test.timeout 120s && /root/sandbox-tests -test.v -test.timeout 120s && /root/wt-tests -test.v -test.timeout 120s'

test-linux-ubuntu:
	@if [ "$(LINUX_TEST_ARCH)" != "$(HOST_ARCH)" ]; then \
		echo "cross-architecture seccomp tests are invalid (host=$(HOST_ARCH), requested=$(LINUX_TEST_ARCH)); run this battery on a native $(LINUX_TEST_ARCH) host"; \
		exit 1; \
	fi
	$(MAKE) LINUX_ARCH=$(LINUX_TEST_ARCH) build-linux build-mock-agent build-linux-tests build-linux-sandbox-tests build-linux-wt-tests
	docker build -t wt-test-ubuntu -f test/linux/Dockerfile.ubuntu2404 test/linux/
	docker run --rm --privileged wt-test-ubuntu sh -lc \
		'/root/run-tests -test.v -test.timeout 120s && /root/sandbox-tests -test.v -test.timeout 120s && /root/wt-tests -test.v -test.timeout 120s'

test-integ: | web/dist
	go test -count=1 -tags e2e -v -timeout 120s ./test/integ/...

test-e2e: test-linux test-linux-ubuntu test-integ

# Opt-in release gate for real, model-swapped harnesses. Requires the local
# Ollama/LiteLLM services and upstream CLIs documented in docs/release-e2e.md.
test-provider-swap:
	python3 test/live/provider_swap_smoke.py

clean:
	rm -f wt
	rm -rf dist/
	rm -f test/linux/wt test/linux/mock-agent test/linux/run-tests test/linux/sandbox-tests test/linux/wt-tests
