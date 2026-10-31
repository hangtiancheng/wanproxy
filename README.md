# WANProxy workspace

This workspace holds the C++ original of [WANProxy](https://github.com/wanproxy/wanproxy),
two ports, and a demo web application that exercises the Go port end to end.

| Directory      | What it is                                                                                                                                                                                                             |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `wanproxy/`    | The original C++ WANProxy (reference implementation)                                                                                                                                                                   |
| `wanproxy.js/` | TypeScript port (used as a cross-check during the Go migration)                                                                                                                                                        |
| `wanproxy.go/` | **Go port** — xcodec dedup codec, pipe protocol, zlib composition, SSH transport, XCDFS disk cache, TCP/SOCKS/SSH proxies, config system, monitor, `tack`/`xcdump`/`fwdproxy` tools. Built on the Go standard library. |
| `srv/`         | NestJS backend-for-frontend that drives the demo (spawns the daemon, runs benchmarks, serves file/monitor/tools APIs)                                                                                                  |
| `web/`         | React + TailwindCSS demo front-end                                                                                                                                                                                     |

## Demo quickstart

Prerequisites: Go ≥ 1.24, Node ≥ 22, pnpm.

```sh
# 1. Start the SRV (port 3101). On first boot it runs `make build` in
#    wanproxy.go/, builds the wanbench helper, spawns the wanproxy daemon
#    and an SSH echo server, and writes srv/data/wanproxy.conf.
cd srv && pnpm install && pnpm start:dev

# 2. Start the web app (port 5173) in a second terminal.
cd web && pnpm install && pnpm dev
```

Open http://localhost:5173.

### What the demo shows

- **Dashboard** — live proxy topology and per-proxy stats from the
  daemon's HTTP monitor (`:9901/status`), plus codec compression ratios.
- **Files** — generate test payloads (mixed / random / zero / repeat
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
| 3101               | NestJS SRV                                     |
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
