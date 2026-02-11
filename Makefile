.PHONY: build test check clean web serve release proto

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

build:
	go build -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o wt ./cmd/wt

test:
	go test ./...

check: web test build

web:
	cd web && npm ci && npm run build

serve: build
	./wt serve

release: web
	@if [ -z "$(CINCH_TAG)" ]; then \
		echo "Error: CINCH_TAG not set (run via Cinch CI on tag push)"; \
		exit 1; \
	fi
	$(eval VERSION := $(CINCH_TAG))
	$(eval LDFLAGS := -s -w -X main.version=$(VERSION))
	@echo "Building $(CINCH_TAG) for all platforms..."
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		output="dist/wt-$$os-$$arch"; \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $$output ./cmd/wt; \
	done
	@echo "Creating release $(CINCH_TAG)..."
	cinch release dist/*

proto:
	protoc -I proto --go_out=paths=source_relative:internal/egg/pb --go-grpc_out=paths=source_relative:internal/egg/pb proto/egg.proto

clean:
	rm -f wt
	rm -rf dist/
