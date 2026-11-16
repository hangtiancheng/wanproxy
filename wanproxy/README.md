# wanproxy

A Go WAN optimization proxy using Go's standard library and
`golang.org/x/crypto/ssh`. It includes
XCodec deduplication, zlib composition, SSH transport, XCDFS disk caches,
TCP/UDP forwarding, SOCKS4/5 CONNECT, monitoring, and the `tack`, `xcdump`,
`fwdproxy` and `diskdup` tools.

Requires Go 1.27.4 or newer.

## Build and run

```sh
go build ./...
go test ./...
go run ./cmd/wanproxy -c examples/udp-echo.json
# Full TCP + SOCKS + UDP deployment with a memory/disk cache hierarchy:
go run ./cmd/wanproxy -c wanproxy.example.json
```

Configuration is **JSON only**. All listeners belong to one `proxies` array
and have an explicit `type`: `tcp`, `udp`, `ssh` or `socks`.

## JSON configuration and schema

[config/schema.json](config/schema.json) is a JSON Schema Draft 2020-12
schema. Add `$schema` to a configuration to enable editor completion and
validation; its path is relative to that configuration file:

```json
{
  "$schema": "../config/schema.json",
  "proxies": [
    {
      "name": "udp-echo",
      "type": "udp",
      "listen": { "host": "127.0.0.1", "port": 8300 },
      "upstream": { "host": "127.0.0.1", "port": 8301 },
      "idleTimeoutSeconds": 60,
      "maxSessions": 1024,
      "maxQueuedBytes": 8388608
    }
  ],
  "monitor": { "listen": { "host": "127.0.0.1", "port": 8404 } }
}
```

The schema describes every supported field, disallows unknown properties,
and enforces settings specific to each proxy/cache/codec type. The daemon
also checks unique proxy names, existing references, acyclic cache graphs,
Go regular expressions, cache size overflow and SSH key files. It rejects
null values, duplicate JSON properties and trailing content. It does not
fetch the `$schema` URI or depend on a schema library at runtime.

| Section       | Configuration                                                                                                                                                                                      |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `proxies`     | Nonempty array; each entry requires a unique `name`, `type` and `listen`. TCP, UDP and SSH also require `upstream`.                                                                                |
| Endpoint      | `host` and integer `port`; optional `family`: `ip` (default), `ipv4`, `ipv6`. Listen port `0` requests an ephemeral port; upstream ports must be `1..65535`.                                       |
| Unix endpoint | `{"family":"unix","path":"/tmp/proxy.sock"}`; supported by stream listeners and the monitor. UDP requires IP endpoints.                                                                            |
| `caches`      | Map of named `memory`, `disk` and `pair` caches.                                                                                                                                                   |
| `codecs`      | Map of named stream codecs; proxies reference them with `interfaceCodec` and `peerCodec`.                                                                                                          |
| `log`         | `level`: `debug`, `info` (default), `warn`, `error`; optional ordered `masks` with `regex` and `level`. First matching mask wins, then the default level applies. `-q`/`-v` override file logging. |
| `monitor`     | Optional object containing a `listen` endpoint.                                                                                                                                                    |

Names use letters, digits, `.`, `_` and `-`, beginning with a letter or
digit. Paths for disk caches, Unix sockets and SSH keys resolve relative
to the **process working directory**, or may be absolute.

### UDP forwarding

Each client IP/port gets its own connected upstream UDP socket. Datagrams
and replies retain their boundaries, including empty datagrams. IPv4 and
IPv6 are supported independently on either side. The sockets reserve a
send buffer large enough for a complete UDP payload; network and OS limits
still apply.

Defaults are 60 seconds of inactivity, 1024 sessions, and an 8 MiB total
outbound queue budget per listener. `idleTimeoutSeconds` accepts
`1..86400`, `maxSessions` accepts `1..65536`, and `maxQueuedBytes` accepts
`1..1073741824`. The byte budget spans all sessions and supplements each
session's 16-datagram limit. Accepted activity in either direction
refreshes a session. Idle sessions, including pending dials, are collected
within one additional sweep interval (at most one second).

