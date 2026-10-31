# WANProxy workspace

This workspace holds the C++ original of
[WANProxy](https://github.com/wanproxy/wanproxy) plus **two complete ports** —
Go and TypeScript — of both the proxy and the demo backend-for-frontend, and a
shared web front-end that drives either one.

| Directory      | What it is                                                                                                                                                                                                                                                             |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `wanproxy/`    | The original C++ WANProxy (reference implementation)                                                                                                                                                                                                                   |
| `wanproxy.go/` | **Go proxy port** — xcodec dedup codec, pipe protocol, zlib, SSH transport, XCDFS disk cache, TCP/SOCKS/SSH proxies, config system, monitor, `tack`/`xcdump`/`fwdproxy` tools. Built on the Go standard library.                                                       |
| `wanproxy.js/` | **TypeScript proxy port** — the same feature set on Node built-ins (`net`/`zlib`/`crypto`/`http`): classic `wanproxy.conf` + JSON config, XCDFS disk cache, memory/pair/null caches, SSH-2 transport, TCP/SOCKS proxies, monitor, and `tack`/`xcdump`/`fwdproxy` CLIs. |
| `srv.go/`      | **Go SRV** (yukino_http) — spawns the wanproxy.go fleet + SSH echo, serves the file/monitor/bench/experiment/tools APIs.                                                                                                                                               |
| `srv.js/`      | **NestJS SRV** — the TypeScript counterpart, driving wanproxy.js: spawns the daemon and serves the same file/monitor/bench/experiment/tools APIs.                                                                                                                      |
| `web/`         | React + TailwindCSS demo front-end (works with either SRV).                                                                                                                                                                                                            |
| `boot.js`      | Stack orchestrator: builds, runs, stops and tests either backend + web. The `Makefile` is a thin wrapper over it.                                                                                                                                                      |

## Demo quickstart

Prerequisites: Node ≥ 20 and pnpm (both stacks); Go ≥ 1.26 (Go stack only).

```sh
pnpm install     # once: installs the JS workspace (wanproxy.js, srv.js, web)
make install     # builds both backends (Go tools + srv.go, wanproxy.js + srv.js)

# TypeScript stack (srv.js + wanproxy.js)
make dev-js      # NestJS SRV (:3101) + web (:5173), foreground

# Go stack (srv.go + wanproxy.go)
make dev-go      # yukino_http SRV (:3101) + web (:5173), foreground
```

Open http://localhost:5173. `make` defaults to the Go stack; pass `STACK=js`
(e.g. `make STACK=js dev`) or use the explicit `-js` targets to select the
TypeScript stack. `make start[-go|-js]` / `make stop` run the same stack
backgrounded (logs and pidfiles under `<srv>/data/`); `make help` lists every
target. All orchestration lives in `boot.js` — the Makefile only forwards to
`node boot.js <command>`.

On first boot the SRV renders its `demo/wanproxy.conf.template`, builds the
proxy if needed, and spawns the proxy fleet with an HTTP monitor on `:9901`
(both stacks also start an SSH echo server on `:8349`).

### What the demo shows

- **Dashboard** — live proxy topology and per-proxy stats from the
  daemon's HTTP monitor (`:9901/status`), plus codec compression ratios.
- **A/B Experiment** — multi-arm controlled comparisons (direct vs
  passthrough / zlib / XCodec / WAN / SOCKS / SSH chains) over identical
  deterministic payloads, with per-iteration warm-up curves, wire-byte
  verdicts vs baseline, and fleet cache cold-restart control.
- **Files** — generate test payloads (mixed / repeat / text / random
  patterns), upload them through the SRV (raw octet-stream, sha256
  verified), download with HTTP Range support (video seeking), and
  preview video in-browser.
- **Scenarios** — one-click benchmarks across the transfer matrix:
  direct passthrough, zlib codec, xcodec codec, a simulated WAN chain
  (wan-a → wan-b with xcodec + dedup caches), SOCKS, and an SSH
  transport chain (ssh-a → ssh-b). Repeat-pattern payloads over the WAN
  chain show xcodec deduplication collapsing megabytes of application
  bytes into kilobytes on the wire.
- **Inspector** — frame-level analysis of captured XCDFS streams using
  the ported `tack` and `xcdump` tools.

Stack coverage: both stacks now cover every demo scenario end to end —
direct / passthrough / zlib / xcodec / WAN / SOCKS / SSH chain — with
timing, throughput, SHA-256 integrity, and the per-codec wire-byte counters
(`track_statistics`), so A/B experiments report real wire savings on either
stack. The JS stack runs the SSH chain over the wanproxy.js SSH-SSH proxy
(codec spliced through the encrypted link) and its `ssh-echo` endpoint; see
[`wanproxy.js/README.md`](wanproxy.js/README.md) (§ Differences from the Go
port, § Residual Gaps) for the one remaining protocol-level difference
between the stacks and the tradeoff behind it.

### Demo port map

| Port               | Role                                           |
| ------------------ | ---------------------------------------------- |
| 5173               | Vite dev server (web)                          |
| 3101               | SRV (`srv.go` or `srv.js`)                     |
| 9901               | wanproxy daemon HTTP monitor                   |
| 8310 / 8311 / 8312 | passthrough / zlib / xcodec single-hop proxies |
| 8320 → 8321        | WAN chain (wan-a → wan-b, xcodec dedup)        |
| 8330               | SOCKS proxy                                    |
| 8340 → 8341        | SSH transport chain (ssh-a → ssh-b)            |
| 8349               | SSH echo server (wanbench / ssh-echo)          |

## Port details

- Go proxy: [`wanproxy.go/README.md`](wanproxy.go/README.md) — package layout
  (C++ → Go migration map), parity evidence (KAT vectors and golden
  snapshots), pipeline mapping, and deviation notes.
- TypeScript proxy: [`wanproxy.js/README.md`](wanproxy.js/README.md) — the
  classic `wanproxy.conf` parser, XCDFS disk cache, SSH transport, monitor
  endpoints, the `tack`/`xcdump`/`fwdproxy` CLIs, and a faithful account of
  where its relay differs from the Go port (crossed decode responses and
  half-close semantics) and why.
- NestJS SRV: [`srv.js/README.md`](srv.js/README.md) — the HTTP API,
  wanproxy.js integration, and its differences from the Go srv.

### Cross-stack parity

The Go and TypeScript ports are golden-tested to be byte-identical at the
codec/session layer, and every demo scenario runs end to end on both stacks
with comparable A/B numbers. The single remaining protocol-level difference
lives in the TypeScript relay integration layer: crossed decode responses
(`<ASK>`/`<LEARN>`/`<ADVANCE>`/`<EOS_ACK>`) are not routed back to the
originating wire, and half-close is FIN-based rather than
`<EOS>`/`<EOS_ACK>`-based. This is a deliberate, documented tradeoff rooted
in the Node stream model; it does not affect any shipped scenario (the demo
fleet shares one cache, so no `<ASK>`/`<LEARN>` exchange ever fires). See
[`wanproxy.js/README.md`](wanproxy.js/README.md#differences-from-the-go-port)
for the full analysis.
