# srv.go — Go SRV for the wanproxy demo

Go SRV for the WANProxy demo, built on
[`yukino_http`](https://github.com/hangtiancheng/yukino.go/tree/main/libs/yukino_http).
One process does everything:

1. spawns the **wanproxy.go demo fleet** (single daemon, 11 proxy listeners)
   plus an in-process **SSH echo server**,
2. serves the HTTP API for the web dashboard on `127.0.0.1:3101`,
3. runs server-side benchmarks (`wanbench`) through the proxy chains,
4. tears everything down on SIGINT/SIGTERM — no orphaned children.

## Run

```bash
cd srv.go
go run ./cmd/server          # or: make build && ./bin/wanproxy-srv
```

First start builds `wanproxy.go/bin/{wanproxy,tack,xcdump}` via `make build`
if missing, generates a demo-only SSH host key, and renders
`demo/wanproxy.example.json` into `data/wanproxy.json`.

Environment overrides: `SRV_PORT` (3101), `MONITOR_PORT` (9901),
`SSH_ECHO_PORT` (8349), `SRV_ROOT`.

## Ports

| Port               | Role                                                                  |
| ------------------ | --------------------------------------------------------------------- |
| 3101               | srv HTTP API + benchmark origin (all proxies ultimately forward here) |
| 9901               | wanproxy monitor (`/status` JSON)                                     |
| 8310               | TCP passthrough proxy (no codec — control baseline)                   |
| 8311 → 8351        | zlib compression chain (zlib-a → zlib-b)                              |
| 8312 → 8352        | XCodec dedup chain (xcodec-a → xcodec-b)                              |
| 8320 → 8321        | WAN optimization chain (wan-a → wan-b, XCodec + zlib)                 |
| 8330               | SOCKS5 CONNECT proxy                                                  |
| 8340 → 8341 → 8349 | SSH chain (ssh-a → ssh-b → in-process SSH echo)                       |

## API

| Method & path                                   | Purpose                                                                                       |
| ----------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `GET /api/files`                                | list stored files                                                                             |
| `POST /api/files/generate`                      | deterministic LCG test file `{name,size,pattern,seed}`                                        |
| `POST /api/files/upload?name=`                  | streamed `application/octet-stream` upload                                                    |
| `GET /api/files/:id/content`                    | download, HTTP Range (206) supported                                                          |
| `DELETE /api/files/:id`                         | remove file                                                                                   |
| `GET /api/monitor/status`                       | proxied daemon fleet status                                                                   |
| `GET /api/monitor/scenarios`                    | scenario catalog (alias `/api/bench/scenarios`)                                               |
| `POST /api/bench/run`                           | single scenario benchmark `{scenario,direction,pattern,size,seed}`                            |
| `POST /api/bench/experiment`                    | multi-arm A/B experiment (below)                                                              |
| `POST /api/bench/sink`, `GET /api/bench/source` | raw upload/download endpoints used by the benchmark runner                                    |
| `POST /api/daemon/restart`                      | `{clearCache}` — restart the fleet; `true` also wipes `data/wanproxy.xcache` for a cold start |
| `POST /api/tools/analyze`                       | run `tack -c`, `tack -p`, `xcdump -v` on a stored file                                        |

Bench and experiment share a single-flight lock: concurrent requests get
`409 Conflict`.

### A/B experiments

`POST /api/bench/experiment`:

```json
{
  "arms": ["direct", "wan-chain"],
  "baseline": "direct",
  "direction": "download",
  "pattern": "mixed",
  "size": 16777216,
  "seed": 42,
  "iterations": 5,
  "isolateArms": true
}
```

- Every arm transfers the **identical deterministic payload** the same number
  of times; each transfer is SHA-256 verified.
- Wire bytes come from the wanproxy codec counters (monitor). Arms without a
  codec (`direct`, `passthrough`, `socks`) report `wire = app = size ×
multiplier` (multiplier is 2 for `roundtrip`, else 1).
- `isolateArms` (default **true**) gives arm _i_ the seed
  `seed + i·7919`, so the fleet-wide shared dedup cache cannot leak warm
  state across arms — a fair A/B. Disable it to observe shared-cache
  cross-chain effects.
- The report aggregates per-arm totals (mean/median, throughput, wire/app
  bytes) and per-arm verdicts vs the baseline (wire reduction %, bytes
  saved, throughput speedup, time saved).

### Payload patterns

| Pattern  | Content                                                  |
| -------- | -------------------------------------------------------- |
| `mixed`  | 70% duplicate 4 KB blocks + 30% pseudo-random (VM image) |
| `repeat` | one 4 KB block repeated (maximum dedup)                  |
| `text`   | compressible log stream with repeating runs              |
| `random` | pure pseudo-random (incompressible honesty check)        |

All patterns are seeded LCG streams, byte-identical across runs and across
the file generator, benchmark runner and experiments.

## Layout

```
cmd/server      main: daemon + services + yukino_http app
cmd/wanbench    standalone benchmark CLI (same runner as the API)
cmd/stack       cross-platform demo orchestrator used by the top-level Makefile
internal/paths  repo/port resolution (SRV_ROOT, data/, ports)
internal/daemon fleet lifecycle: build bins, host key, config render,
                spawn wanproxy, SSH echo, Restart(clearCache)
internal/files  file storage + metadata (data/files, data/files.json)
internal/monitor daemon /status proxy + codec counter extraction
internal/bench  wanbench runner (HTTP/SOCKS/SSH drivers)
internal/benchsvc single-flight bench service + experiment orchestration
internal/payload  deterministic pattern generators (mixed/repeat/text/random)
internal/tools  tack/xcdump analysis of stored files
internal/server yukino_http routes + CORS/logging middleware
```

Runtime state lives in `data/` (files, config, host key, XCDFS disk cache,
`server.log`).

## Test

```bash
go build ./... && go vet ./... && go test ./...
```
