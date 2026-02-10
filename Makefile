.PHONY: build test check clean web serve

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o wt ./cmd/wt

test:
	go test ./...

check: test build

web:
	cd web && npm run build

serve: build
	./wt serve

clean:
	rm -f wt
