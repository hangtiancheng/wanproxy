# wanproxy.go

A Go port of [WANProxy](https://github.com/wanproxy/wanproxy), the TCP
WAN-optimization proxy.  The migration targets the core of the C++
original — the **XCodec deduplication codec**, the **pipe protocol**
session, the **zlib composition**, the **SSH transport**, the
**XCDFS disk cache**, the **TCP/SOCKS/SSH proxies**, the **configuration
system**, the **monitor** and the **tack/xcdump/fwdproxy/diskdup
tools** —
built on the Go standard library (`net`, `compress/zlib`, `net/http`,
`crypto/*`, `encoding/binary`, `math/big`, `log/slog`).

The TypeScript sibling port (`wanproxy.js`) served as a cross-check;
where the two references disagreed, the C++ original is the source of
truth (see *Deviations*).

## Layout

| Go package / command | C++ original | Purpose |
|---|---|---|
| `xcodec/` | `xcodec/` | Rolling hash, encoder, decoder, backref window, memory/pair caches, **XCDFS disk cache** (`XCodecDisk`/`XCodecDiskCache`), frame codec, pipe protocol session (`XCodecPipePair`), streaming readers |
| `ssh/` | `ssh/` | SSH-2 transport: version exchange, KEXINIT negotiation, diffie-hellman-group-exchange (SHA-1/SHA-256, C++ test group), RFC 4253 key derivation, binary packets, ssh-rsa host keys, `SSHStream` application framing |
| `pipeline/` | `zlib/`, `programs/wanproxy/wanproxy_codec_pipe_pair.*` | Codec spec, direction/role step resolution, zlib deflate/inflate wrappers, byte-counter stats |
| `proxy/` | `programs/wanproxy/proxy_*` | TCP-TCP proxy listener, SOCKS4/5 proxy, bidirectional relay with half-close and session drain, fleet |
| `config/` | `config/`, `programs/wanproxy/wanproxy_config*` | Classic `wanproxy.conf` parser (`create`/`set`/`activate`, `$obj.member` references, quoted strings, `128MB` sizes) and the JSON format of the TypeScript port |
| `monitor/` | `programs/wanproxy/monitor_client.*` | HTTP monitor: HTML at `/`, per-object at `/object/<name>`, text under `/:text`, JSON at `/status` |
| `validation/` | (TypeScript `src/validation`) | Large-file XCodec roundtrip harness with SHA-256 verification |
| `common/` | `common/log.*` | Leveled logging with C++-style component paths |
| `cmd/wanproxy` | `programs/wanproxy` | The daemon: `wanproxy [-q \| -v] -c configfile` |
| `cmd/xcdump` | `programs/xcdump` | XCodec stream decoder and pretty-printer: `xcdump [-v] [file ...]` |
| `cmd/tack` | `programs/tack` | File-oriented dictionary compression: `tack [-p cache \| -N] [-svQ] {-c\|-d\|-h} [file ...]` |
| `cmd/fwdproxy` | `programs/fwdproxy` | File-driven plain port forwarder: `fwdproxy [-q \| -v] -c configfile` |
| `cmd/diskdup` | `programs/diskdup` | Block-level minimal-write volume duplication: `diskdup [-s block_size] source target` |

## Parity evidence

* **Hash**: `xcodec/hash_test.go` runs the C++ `xcodec-hash1`
  known-answer vectors — the digests of all 256 single-byte-filled
  segments — plus roll-vs-recompute checks.  `TestEncodeDecodeCharKAT`
  ports the C++ `xcodec-encode-decode1` roundtrip for every byte value.
* **Wire format**: `xcodec/golden_test.go` pins the exact bytes of
  ESCAPE/EXTRACT/REF/BACKREF frames and HELLO/FRAME/ASK/LEARN/ADVANCE/
  EOS/EOS_ACK control frames, including a minimal session exchange.
* **Session**: `xcodec/session_test.go` exercises the full
  HELLO/FRAME/ASK/LEARN/ADVANCE/EOS/EOS_ACK state machine with separate
  per-peer caches, protocol-violation rejections and the C++
  "gratuitous LEARN" rule.
* **Deployment**: `proxy/relay_test.go` runs a two-proxy chain
  (client-side proxy with a peer codec, server-side proxy with an
  interface codec) over real TCP with separate caches, verifying data
  integrity, the four C++ byte counters per codec, and cross-connection
  deduplication.  `e2e/` builds and runs the daemon with the classic
  config topology (SOCKS client → proxy0 → xcodec+zlib WAN → proxy1 →
  proxy-socks0 → server) and asserts the wire-byte reduction.
* **Original config**: `config/config_test.go` parses the exact
  `wanproxy.conf` shipped with the C++ implementation, including the
  SSH-SSH proxy type with PEM host keys.
* **SSH**: `ssh/transport_test.go` covers the handshake (negotiation,
  DH group exchange, key derivation, NEWKEYS switch), tagged/untagged
  application framing and 100KB encrypted roundtrips; `proxy/ssh_test.go`
  runs SSH-SSH proxy chains; `e2e/ssh_e2e_test.go` runs the daemon with
  two SSH-SSH proxies around an XCodec+zlib WAN hop.
* **XCDFS**: `xcodec/xcdfs_test.go` covers the registry, FIFO
  wraparound with index invalidation, boundary corruption checks,
  per-XUID namespace isolation across reopens, and Memory+Disk pairing.
* **Rolling hash speed**: `xcodec/bench_test.go` mirrors the C++ speed
  examples (`xcodec-hash-speed1`, `xcodec-hash-roll1`,
  `xcodec-encode-decode1`) as Go benchmarks for hash, roll, encode,
  decode and session roundtrip throughput.
* **Log masks**: `common/log_test.go` covers the C++ `Log::mask`
  semantics — ordered per-component regex masks, first match wins,
  unmatched components log, empty mask list logs everything — and
  `config/config_test.go` covers `log-mask` activation, including
  invalid-regex rejection like the C++ `regcomp` failure.
* **diskdup**: `cmd/diskdup/main_test.go` covers minimal-write
  duplication, stop-at-shorter-volume, partial trailing blocks and the
  CLI.

## Quick start

```sh
go build ./...
go test ./...

# Run the daemon with the classic C++ config format (see
# wanproxy.conf.example):
go run ./cmd/wanproxy -c wanproxy.conf.example

# Or with the JSON format of the TypeScript port:
cat > proxy.json <<'JSON'
{
  "proxies": [
    {
      "name": "local-echo",
      "listen": { "host": "127.0.0.1", "port": 3300 },
      "upstream": { "host": "127.0.0.1", "port": 3301 },
      "codec": { "mode": "zlib+xcodec", "role": "incoming", "compressorLevel": 6 }
    }
  ],
  "monitor": { "listen": { "host": "127.0.0.1", "port": 9900 } }
}
JSON
go run ./cmd/wanproxy -c proxy.json
```

Monitor endpoints: `GET /status` (JSON), `GET /` (HTML), `GET /:text`,
`GET /object/<name>`.

### SSH-SSH proxies

The classic config supports the C++ `SSH-SSH` proxy type; both hops run
the ported SSH-2 transport and the codec sits between them, exactly
like the C++ `SSHProxyListener`/`SSHProxyConnector`:

```
create proxy proxy0
set proxy0.type SSH-SSH
set proxy0.interface if0
set proxy0.interface_codec None
set proxy0.peer peer0
set proxy0.peer_codec codec0
set proxy0.server_host_key "ssh-server1.pem"
activate proxy0
```

The host key is a PEM PKCS#1 `RSA PRIVATE KEY` file (PKCS#8 is also
accepted).  As in the C++ version, the key defaults to
`ssh-server1.pem` when unset.

