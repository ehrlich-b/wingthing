# Wingthing Makefile

# Variables
BINARY_NAME=wingthing
DIST_DIR=./dist
MAIN_PATH=./cmd/wingthing

# Build flags
LDFLAGS=-ldflags="-s -w"

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build: clean
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME) $(MAIN_PATH)

# Clean build artifacts
.PHONY: clean
clean:
	@rm -rf $(DIST_DIR)

# Run tests
.PHONY: test
test:
	go test ./...

# Run tests with race detection
.PHONY: race
race:
	go test -race ./...

# Run tests with verbose output
.PHONY: test-v
test-v:
	go test -v ./...

# Run tests with coverage
.PHONY: test-cover
test-cover:
	go test -cover ./...

# Run tests with race detection and verbose output
.PHONY: race-v
race-v:
	go test -race -v ./...

# Format code
.PHONY: fmt
fmt:
	go fmt ./...

# Vet code
.PHONY: vet
vet:
	go vet ./...

# Run linter (requires golangci-lint)
.PHONY: lint
lint:
	golangci-lint run

# Install dependencies
.PHONY: deps
deps:
	go mod download
	go mod tidy

# Build for multiple platforms
.PHONY: build-all
build-all: clean
	@mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)

# Development build (no optimizations)
.PHONY: dev
dev: clean
	@mkdir -p $(DIST_DIR)
	go build -o $(DIST_DIR)/$(BINARY_NAME) $(MAIN_PATH)

# Install binary to GOPATH/bin
.PHONY: install
install:
	go install $(MAIN_PATH)

# Generate mocks using mockery
.PHONY: mocks
mocks:
	@echo "Generating mocks..."
	go run github.com/vektra/mockery/v2@latest --all --keeptree --dir=./internal --output=./internal/mocks

# Clean generated mocks
.PHONY: clean-mocks
clean-mocks:
	@rm -rf ./internal/mocks

# Help target
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build       - Build the binary to ./dist/wingthing"
	@echo "  clean       - Remove build artifacts"
	@echo "  test        - Run tests"
	@echo "  race        - Run tests with race detection"
	@echo "  test-v      - Run tests with verbose output"
	@echo "  test-cover  - Run tests with coverage"
	@echo "  race-v      - Run tests with race detection and verbose output"
	@echo "  fmt         - Format code"
	@echo "  vet         - Vet code"
	@echo "  lint        - Run linter (requires golangci-lint)"
	@echo "  deps        - Download and tidy dependencies"
	@echo "  build-all   - Build for multiple platforms"
	@echo "  dev         - Development build (no optimizations)"
	@echo "  install     - Install binary to GOPATH/bin"
	@echo "  mocks       - Generate mocks using mockery"
	@echo "  clean-mocks - Remove generated mocks"
	@echo "  help        - Show this help message"
