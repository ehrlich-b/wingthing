.PHONY: build test check clean web serve

build:
	go build -o wt ./cmd/wt

test:
	go test ./...

check: test build

web:
	cd web && npm run build

serve: build
	./wt serve

clean:
	rm -f wt
