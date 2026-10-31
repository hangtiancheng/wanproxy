# WANProxy demo stack — one-command orchestration for srv (Go BFF) + web (Vite).
#
#   make          start backend + frontend in one terminal (Ctrl-C stops both)
#   make help     list every target
#
# Backend  srv  → http://127.0.0.1:$(SRV_PORT)  (spawns the wanproxy.go fleet,
#                 monitor :$(MONITOR_PORT), ssh echo :$(SSH_ECHO_PORT))
# Frontend web  → http://localhost:$(WEB_PORT)

SHELL := /bin/sh
GO    ?= go

SRV_PORT     ?= 3101
WEB_PORT     ?= 5173
MONITOR_PORT ?= 9901
SSH_ECHO_PORT ?= 8349

DATA_DIR := srv/data
PID_DIR  := $(DATA_DIR)/pids
SRV_LOG  := $(DATA_DIR)/server.log
WEB_LOG  := $(DATA_DIR)/web.log
SRV_BIN  := srv/bin/wanproxy-srv

.DEFAULT_GOAL := dev

.PHONY: help dev srv web start stop restart status logs open install build test clean

help:
	@echo "WANProxy demo stack"
	@echo ""
	@echo "  make dev       srv (:$(SRV_PORT)) + web (:$(WEB_PORT)) in the foreground; Ctrl-C stops both"
	@echo "  make start     same, but backgrounded (logs+pidfiles under $(DATA_DIR))"
	@echo "  make stop      stop the background stack (port-scan fallback)"
	@echo "  make restart   stop + start"
	@echo "  make status    show listeners on $(SRV_PORT)/$(WEB_PORT)/$(MONITOR_PORT)"
	@echo "  make logs      tail srv + web background logs"
	@echo "  make open      open http://localhost:$(WEB_PORT) in the browser"
	@echo "  make srv       backend only (foreground)"
	@echo "  make web       frontend only (foreground)"
	@echo "  make install   pnpm install + build wanproxy.go tools + srv"
	@echo "  make build     build wanproxy.go + srv + web"
	@echo "  make test      go test (wanproxy.go, srv) + web build/lint"
	@echo "  make clean     remove build artifacts (runtime data in $(DATA_DIR) is kept)"

# ---------------------------------------------------------------- foreground

dev: build-srv
	@echo "==> srv   http://127.0.0.1:$(SRV_PORT)"
	@echo "==> web   http://localhost:$(WEB_PORT)"
	@echo "==> Ctrl-C stops both"
	@trap 'kill 0' EXIT INT TERM; \
	$(MAKE) srv & \
	$(MAKE) web & \
	wait

srv: build-srv
	SRV_PORT=$(SRV_PORT) MONITOR_PORT=$(MONITOR_PORT) SSH_ECHO_PORT=$(SSH_ECHO_PORT) ./$(SRV_BIN)

web:
	cd web && pnpm dev --port $(WEB_PORT)

# ---------------------------------------------------------------- background

start: build-srv
	@mkdir -p $(PID_DIR)
	@if [ -f $(PID_DIR)/srv.pid ] && kill -0 `cat $(PID_DIR)/srv.pid` 2>/dev/null; then \
	  echo "srv already running (pid `cat $(PID_DIR)/srv.pid`)"; \
	else \
	  SRV_PORT=$(SRV_PORT) MONITOR_PORT=$(MONITOR_PORT) SSH_ECHO_PORT=$(SSH_ECHO_PORT) \
	    nohup ./$(SRV_BIN) >> $(SRV_LOG) 2>&1 & echo $$! > $(PID_DIR)/srv.pid; \
	  echo "srv started (pid $$!) → http://127.0.0.1:$(SRV_PORT)  log: $(SRV_LOG)"; \
	fi
	@if [ -f $(PID_DIR)/web.pid ] && kill -0 `cat $(PID_DIR)/web.pid` 2>/dev/null; then \
	  echo "web already running (pid `cat $(PID_DIR)/web.pid`)"; \
	else \
	  cd web && nohup pnpm dev --port $(WEB_PORT) >> ../$(WEB_LOG) 2>&1 & echo $$! > $(PID_DIR)/web.pid; \
	  echo "web started (pid $$!) → http://localhost:$(WEB_PORT)  log: $(WEB_LOG)"; \
	fi

stop:
	@for f in $(PID_DIR)/srv.pid $(PID_DIR)/web.pid; do \
	  if [ -f $$f ]; then \
	    pid=`cat $$f`; \
	    kill $$pid 2>/dev/null && echo "stopped pid $$pid"; \
	    rm -f $$f; \
	  fi; \
	done
	@sleep 1
	@for p in $(SRV_PORT) $(WEB_PORT) $(MONITOR_PORT) $(SSH_ECHO_PORT); do \
	  pids=`lsof -ti tcp:$$p -s tcp:listen 2>/dev/null`; \
	  if [ -n "$$pids" ]; then echo "killing leftover listeners on :$$p ($$pids)"; kill $$pids 2>/dev/null; fi; \
	done
	@echo "stack stopped"

restart: stop start

status:
	@lsof -nP -iTCP:$(SRV_PORT) -iTCP:$(WEB_PORT) -iTCP:$(MONITOR_PORT) -sTCP:LISTEN 2>/dev/null || echo "nothing listening on $(SRV_PORT)/$(WEB_PORT)/$(MONITOR_PORT)"

logs:
	tail -n 40 -f $(SRV_LOG) $(WEB_LOG)

open:
	open http://localhost:$(WEB_PORT)

# ------------------------------------------------------------------- builds

build-srv:
	@mkdir -p srv/bin
	cd srv && $(GO) build -o bin/wanproxy-srv ./cmd/server

install:
	pnpm install
	$(MAKE) -C wanproxy.go build
	$(MAKE) build-srv

build:
	$(MAKE) -C wanproxy.go build
	$(MAKE) build-srv
	cd web && pnpm build

test:
	$(MAKE) -C wanproxy.go test
	cd srv && $(GO) vet ./... && $(GO) test ./...
	cd web && pnpm build && pnpm lint

clean:
	$(MAKE) -C wanproxy.go clean
	rm -rf srv/bin web/dist
