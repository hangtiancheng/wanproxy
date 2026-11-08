# WANProxy workspace

This workspace holds the Go WAN optimization proxy, the demo backend and
the web front-end that drives it.

| Directory   | What it is                                                                                                                                                                                                                      |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `wanproxy/` | **Go proxy** — xcodec dedup codec, pipe protocol, zlib, SSH transport, XCDFS disk cache, TCP/UDP/SOCKS/SSH proxies, JSON configuration and schema, monitor, `tack`/`xcdump`/`fwdproxy` tools. Built on the Go standard library. |
| `srv/`      | **Go SRV** (yukino_http) — spawns the wanproxy fleet and SSH echo, and serves the file/monitor/bench/experiment/tools APIs.                                                                                                     |
| `web/`      | React + TailwindCSS demo front-end.                                                                                                                                                                                             |

## Demo quickstart

Prerequisites: Go ≥ 1.26; Node ≥ 20 and pnpm for the web front-end.

```sh
make install     # installs web dependencies and builds the Go backend
make dev         # SRV (:3101) + web (:5173), foreground
```

Open http://localhost:5173. `make start` / `make stop` run the same stack in
the background (logs and pidfiles under `srv/data/`); `make help` lists
every target. `make srv` runs only the backend, and `make web` runs only the
front-end. All targets forward to `srv/cmd/stack`, a cross-platform Go
orchestrator.

On first boot the SRV renders its `demo/wanproxy.example.json`, builds the
proxy if needed, and spawns the proxy fleet with an HTTP monitor on `:9901`
plus an SSH echo server on `:8349`.

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
  the `tack` and `xcdump` tools.

The demo covers every scenario end to end — direct / passthrough / zlib /
xcodec / WAN / SOCKS / SSH chain — with timing, throughput, SHA-256
integrity, and the per-codec wire-byte counters (`track_statistics`), so A/B
experiments report real wire savings.

### Demo port map

| Port               | Role                                           |
| ------------------ | ---------------------------------------------- |
| 5173               | Vite dev server (web)                          |
| 3101               | SRV (`srv`)                                    |
| 9901               | wanproxy daemon HTTP monitor                   |
| 8310 / 8311 / 8312 | passthrough / zlib / xcodec single-hop proxies |
| 8320 → 8321        | WAN chain (wan-a → wan-b, xcodec dedup)        |
| 8330               | SOCKS proxy                                    |
| 8340 → 8341        | SSH transport chain (ssh-a → ssh-b)            |
| 8349               | SSH echo server (wanbench)                     |

## Component details

- Go proxy: [`wanproxy/README.md`](wanproxy/README.md) — package layout,
  configuration and schema, architecture, and verification.
- Go SRV: [`srv/README.md`](srv/README.md) — the HTTP API, demo fleet
  lifecycle, and benchmark/experiment orchestration.
- Web dashboard: [`web/README.md`](web/README.md) — pages, development
  commands, and theming.
