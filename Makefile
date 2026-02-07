.PHONY: build test check clean

build:
	go build -o wt ./cmd/wt

test:
	go test ./...

check: test build

clean:
	rm -f wt
