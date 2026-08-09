.PHONY: build test coverage check clean web serve release proto deploy deploy-edge scale status jail \
	build-linux build-mock-agent build-linux-tests test-linux test-linux-ubuntu test-integ test-e2e \
	test-provider-swap

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

# Detect host arch for cross-compilation target
LINUX_ARCH := $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')

build-linux: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -buildvcs=false \
		-ldflags "-X main.version=test" -o test/linux/wt ./cmd/wt

build-mock-agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -o test/linux/mock-agent ./test/mock-agent/

build-linux-tests: | web/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go test -c -tags 'e2e linux' \
		-o test/linux/run-tests ./test/linux/

test-linux: build-linux build-mock-agent build-linux-tests
	docker build -t wt-test-linux -f test/linux/Dockerfile test/linux/
	docker run --rm --privileged wt-test-linux \
		/root/run-tests -test.v -test.timeout 120s

test-linux-ubuntu: build-linux build-mock-agent build-linux-tests
	docker build -t wt-test-ubuntu -f test/linux/Dockerfile.ubuntu2404 test/linux/
	docker run --rm --privileged wt-test-ubuntu \
		/root/run-tests -test.v -test.timeout 120s

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
	rm -f test/linux/wt test/linux/mock-agent test/linux/run-tests