Packets exceeding a session or queue limit are dropped; one client's
pending dial or full queue does not stop other clients. Datagram writes
have a deadline of at most one second. Session cancellation interrupts a
blocked reply before another session uses the shared socket. Packet-size
errors, temporary buffer exhaustion, and asynchronous ICMP errors preserve
the session; unsuccessful sends are dropped without retry. Recoverable
read errors use a bounded, cancellable backoff. UDP retains its normal
loss and ordering semantics.

UDP is fixed-target forwarding. Stream codecs and SSH framing are not
applied to UDP. SOCKS listeners support TCP CONNECT; SOCKS5 UDP ASSOCIATE
is not implemented.

The monitor reports UDP session counts, successful datagram/byte totals
in both directions, queued payload bytes, and observed admission, queue,
and send drops. These counters cannot measure packets lost in the network.
`Stop` cancels pending dials, closes all sockets and joins workers.

### Stream codecs and caches

A codec on `peerCodec` encodes traffic toward the upstream WAN hop and
decodes its replies. A codec on `interfaceCodec` decodes incoming WAN
traffic and encodes replies. Both slots may be set for a middle proxy.
See [examples/two-proxy-chain.json](examples/two-proxy-chain.json).

```json
{
  "caches": {
    "memory": { "type": "memory", "size": "128MB" },
    "disk": { "type": "disk", "size": "1GB", "path": "wanproxy.xcache" },
    "shared": { "type": "pair", "primary": "memory", "secondary": "disk" }
  },
  "codecs": {
    "wan": {
      "mode": "zlib+xcodec",
      "cache": "shared",
      "compressorLevel": 6,
      "trackStatistics": true
    }
  },
  "proxies": [
    {
      "name": "encode",
      "type": "tcp",
      "listen": { "host": "127.0.0.1", "port": 3300 },
      "upstream": { "host": "127.0.0.1", "port": 3301 },
      "peerCodec": "wan"
    }
  ]
}
```

Codec modes are `none`, `zlib`, `xcodec`, `zlib+xcodec`. XCodec modes
require a named cache. Zlib modes accept `compressorLevel` from `-2` to `9`
(default `-1`). `trackStatistics` enables the four codec byte counters.
Declaration order does not matter; references resolve by name.

Cache sizes are decimal bytes with optional case-insensitive
`B`/`KB`/`MB`/`GB`/`TB` suffixes, using powers of 1024. A memory cache
with omitted/zero size uses a 64 MiB payload budget. All peer namespaces
and the local encoder share one LRU and one budget. Empty peer handles
are not retained. Heap metadata is additional to the payload budget.
A disk cache with omitted/zero size
opens an existing volume at its current size. New volumes require a size.
A pair cache requires `primary` and `secondary` names and derives its
identity from the secondary. Existing disk volumes cannot be resized
implicitly. Hard-link and symlink aliases share one volume writer.
An exclusive OS file lock rejects competing daemon processes.
Overflow namespaces of a full disk registry share a bounded 64 MiB memory
fallback. Failed configuration loads close all acquired disk references.

### SSH and SOCKS

