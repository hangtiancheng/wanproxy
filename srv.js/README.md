# srv.js

NestJS backend-for-frontend for the WANProxy demo. It is the TypeScript
counterpart of the Go `srv/` service and drives **`wanproxy.js`** (the
TypeScript proxy) instead of the Go binaries: it spawns the wanproxy.js
daemon, proxies its monitor, serves and analyzes test files, and runs the
benchmark matrix through the proxy fleet.

## Quick start

Prerequisites: Node ≥ 20, pnpm, and a built `wanproxy.js` (`pnpm --dir
../wanproxy.js build`). srv.js builds wanproxy.js automatically on first
boot if its `dist/` is missing.

```bash
pnpm install
pnpm start:dev      # or: pnpm build && pnpm start:prod
```

On boot the daemon service renders `demo/wanproxy.conf.template` into
`data/wanproxy.conf`, spawns `node ../wanproxy.js/dist/cli/index.js
data/wanproxy.conf`, and waits for the wanproxy.js monitor. Then the HTTP
API listens on `127.0.0.1:3101`.

The Vite front-end in `../web` talks to this API unchanged.

## Ports

| Port               | Role                                           |
| ------------------ | ---------------------------------------------- |
| 3101               | srv.js HTTP API (`PORT`/`SRV_PORT`)            |
| 9901               | wanproxy.js monitor (`MONITOR_PORT`)           |
| 8310 / 8311 / 8312 | passthrough / zlib / xcodec single-hop proxies |
| 8320 → 8321        | WAN chain (wan-a → wan-b, XCodec + zlib)       |
| 8351 / 8352        | zlib-b / xcodec-b server-side hops             |
| 8330               | SOCKS5 proxy                                   |

## HTTP API

| Method & path                             | Purpose                                                                       |
| ----------------------------------------- | ----------------------------------------------------------------------------- |
| `GET /`                                   | health (`{service, status}`)                                                  |
| `GET /api/files`                          | list stored files                                                             |
| `POST /api/files/generate`                | generate a deterministic payload file (`name?`, `size?`, `pattern?`, `seed?`) |
| `POST /api/files/upload?name=`            | store a raw octet-stream upload                                               |
| `GET /api/files/:id/content`              | stream a file, with HTTP `Range` support (video seeking)                      |
| `DELETE /api/files/:id`                   | delete a file                                                                 |
| `GET /api/monitor/status`                 | normalized wanproxy.js fleet status                                           |
| `GET /api/monitor/scenarios`              | the benchmark topologies                                                      |
| `GET /api/bench/scenarios`                | same scenario list                                                            |
| `POST /api/bench/run`                     | run one benchmark (`scenario`, `direction`, `pattern`, `size`, `seed`)        |
| `POST /api/bench/experiment`              | A/B experiment across `arms` vs a `baseline`                                  |
| `POST /api/bench/sink`                    | upload sink: returns `{bytes, sha256}`                                        |
| `GET /api/bench/source?size&pattern&seed` | download source: streams the payload                                          |
| `POST /api/daemon/restart`                | restart the fleet (`clearCache` wipes the XCDFS volume)                       |
| `POST /api/tools/analyze`                 | analyze a file with wanproxy.js `tack` + `xcdump`                             |

## Scenarios

`direct`, `passthrough`, `zlib-chain`, `xcodec-chain`, `wan-chain`, `socks`.
Benchmarks speak HTTP directly or through any TCP proxy, and SOCKS5 CONNECT;
integrity is verified by SHA-256 against the local deterministic generator.

## Layout

- `src/common/paths.ts` — repo/port resolution; points at the `wanproxy.js` checkout.
- `src/common/payload.ts` — deterministic payload generator (random/repeat/mixed/text), byte-identical to the Go `srv` so SHA-256 digests match across the source endpoint, file generation and benchmarks.
- `src/files` — file metadata (`data/files.json`) and streaming bodies (`data/files/<id>`).
- `src/monitor` — scenario definitions and the wanproxy.js monitor proxy (normalizes the single `codec` slot onto `interfaceCodec`/`peerCodec`).
- `src/daemon` — builds/renders/spawns/supervises the wanproxy.js daemon; `restart(clearCache)`.
- `src/bench` — single-flight benchmark runner (HTTP + SOCKS5 transports) and the A/B experiment aggregator.
- `src/tools` — `tack`/`xcdump` analysis pipeline over the wanproxy.js CLIs.
- `demo/wanproxy.conf.template` — the classic-format topology rendered at startup.

## Environment

- `PORT` / `SRV_PORT` — HTTP port (default 3101).
- `MONITOR_PORT` — wanproxy.js monitor port (default 9901).
- `SRV_ROOT` — override the resolved srv.js root (used by tests for data isolation).
- `WANPROXY_JS_ROOT` — override the wanproxy.js checkout location.
- `SRV_NO_DAEMON=1` — boot the HTTP API without spawning the daemon (tests).

## Tests

```bash
pnpm test          # unit (payload vectors vs the Go generator, monitor normalize, dump parser)
pnpm test:e2e      # boots the app (daemon disabled) and exercises the HTTP API
```

The payload unit test pins SHA-256 digests generated by the Go
`srv/cmd/wanbench gen`, so the TypeScript generator is provably
byte-identical.

## Differences from the Go srv

- Drives `wanproxy.js` (node CLIs) rather than the Go `wanproxy.go` binaries.
- The SSH-to-SSH chain scenario is included: the daemon generates a demo RSA
  host key, spawns the wanproxy.js `ssh-echo` endpoint on `:8349`, and the
  fleet runs the `proxy-ssh-a`/`proxy-ssh-b` SSH-SSH proxies with the
  XCodec+zlib codec spliced through the encrypted link. The benchmark's SSH
  transport imports the wanproxy.js SSH client (`SshTransport`/`SshStream`)
  and uses the C++ untagged `[u32 len][data]` framing, mirroring the Go bench.
- The monitor normalizes the wanproxy.js single-`codec` status slot onto the
  Go `interfaceCodec`/`peerCodec` shape (role `outgoing` → interfaceCodec,
  `incoming` → peerCodec) and passes the per-codec byte counters through
  verbatim, so benchmark `stats` (app/wire bytes and the compression ratio)
  and the experiment's wire-savings verdicts work exactly as on the Go
  stack. Codecs without `track_statistics` read as an all-zero delta and
  are treated as untracked, letting the experiment fall back to size-based
  estimates rather than reporting a misleading 100% wire reduction.
