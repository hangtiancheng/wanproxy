GO ?= go

.PHONY: all build test race vet fmt clean e2e

all: build test

build:
	$(GO) build ./...
	mkdir -p bin
	$(GO) build -o bin/wanproxy ./cmd/wanproxy
	$(GO) build -o bin/xcdump ./cmd/xcdump
	$(GO) build -o bin/tack ./cmd/tack
	$(GO) build -o bin/fwdproxy ./cmd/fwdproxy
	$(GO) build -o bin/diskdup ./cmd/diskdup

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

e2e:
	$(GO) test ./e2e/ -v

clean:
	rm -rf bin