### tack and xcdump

```sh
go run ./cmd/tack -c -s input.file > input.tack   # compress
go run ./cmd/tack -d input.tack > input.restored  # decompress
go run ./cmd/tack -c -p dict.cache input.file > input.tack  # dictionary mode
go run ./cmd/tack -c -F fifo.xcache input.file > input.tack # XCDFS FIFO cache
go run ./cmd/tack -T -E -c -s input.file > input.tack       # with timing
go run ./cmd/xcdump -v input.tack | head          # inspect the stream
```

### diskdup

```sh
go run ./cmd/diskdup -s 65536 source.img target.img
```

Reads both volumes block by block and writes a target block only when
it differs from the source block, stopping at the end of the shorter
volume, like the C++ original.

## How the pipeline maps to C++

The C++ `WANProxyCodecPipePair` wires, per proxied connection:

```
client → upstream:  [inflate, session decode]  then  [session encode, deflate]
upstream → client:  [inflate, session decode]  then  [session encode, deflate]
```

where the first bracket applies when an *interface codec* is configured
(C++ `XCodecPipePairTypeServer`) and the second when a *peer codec* is
configured (`XCodecPipePairTypeClient`).  Data flowing **toward the WAN
is always encoded then deflated; data flowing in from the WAN is
inflated then decoded**.  The JSON `role` field selects the slot:
`incoming` → peer codec, `outgoing` → interface codec.

