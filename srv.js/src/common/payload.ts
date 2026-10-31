/**
 * payload implements the deterministic benchmark payload generator.  It is
 * byte-identical to srv/internal/payload/payload.go so that the SRV, the
 * /api/bench/source endpoint and the /api/files/generate endpoint all agree
 * on SHA-256 digests without exchanging the payload.
 *
 * LCG: x = (x*1103515245 + 12345) mod 2^31; byte = (x >> 16) & 0xff.  The
 * mod-2^31 of the true product equals (product mod 2^32) & 0x7fffffff, which
 * is what Math.imul reproduces here.
 */

import { createHash } from 'node:crypto';
import { Readable } from 'node:stream';

/** Pattern selects how the payload bytes are produced. */
export type Pattern = 'random' | 'repeat' | 'mixed' | 'text';

/** BLOCK_SIZE is the 4096-byte block granularity of repeat/mixed. */
export const BLOCK_SIZE = 4096;

const PATTERNS: readonly Pattern[] = ['random', 'repeat', 'mixed', 'text'];

/** parsePattern validates a pattern name. */
export function parsePattern(value: string): Pattern {
  if ((PATTERNS as readonly string[]).includes(value)) {
    return value as Pattern;
  }
  throw new Error(
    `unknown pattern "${value}" (want random|repeat|mixed|text)`,
  );
}

// The text pattern mimics an application log stream: long runs of identical,
// highly compressible vocabulary lines punctuated by unique lines carrying
// sequence numbers, trace tokens and latencies.  Every 40-line period holds
// 30 fixed vocabulary lines followed by 10 unique lines.
const TEXT_VOCABULARY: readonly string[] = [
  'level=INFO msg="request completed" method=GET path=/api/v1/users status=200 bytes=1843',
  'level=INFO msg="cache hit" store=redis key=session.ttl hit=true latency_us=142',
  'level=INFO msg="db query executed" table=orders rows=37 latency_ms=3 plan=index_scan',
  'level=WARN msg="retrying upstream call" upstream=payments attempt=2 backoff_ms=250',
  'level=INFO msg="request completed" method=POST path=/api/v1/orders status=201 bytes=921',
  'level=INFO msg="health check ok" component=api uptime_s=86413 goroutines=214',
  'level=INFO msg="static asset served" path=/assets/app.css status=304 etag=W/"9f2a"',
  'level=ERROR msg="connection reset by peer" remote=10.4.2.18:52413 read_bytes=40960',
  'level=INFO msg="background job finished" queue=emails job=send_digest duration_ms=812',
  'level=INFO msg="rate limiter state" client_id=web-frontend remaining=412 window_s=60',
  'level=DEBUG msg="tls handshake complete" version=TLS1.3 cipher=TLS_AES_128_GCM_SHA256',
  'level=INFO msg="metrics flushed" series=1204 endpoint=prometheus:9090 duration_ms=44',
];

const TEXT_HANDLERS: readonly string[] = [
  'ListUsers',
  'GetOrder',
  'CreateOrder',
  'SearchItems',
  'UpdateCart',
  'Checkout',
  'RefreshSession',
  'UploadAsset',
];

/** Lcg is the deterministic byte generator shared by every pattern. */
class Lcg {
  private x: number;

  public constructor(seed: number) {
    this.x = Number(BigInt(Math.trunc(seed)) & 0x7fffffffn);
  }

  public next(): number {
    this.x = (Math.imul(this.x, 1103515245) + 12345) & 0x7fffffff;
    return (this.x >>> 16) & 0xff;
  }

  public fillBlock(block: Uint8Array): void {
    for (let i = 0; i < block.length; i++) {
      block[i] = this.next();
    }
  }
}

/**
 * PayloadReader streams a deterministic payload of a fixed size.  read()
 * fills the supplied buffer and returns the number of bytes written, or 0
 * once size bytes have been produced (EOF), mirroring the Go io.Reader.
 */
export class PayloadReader {
  private off = 0;
  private readonly g: Lcg;
  private readonly blockA?: Uint8Array;
  private readonly blockB?: Uint8Array;
  private readonly blockC?: Uint8Array;
  private readonly fresh?: Uint8Array;
  private textLine = new Uint8Array(0);
  private textPos = 0;
  private textSeq = 0;

  public constructor(
    private readonly pattern: Pattern,
    private readonly size: number,
    seed: number,
  ) {
    this.g = new Lcg(seed);
    if (pattern === 'repeat') {
      this.blockA = new Uint8Array(BLOCK_SIZE);
      this.g.fillBlock(this.blockA);
    } else if (pattern === 'mixed') {
      this.blockA = new Uint8Array(BLOCK_SIZE);
      this.blockB = new Uint8Array(BLOCK_SIZE);
      this.blockC = new Uint8Array(BLOCK_SIZE);
      this.g.fillBlock(this.blockA);
      this.g.fillBlock(this.blockB);
      this.g.fillBlock(this.blockC);
      this.fresh = new Uint8Array(BLOCK_SIZE);
    }
  }

