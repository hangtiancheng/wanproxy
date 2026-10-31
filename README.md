# WANProxy workspace

This workspace holds the C++ original of [WANProxy](https://github.com/wanproxy/wanproxy),
two ports, and a demo web application that exercises the Go port end to end.

| Directory      | What it is                                                                                                                                                                                                             |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `wanproxy/`    | The original C++ WANProxy (reference implementation)                                                                                                                                                                   |
| `wanproxy.js/` | TypeScript port (used as a cross-check during the Go migration)                                                                                                                                                        |
| `wanproxy.go/` | **Go port** — xcodec dedup codec, pipe protocol, zlib composition, SSH transport, XCDFS disk cache, TCP/SOCKS/SSH proxies, config system, monitor, `tack`/`xcdump`/`fwdproxy` tools. Built on the Go standard library. |
| `srv.go/`      | **Go BFF** (yukino_http) that drives the demo — spawns the wanproxy.go fleet + SSH echo server, serves file/monitor/bench/experiment APIs, runs server-side benchmarks                                                 |
| `web/`         | React + TailwindCSS demo front-end                                                                                                                                                                                     |

## Demo quickstart

Prerequisites: Go ≥ 1.26, Node ≥ 22, pnpm.

```sh
pnpm install    # once, for web/
make install    # once: builds the wanproxy.go tools + the srv.go binary
make dev        # srv.go (:3101) + web (:5173) in one terminal; Ctrl-C stops both
```

Open http://localhost:5173. `make start` / `make stop` run the same stack
backgrounded (logs under `srv.go/data/`); `make help` lists every target.

On first boot srv.go runs `make build` in wanproxy.go/ (when binaries are
missing), generates a demo SSH host key, spawns the wanproxy daemon fleet
and an SSH echo server, and writes `srv.go/data/wanproxy.conf`.

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

### Demo port map

| Port               | Role                                           |
| ------------------ | ---------------------------------------------- |
| 5173               | Vite dev server (web)                          |
| 3101               | Go srv.go BFF                                  |
| 9901               | wanproxy daemon HTTP monitor                   |
| 8310 / 8311 / 8312 | passthrough / zlib / xcodec single-hop proxies |
| 8320 → 8321        | WAN chain (wan-a → wan-b, xcodec dedup)        |
| 8330               | SOCKS proxy                                    |
| 8340 → 8341        | SSH transport chain (ssh-a → ssh-b)            |
| 8349               | SSH echo server (wanbench)                     |

## Go port details

See [`wanproxy.go/README.md`](wanproxy.go/README.md) for the package
layout (C++ → Go migration map), parity evidence (KAT vectors and
golden snapshots), the pipeline mapping, and deviation notes.
