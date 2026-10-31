GO ?= go
EXE := $(shell $(GO) env GOEXE)

.PHONY: all build test race vet fmt clean e2e

all: build test

build:
	$(GO) build ./...
	mkdir -p bin
	$(GO) build -o bin/wanproxy$(EXE) ./cmd/wanproxy
	$(GO) build -o bin/xcdump$(EXE) ./cmd/xcdump
	$(GO) build -o bin/tack$(EXE) ./cmd/tack
	$(GO) build -o bin/fwdproxy$(EXE) ./cmd/fwdproxy
	$(GO) build -o bin/diskdup$(EXE) ./cmd/diskdup

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
