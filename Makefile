.PHONY: build test check clean web

build:
	go build -o wt ./cmd/wt

test:
	go test ./...

check: test build

web:
	cd web && npm run build

clean:
	rm -f wt