An SSH proxy uses `"type":"ssh"` with an explicit `hostKey` PEM file
(PKCS#1 RSA or PKCS#8, at least 2048 bits) and a required
`peerHostKeyFingerprint` containing the upstream server's OpenSSH SHA256
public-key fingerprint. A missing or mismatched pin fails the handshake.
Obtain the fingerprint from the upstream's trusted key:

```sh
ssh-keygen -y -f upstream-hostkey.pem > upstream-hostkey.pub
ssh-keygen -lf upstream-hostkey.pub -E sha256
```

SSH uses the maintained Go SSH implementation with its supported
algorithms, excluding its insecure algorithm set. Each connection carries
one raw stream in a `wanproxy-stream` channel; other channels and requests
are rejected. Codec slots work as for TCP. SSH listeners accept clients
without client credentials, as TCP listeners do; the pin authenticates the
upstream server. The demo renderer derives pins from its generated host key.

A SOCKS proxy uses `"type":"socks"` with a `listen` endpoint and no
`upstream` or codec slots. It supports SOCKS4 and SOCKS5 no-auth CONNECT,
with success sent after a successful target dial. SSH and SOCKS accept
`handshakeTimeoutSeconds` in `1..86400`, defaulting to 15 seconds.
SOCKS4A, BIND and UDP ASSOCIATE are not supported. Unsupported commands
receive failure replies. Handshake timeouts cover target dialing too.
Pipelined bytes enter the normal duplex relay, allowing an upstream
greeting to progress concurrently.

TCP, SSH and SOCKS listeners admit at most 1024 concurrent clients by
default. Set `maxConnections` in `1..65536` to change the limit;
connections above the limit are closed. UDP uses `maxSessions` instead.

The SSH wire format and XCodec session protocol require a coordinated
upgrade of all proxy nodes. XCodec pipe protocol version 2 rejects older
HELLO messages. Each data frame includes its plaintext length and SHA256
digest, verified before any plaintext from that frame is delivered. This
detects corrupted cache entries and rolling-hash collisions; it is an
integrity check, not peer authentication. Reference frames also carry SHA256 tags, so raw codecs and offline
`tack` dictionaries reject colliding or corrupted cached segments.
The raw `tack`/`xcdump` format also requires upgrading; existing encoded
files containing references must be regenerated. Rolling-hash vectors
remain unchanged.

## Monitor

`GET /status` returns the live fleet JSON, including `kind: "udp"`
entries. `/` shows configuration and status, `/object/<name>` selects a
named JSON object, and `/:text` exports text. Monitor object names use
`cache.<name>`, `codec.<name>` and `proxy.<name>` to separate namespaces.

## Tools

```sh
go run ./cmd/tack -c -s input.file > input.tack
go run ./cmd/tack -d input.tack > input.restored
go run ./cmd/tack -c -p dict.cache input.file > input.tack
go run ./cmd/tack -c -F fifo.xcache input.file > input.tack
go run ./cmd/xcdump -v input.tack
go run ./cmd/diskdup -s 65536 source.img target.img
go run ./cmd/fwdproxy -c examples/forwarding.json
```

`fwdproxy` uses the same JSON schema, restricted to plain TCP/UDP forwarding without
stream codecs or a monitor.

## Architecture and verification

`proxy/` owns stream relays, UDP sessions, listeners and fleet lifecycle.
`pipeline/` composes zlib and XCodec transforms. `xcodec/` contains the hash,
frame codec, bounded pipe sessions and memory/disk caches. `ssh/` implements
the single-channel SSH adapter. `config/` loads JSON, resolves named resources and owns
disk handles. `monitor/` exports configuration and runtime status.

Stream relays give socket reads, application forwarding and protocol
response writes separate goroutines with bounded queues. XCodec
ASK/LEARN traffic can progress while application writes block; ADVANCE
and EOS_ACK follow plaintext delivery. TCP half-close drains both
sources before closing sockets. Session defaults allow 8 MiB / 256
unacknowledged frames; receive buffers and relay queues have 16 MiB
limits. These are per-buffer limits, not a process memory budget.
Default outbound dialing has a 30-second timeout.

Tests cover hash known-answer vectors, exact wire
frames, cache lifecycle and corruption checks, codec chains and SSH
handshakes. Daemon end-to-end tests exercise JSON-configured TCP/SOCKS,
SSH and UDP topologies, including monitored codec reduction and UDP
empty/large datagrams. UDP tests also cover IPv4/IPv6, concurrent client
isolation, idle recreation, session/queue limits and cancellation.

```sh
go test ./...
go test -race ./...
go vet ./...
# Optional Python schema validator, isolated with uv:
make schema
```

Schema tests validate the schema itself, all example configurations and
fixtures shared with the Go parser tests. SSH depends on the Go project's
`golang.org/x/crypto/ssh` implementation.
