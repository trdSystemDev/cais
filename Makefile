VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-amd64 build-arm64 test clean

build: build-amd64 build-arm64

build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/cais-linux-amd64 ./cmd/cais

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/cais-linux-arm64 ./cmd/cais

test:
	go test ./...

clean:
	rm -rf dist
