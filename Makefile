.PHONY: build clean test fmt vet run-bot run-dream help

# Binary name
BINARY=wingthing

# Build the application
build:
	@echo "Building ${BINARY}..."
	go build -o ${BINARY} ./cmd/wingthing

# Run the bot
run-bot: build
	@echo "Starting bot..."
	./${BINARY} bot

# Run Dreams manually
run-dream: build
	@echo "Running Dreams..."
	./${BINARY} dream

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Vet code
vet:
	@echo "Vetting code..."
	go vet ./...

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f ${BINARY}
	rm -f *.log

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod download
	go mod tidy

# Show help
help:
	@echo "Available targets:"
	@echo "  build      - Build the application"
	@echo "  run-bot    - Build and run the bot"
	@echo "  run-dream  - Build and run Dreams manually"
	@echo "  test       - Run tests"
	@echo "  fmt        - Format code"
	@echo "  vet        - Vet code"
	@echo "  clean      - Remove build artifacts"
	@echo "  deps       - Install and tidy dependencies"
	@echo "  help       - Show this help message"