  /** read fills p with the next payload bytes, returning the count (0 = EOF). */
  public read(p: Uint8Array): number {
    if (this.off >= this.size) {
      return 0;
    }
    let n = 0;
    while (n < p.length && this.off < this.size) {
      switch (this.pattern) {
        case 'random':
          p[n] = this.g.next();
          n++;
          this.off++;
          break;
        case 'repeat':
          n += this.copyBlock(p, n, this.blockA!);
          break;
        case 'mixed':
          n += this.copyBlock(p, n, this.mixedSource());
          break;
        case 'text':
          while (n < p.length && this.off < this.size) {
            if (this.textPos === this.textLine.length) {
              this.fillTextLine();
            }
            let c = Math.min(p.length - n, this.textLine.length - this.textPos);
            if (this.off + c > this.size) {
              c = this.size - this.off;
            }
            p.set(this.textLine.subarray(this.textPos, this.textPos + c), n);
            this.textPos += c;
            n += c;
            this.off += c;
          }
          break;
      }
    }
    return n;
  }

  /** mixedSource picks the block for the current offset, refreshing fresh. */
  private mixedSource(): Uint8Array {
    const chunk = Math.floor(this.off / BLOCK_SIZE);
    const pos = this.off % BLOCK_SIZE;
    const m = chunk % 10;
    if (m === 3 || m === 7 || m === 9) {
      if (pos === 0) {
        this.g.fillBlock(this.fresh!);
      }
      return this.fresh!;
    }
    const r = chunk % 3;
    return r === 0 ? this.blockA! : r === 1 ? this.blockB! : this.blockC!;
  }

  /** copyBlock copies from src at the current block offset into p. */
  private copyBlock(p: Uint8Array, n: number, src: Uint8Array): number {
    const pos = this.off % BLOCK_SIZE;
    let c = Math.min(p.length - n, BLOCK_SIZE - pos);
    if (this.off + c > this.size) {
      c = this.size - this.off;
    }
    p.set(src.subarray(pos, pos + c), n);
    this.off += c;
    return c;
  }

  private fillTextLine(): void {
    const seq = this.textSeq;
    this.textSeq++;
    this.textPos = 0;
    const slot = seq % 40;
    if (slot >= 30) {
      const tok = [
        this.g.next(),
        this.g.next(),
        this.g.next(),
        this.g.next(),
      ];
      const latency = this.g.next() * 4 + this.g.next();
      const handler = TEXT_HANDLERS[slot % TEXT_HANDLERS.length]!;
      const trace = tok
        .map((b) => b.toString(16).padStart(2, '0'))
        .join('');
      const c1 = this.g.next();
      const c2 = this.g.next();
      const c3 = this.g.next();
      const mm = String(Math.floor(seq / 60) % 60).padStart(2, '0');
      const ss = String(seq % 60).padStart(2, '0');
      const ms = String(seq % 1000).padStart(3, '0');
      const line =
        `2026-01-15T10:${mm}:${ss}.${ms}Z seq=${seq} level=INFO ` +
        `msg="request handled" handler=${handler} duration_ms=${latency} ` +
        `trace=${trace} client=10.${c1}.${c2}.${c3}\n`;
      this.textLine = Buffer.from(line, 'utf8');
      return;
    }
    const line = TEXT_VOCABULARY[slot % TEXT_VOCABULARY.length]! + '\n';
    this.textLine = Buffer.from(line, 'utf8');
  }
}

/** payloadSha256 regenerates the payload and returns its hex digest. */
export function payloadSha256(
  pattern: Pattern,
  size: number,
  seed: number,
): string {
  const hash = createHash('sha256');
  const reader = new PayloadReader(pattern, size, seed);
  const buf = new Uint8Array(65536);
  for (;;) {
    const n = reader.read(buf);
    if (n <= 0) {
      break;
    }
    hash.update(buf.subarray(0, n));
  }
  return hash.digest('hex');
}

const STREAM_CHUNK = 65536;

/** PayloadStream wraps a PayloadReader as a Node byte Readable. */
export class PayloadStream extends Readable {
  private readonly reader: PayloadReader;

  public constructor(pattern: Pattern, size: number, seed: number) {
    super();
    this.reader = new PayloadReader(pattern, size, seed);
  }

  public override _read(): void {
    const buf = Buffer.alloc(STREAM_CHUNK);
    const n = this.reader.read(buf);
    if (n <= 0) {
      this.push(null);
      return;
    }
    this.push(n === buf.length ? buf : buf.subarray(0, n));
  }
}

/** createPayloadStream builds a Readable streaming the whole payload. */
export function createPayloadStream(
  pattern: Pattern,
  size: number,
  seed: number,
): Readable {
  return new PayloadStream(pattern, size, seed);
}
