# WANProxy demo stack — thin wrapper around boot.js.
#
# All startup logic (Go stack: srv.go + wanproxy.go, JS stack: srv.js +
# wanproxy.js, plus the Vite web front-end) lives in boot.js.  Every target
# here just forwards to `node boot.js <command>`.
#
#   make [STACK=go|js]   default target: dev (backend + web, foreground)
#   make help            list every target

SHELL := /bin/sh
BOOT  := node boot.js

STACK ?= go

SRV_PORT      ?= 3101
WEB_PORT      ?= 5173
MONITOR_PORT  ?= 9901
SSH_ECHO_PORT ?= 8349

PORTS := --srv-port=$(SRV_PORT) --web-port=$(WEB_PORT) --monitor-port=$(MONITOR_PORT) --ssh-echo-port=$(SSH_ECHO_PORT)

.DEFAULT_GOAL := dev

.PHONY: help dev dev-go dev-js srv srv-go srv-js web
.PHONY: start start-go start-js stop restart status logs open
.PHONY: install build build-go build-js test test-go test-js clean

help:
	@$(BOOT) help

dev:
	@$(BOOT) dev --stack=$(STACK) $(PORTS)

dev-go:
	@$(BOOT) dev --stack=go $(PORTS)

dev-js:
	@$(BOOT) dev --stack=js $(PORTS)

srv:
	@$(BOOT) srv --stack=$(STACK) $(PORTS)

srv-go:
	@$(BOOT) srv --stack=go $(PORTS)

srv-js:
	@$(BOOT) srv --stack=js $(PORTS)

web:
	@$(BOOT) web $(PORTS)

start:
	@$(BOOT) start --stack=$(STACK) $(PORTS)

start-go:
	@$(BOOT) start --stack=go $(PORTS)

start-js:
	@$(BOOT) start --stack=js $(PORTS)

stop:
	@$(BOOT) stop $(PORTS)

restart:
	@$(BOOT) restart --stack=$(STACK) $(PORTS)

status:
	@$(BOOT) status $(PORTS)

logs:
	@$(BOOT) logs --stack=$(STACK)

open:
	@$(BOOT) open $(PORTS)

install:
	@$(BOOT) install

build:
	@$(BOOT) build --stack=$(STACK)

build-go:
	@$(BOOT) build --stack=go

build-js:
	@$(BOOT) build --stack=js

test:
	@$(BOOT) test --stack=$(STACK)

test-go:
	@$(BOOT) test --stack=go

test-js:
	@$(BOOT) test --stack=js

clean:
	@$(BOOT) clean