Protocol responses cross over inside each session exactly like the C++
pipe pair: `<ASK>`/`<ADVANCE>`/`<EOS_ACK>` produced while decoding the
peer's stream are written back on the same wire (through the same
deflate stream) their frames arrived on.  Half-close propagates with
`<EOS>`/`<EOS_ACK>` plus TCP `CloseWrite`, and each direction drains
its source socket to EOF before the relay closes anything, so final
frames are never lost to a reset.

(The TypeScript port keeps the session layer — its `CodecSession.receive`
emits the same responses — but simplifies this integration layer: decode
responses are not crossed back onto the originating wire and half-close is
FIN-based rather than `<EOS>`/`<EOS_ACK>`-based.  See
`../wanproxy.js/README.md` § "Differences from the Go port" for the exact
scope and the tradeoff.)

The identity announced in `<HELLO>` is the **cache UUID**, stable
across connections (C++ `codec_->cache()->uuid_encode`), so a
reconnecting peer reuses the decoder cache it connected before and
references resolve without new `<ASK>`/`<LEARN>` exchanges.

## Deviations from the C++ original

* **SSH NEWKEYS**: the C++ code never sends the first NEWKEYS message
  (acknowledged by two XXX comments), which leaves the data phase
  unencrypted.  This port sends NEWKEYS after key exchange as the RFC
  requires; the C++ NEWKEYS handler replies and activates keys, so the
  two implementations interoperate in both directions and Go↔Go links
  are actually encrypted.
* **SSH algorithm subset**: the C++ table entries without Go standard
  library equivalents are omitted — blowfish/cast128/idea ciphers and
  hmac-ripemd160.  The negotiated defaults (aes128-ctr, hmac-sha1,
  diffie-hellman-group-exchange-sha256) match what modern OpenSSL
  builds of the C++ version select.  The algorithm set mirrors the C++
  original and is dated by modern standards.
* **XCDFS registry write**: the C++ `registry_write` reads registry
  block N but writes the update back to block 0, corrupting the
  registry for XUIDs beyond the first 128; this port writes to the
  correct block.  Volumes using fewer than 128 peer namespaces are
  unaffected and remain interoperable.
* **XCDFS in-flight index**: as in C++, the in-flight index block is
  only written when it fills (the documented "little consistency"
  tradeoff), so up to 203 entries are lost on exit.
* **tack**: decode failures now surface as a non-zero exit code (the
  C++ version logs and exits 0); `-T/-E/-S` timing output goes to
  stderr in plain lines rather than through the C++ log format.
* **diskdup exit codes**: read/write failures and a volume ending
  mid-block exit non-zero (the C++ version logs and exits 0), matching
  the tack deviation above.
* **Not ported** (as in the TypeScript port): the IRC and HTTP protocol
  libraries, the `uinet` user-space TCP/IP stack, the event/callback
  subsystem, the `Buffer`/refcount/thread infrastructure (Go slices,
  goroutines and GC replace them), and the `websplat` demo web server.
* **log masks**: implemented with the C++ `Log::mask` semantics — an
  ordered list of component regexes where the first match decides and
  unmatched components log.  With `-q`/`-v` the daemon installs the
  command-line catch-all first, exactly like the C++ `main()`, so
  `log-mask` objects in the file cannot override the command line (as
  in C++).  Without `-q`/`-v` the configuration's per-component masks
  apply as written and a catch-all at the most detailed configured
  level is appended for components they do not match; the C++ daemon
  shadows them behind its default catch-all instead.  Level names map
  onto slog preserving what each mask admits: `EMERG`–`ERR` → Error,
  `WARNING`/`NOTICE` → Warn, `INFO` → Info, `DEBUG` → Debug.
* **XCodecCache::connect registry**: the C++ process-global UUID map is
  replaced by per-cache child maps (memory caches) and the shared XCDFS
  registry (disk caches); observable behaviour is the same.
* **SOCKS4A** is not supported, matching the C++ implementation.
