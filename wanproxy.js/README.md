# wanproxy-js

TypeScript migration of the core WANProxy TCP path. The implementation covers a local TCP proxy, both the classic `wanproxy.conf` and JSON configuration formats, zlib composition, XCodec framing, and the XCodec pipe protocol session.

## Status

- `M0` through `M12` are implemented and validated.
- The current runtime covers XCodec, pipe protocol sessions, zlib pipeline composition, TCP proxying, SOCKS proxying, monitor status, persistent XCodec cache, and large-file correctness validation.
- Both configuration formats are supported (`src/config`): the classic C++ `wanproxy.conf` directive format (`create`/`set`/`activate` with `$object.member` references, quoted strings, `128MB` sizes, Memory/Disk/Pair caches, TCP-TCP/SSH-SSH proxies) and the JSON format. The loader auto-detects the format.
- The monitor serves the C++ endpoints (`src/monitor`): HTML at `/`, per-object views at `/object/<name>`, text under `/:text`, and JSON at `/status`, exporting the activated config objects.
- The SSH-2 transport layer is migrated (`src/ssh`): identification exchange, KEXINIT negotiation, diffie-hellman-group-exchange key exchange with an `ssh-rsa` host key, AES-CTR/CBC encryption, HMAC integrity, and the binary packet protocol, plus an SSH tunnel proxy (`SshTunnelClient`/`SshTunnelServer`).
- The XCDFS on-disk cache format is migrated (`src/xcodec/disk-cache.ts`): registry blocks, counter-ordered index blocks, circular FIFO data blocks, and per-XUID front-ends.
- Pair and null caches (`src/xcodec/pair-cache.ts`, `src/xcodec/null-cache.ts`), the four codec byte counters (`src/pipeline/codec-stats.ts`, counted at the relay's four codec boundaries and exported by the monitor `/status` payload), and leveled logging (`src/common/log.ts`) are migrated.
- The `tack`, `xcdump`, and `fwdproxy` command-line programs are migrated (see below).
- The `tack` timing flags (`-T`/`-E`/`-S`) are ported, and SSH host keys load from PEM via `RsaHostKey.fromPrivatePem`.

### Parity fixes

The following behaviors were corrected to match the C++ original (cross-checked against the Go port):

- The `outgoing` codec role now resolves encode/decode in the correct direction for every mode (previously reversed), following the C++ `WANProxyCodecPipePair` toward-the-WAN ordering.
- `<HELLO>` carries the cache's stable UUID instead of a per-connection random id, and the decoder connects a memoized per-peer cache view, so cross-connection deduplication works without an `<ASK>`/`<LEARN>` storm.
- The deflate stage ends every batch with `Z_SYNC_FLUSH` (like the C++ `DeflatePipe` and the Go wire writer) so compressed bytes reach the wire immediately instead of stalling.
- TCP teardown half-closes and drains both sockets instead of abortively destroying them, so a trailing `<EOS>` is not lost to a reset with unread data outstanding.

## Use Cases

- Local TCP proxying for forwarding one local port to an upstream TCP service.
- Codec-path validation with XCodec, zlib, or combined pipeline modes.
- SOCKS4/SOCKS5 CONNECT proxying for clients that can select the destination at connect time.
- Runtime introspection through the monitor's HTML, text, and JSON endpoints.
- XCodec correctness verification with deterministic tests, golden wire snapshots, and large-file SHA-256 roundtrip validation.

## Command-Line Programs

The C++ `programs/` tools are migrated as CLIs (built to `dist/cli/`):

- `tack` — file-oriented XCodec dictionary compression (`tack [-p cache | -F fifo | -N] [-svQ] [-T [-ES]] {-c|-d|-h} [file ...]`). `-c` compresses, `-d` decompresses, `-h` prints per-segment hashes; `-p` uses a persistent dictionary file, `-F` an XCDFS disk, `-N` a null cache; `-T` times codec calls, `-E` times each file, `-S` prints individual samples.
- `xcdump` — XCodec stream decoder/pretty-printer (`xcdump [-v] [file ...]`), emitting `<data/>`, `<escape />`, `<hash-declare/>`, `<hash-reference/>`, and `<back-reference/>` elements.
- `fwdproxy` — simple file-driven port forwarder (`fwdproxy [-q | -v] -c configfile`), reading the C++ columnar `interface-host interface-port remote-host remote-port` format.

Run them after `pnpm build`, e.g. `node dist/cli/tack.js -c -s input.bin > out.tack`.

## Demos

Runnable demo sources and configs live under `src/demo/`:

- `src/demo/tcp-pass-through.json`: TCP pass-through with monitor status.
- `src/demo/tcp-xcodec-echo.json`: XCodec encode/decode path using a local echo server.
- `src/demo/socks-with-monitor.json`: SOCKS5 CONNECT proxy with monitor status.
- `src/demo/echo-server.ts`: local TCP echo server used by the demos.
- `src/demo/tcp-client.ts`: simple TCP client for pass-through and XCodec demos.
- `src/demo/socks5-client.ts`: simple SOCKS5 CONNECT client.

After `pnpm build`, run demo scripts from `dist/demo/*.js`. See `src/demo/README.md` for step-by-step commands.

## Quick Start

Install dependencies:

```bash
pnpm install
```

Build the project:

```bash
pnpm build
```

Create `proxy.json`:

```json
{
  "proxies": [
    {
      "name": "local-echo",
      "listen": { "host": "127.0.0.1", "port": 3300 },
      "upstream": { "host": "127.0.0.1", "port": 3301 },
      "codec": { "mode": "none", "role": "incoming" }
    }
  ]
}
```

Run the configured proxy:

```bash
node dist/cli/index.js proxy.json
```

The CLI also accepts:

```bash
node dist/cli/index.js --config proxy.json
```

## Configuration

Configuration is parsed at the file boundary in one of two formats (see below). The JSON form is validated through `zod`; invalid shape, empty proxy lists, invalid ports, unknown codec modes, and invalid roles fail fast before any listener starts. The classic form is validated by the directive parser with C++-compatible error messages. Both produce the same runtime model consumed by `RuntimeFleet`.

Top-level schema:

```json
{
  "proxies": [
    {
      "name": "proxy-name",
      "listen": { "host": "127.0.0.1", "port": 0 },
      "upstream": { "host": "127.0.0.1", "port": 8080 },
      "codec": {
        "mode": "none",
        "role": "incoming",
        "compressorLevel": 6
      }
    }
  ]
}
```

Codec fields:

- `mode`: `none`, `zlib`, `xcodec`, or `zlib+xcodec`; defaults to `none`.
- `role`: `incoming` or `outgoing`; defaults to `incoming`.
- `compressorLevel`: optional zlib level from `-1` through `9`.

### Format detection

`loadConfig`/`parseConfig` (`src/config/load.ts`) auto-detect the format: a document whose first non-blank, non-comment byte is `{` is parsed as JSON, anything else as the classic directive format.

### Classic `wanproxy.conf` format

The classic C++ directive format is fully supported (`src/config/classic.ts`): `create <class> <name>`, `set <name>.<member> <value>`, `activate <name>`, with `#` comments, quoted strings, `$object.member` references, and `128MB`-style sizes. Recognized classes: `log-mask`, `cache` (`Memory`/`Disk`/`Pair`/`None`), `codec` (`XCodec` + `zlib` compressor + level + `track_statistics`), `interface`/`peer` (`IP`/`IPv4`/`IPv6`/`Unix`), `proxy` (`TCP-TCP`/`SSH-SSH`), `proxy-socks`, and `monitor`. Example:

```
create cache mem0
set mem0.type Memory
set mem0.size 128MB
activate mem0

create codec codec0
set codec0.codec XCodec
set codec0.cache mem0
set codec0.compressor zlib
set codec0.compressor_level 6
activate codec0

create interface if0
set if0.family IP
set if0.host "127.0.0.1"
set if0.port "3300"
activate if0

create peer peer0
set peer0.family $if0.family
set peer0.host $if0.host
set peer0.port "3301"
activate peer0

create proxy proxy0
set proxy0.type TCP-TCP
set proxy0.interface if0
set proxy0.interface_codec None
set proxy0.peer peer0
set proxy0.peer_codec codec0
activate proxy0
```

## Architecture

- `src/common`: leveled logging with C++-style component paths (`src/common/log.ts`).
- `src/config`: configuration — the classic `wanproxy.conf` directive parser, the JSON schemas, the unified runtime model, size parsing, and the format-detecting loader.
- `src/proxy`: TCP listener, upstream connector, bidirectional relay, the JSON `ProxyFleet`, and the `RuntimeFleet` that runs the unified model.
- `src/monitor`: monitor HTTP interface — HTML at `/`, per-object views at `/object/<name>`, text under `/:text`, and JSON at `/status`. TCP proxies report their codec slot with the Go-compatible statistics fields (`trackStatistics`, `stats` with the four boundary counters) when the codec was activated with `track_statistics`.
- `src/pipeline`: explicit stream pipeline builder for pass-through, zlib, XCodec session, and combined modes, plus the four codec byte counters (`codec-stats.ts`).
- `src/pipe-protocol`: HELLO, FRAME, ASK, LEARN, ADVANCE, EOS, and EOS_ACK session protocol.
- `src/xcodec`: pure XCodec hash, frame primitives, memory/persistent/pair/null caches, XCDFS disk cache, backref window, encoder, decoder, and the `xcdump` stream dumper.
- `src/ssh`: SSH-2 transport layer — protocol primitives, crypto (DH, AES-CTR/CBC, HMAC, RSA host key incl. PEM loading), algorithm negotiation, session key derivation, packet framing, and the handshake state machine.
- `src/tack`: the `tack` dictionary compressor core, argument parsing (incl. `-T`/`-E`/`-S` timing), and its null/persistent cache backends.
- `src/fwdproxy`: the columnar `fwdproxy` configuration parser.
- `src/validation`: large-file XCodec correctness harness and stream hashing helpers.
- `src/cli`: command-line entry points — `wanproxy` (configured TCP proxies), `tack`, `xcdump`, and `fwdproxy`.
- `src/proxy` also provides `SshTunnelClient`/`SshTunnelServer`, which relay a local byte stream to a target over an encrypted SSH transport.

The TCP relay uses manual bidirectional `pipe()` chains instead of `stream/promises.pipeline()` because duplex sockets must preserve half-close behavior while the opposite direction flushes pending data.

When a codec is activated with `track_statistics`, the relay wraps each direction with counting pass-throughs at the socket boundaries, mirroring the Go relay's count callbacks: bytes read from the local socket (`incoming_to_codec`), bytes written to the upstream socket (`codec_to_outgoing`), and the symmetric receive direction (`outgoing_to_codec` / `codec_to_incoming`). The positions hold for both chain sides because a proxy's single codec always sits between its local and upstream sockets, so the monitor `/status` payload reports the same counters the Go fleet does.

## Development

Run the standard validation set:

```bash
pnpm check
pnpm typecheck
pnpm test
pnpm test:coverage
pnpm build
```

Coverage uses the V8 provider with global thresholds:

- Lines: at least `90%`.
- Statements: at least `90%`.
- Branches: at least `85%`.

Coverage HTML output is written under `coverage/`.

## Golden Snapshots

Golden wire snapshots live in `test/golden/wire-snapshots.spec.ts`. They pin representative byte encodings for:

- XCodec escape, ref, and backref frames.
- Pipe-protocol HELLO, FRAME, ASK, ADVANCE, EOS, and EOS_ACK frames.
- A minimal `CodecSession.encodeData()` exchange.

Update these snapshots only when the wire format intentionally changes.

## Large-File Validation

`large-file.js` is the repeatable XCodec correctness harness for large payloads. It builds the project, creates a fixture with `src/cli/create-large-file.ts`, streams the file through XCodec encode and decode transforms from `src/validation`, and compares SHA-256 hashes without loading the whole file into memory.

Run the default 3 GiB verification:

```bash
node large-file.js
```

Useful options:

- `--size-gb=N`: fixture size in GiB; defaults to `3`.
- `--file=PATH`: fixture path relative to the project root; defaults to `large-xcodec-fixture.bin`.
- `--force`: recreate the fixture even when a same-size file already exists.
- `--skip-build`: reuse existing `dist/` output.
- `--workers=N`: worker count for fixture generation.
- `--chunk-mib=N`: write chunk size for fixture generation.
- `--cache-segments=N`: in-memory XCodec cache size during verification; defaults to `4096`.
- `--json`: print a machine-readable success summary.

Example quick smoke run:

```bash
node large-file.js --size-gb=1 --file=large-file-smoke.bin --force --skip-build --json
```

Clean up generated fixtures:

```bash
rm -f large-xcodec-fixture.bin large-file-smoke.bin large-file.log
```

## Troubleshooting

- If the CLI exits with a usage error, pass exactly one positional config path or `--config <path>`; the file may be a classic `wanproxy.conf` or JSON.
- If configuration parsing fails, validate port ranges, non-empty host strings, codec mode names, and role names (JSON), or the `create`/`set`/`activate` directive syntax, class names, and `$ref` targets (classic).
- If a TCP test hangs, check for accepted sockets that were not destroyed before server shutdown.
- If echo traffic returns empty data, verify both listener and upstream sockets are created with half-open behavior.
- If zlib plus XCodec fails to round-trip, check the pipeline role and direction ordering first.
- If large-file validation fails before hashing, run `pnpm build` or remove `--skip-build`.
- If large-file validation reuses stale data, pass `--force` or delete the fixture.

## Residual Gaps

- Full C++ protocol compatibility has not been certified against a live C++ WANProxy peer.
- The SSH transport is migrated and self-tested (client/server handshake and encrypted tunnel), but has not been certified for interoperability against a live OpenSSH or C++ WANProxy peer; it offers a focused algorithm suite (diffie-hellman-group-exchange-sha256/sha1, ssh-rsa, aes128/256-ctr/cbc, hmac-sha1/sha2-256/sha2-512) and does not implement user authentication or SSH channels.
- The SSH tunnel proxy carries a raw byte stream over the transport; it does not yet compose the XCodec/zlib codec pipeline inside the SSH link the way the C++ `SSHProxyConnector` splices a codec pipe pair through `SSHStream`.
- SSH-SSH proxies and proxies that combine an interface and a peer codec parse and validate from the classic config, but `RuntimeFleet` does not yet run them (it raises a clear error); the common single-codec-per-proxy WAN topology is fully supported.
- Unix-domain-socket endpoints parse from the classic config, but the runtime fleet listens on TCP only.
