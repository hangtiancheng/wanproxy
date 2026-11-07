
GO ?= go

SRV_PORT      ?= 3101
WEB_PORT      ?= 5173
MONITOR_PORT  ?= 9901
SSH_ECHO_PORT ?= 8349

PORTS := --srv-port=$(SRV_PORT) --web-port=$(WEB_PORT) --monitor-port=$(MONITOR_PORT) --ssh-echo-port=$(SSH_ECHO_PORT)
STACK := $(GO) run ./srv.go/cmd/stack

.DEFAULT_GOAL := dev

.PHONY: help dev srv web start stop restart status logs open
.PHONY: install build build-go build-web test test-go test-web clean

help:
	@$(STACK) help $(PORTS)

dev:
	@$(STACK) dev $(PORTS)

srv:
	@$(STACK) srv $(PORTS)

web:
	@$(STACK) web $(PORTS)

start:
	@$(STACK) start $(PORTS)

stop:
	@$(STACK) stop

restart:
	@$(STACK) restart $(PORTS)

status:
	@$(STACK) status

logs:
	@$(STACK) logs

open:
	@$(STACK) open $(PORTS)

install:
	@$(STACK) install $(PORTS)

build:
	@$(STACK) build

build-go:
	@$(STACK) build-go

build-web:
	@$(STACK) build-web

test:
	@$(STACK) test

test-go:
	@$(STACK) test-go

test-web:
	@$(STACK) test-web

clean:
	@$(STACK) clean

.PHONY: commit
commit: config
	git add -A && git commit -m 'feat: Introduce new features'

.PHONY: feat
feat: commit
	git push origin main

.PHONY: config
config:
	git config user.name hangtiancheng && git config user.email '161043261@qq.com' 
