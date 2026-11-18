
GO ?= go

SERVER_PORT   ?= 3101
CLIENT_PORT   ?= 5173
MONITOR_PORT  ?= 9901
SSH_ECHO_PORT ?= 8349

PORTS := --server-port=$(SERVER_PORT) --client-port=$(CLIENT_PORT) --monitor-port=$(MONITOR_PORT) --ssh-echo-port=$(SSH_ECHO_PORT)
STACK := $(GO) run ./server/cmd/stack

.DEFAULT_GOAL := dev

.PHONY: help dev server client start stop restart status logs open
.PHONY: install build build-go build-client test test-go test-client clean

help:
	@$(STACK) help $(PORTS)

dev:
	@$(STACK) dev $(PORTS)

server:
	@$(STACK) server $(PORTS)

client:
	@$(STACK) client $(PORTS)

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

build-client:
	@$(STACK) build-client

test:
	@$(STACK) test

test-go:
	@$(STACK) test-go

test-client:
	@$(STACK) test-client

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
