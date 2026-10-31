/**
 * The benchmark transfer engine.  It speaks plain HTTP (directly or through
 * any TCP proxy) and SOCKS5 CONNECT.  It is the TypeScript port of
 * srv/internal/bench/bench.go, minus the SSH transport (the wanproxy.js
 * runtime does not yet run SSH-SSH proxies).
 */

import { createHash } from 'node:crypto';
import http from 'node:http';
import net from 'node:net';
import type { Readable } from 'node:stream';

import {
  createPayloadStream,
  payloadSha256,
  type Pattern,
} from '../common/payload.js';

/** BenchRunResult is the outcome of one benchmark transfer. */
export interface BenchRunResult {
  durationMs: number;
  throughputMBps: number;
  sha256?: string;
  expected?: string;
  ok: boolean;
  error?: string;
}

/**
 * runBench executes one benchmark transfer.  kind selects the transport.
 * The result reports failures in-band (ok=false, error set) rather than
 * throwing, so callers can surface them inside the HTTP response.
 */
export async function runBench(
  kind: string,
  base: string | undefined,
  proxy: string | undefined,
  target: string | undefined,
  direction: string,
  pattern: Pattern,
  size: number,
  seed: number,
): Promise<BenchRunResult> {
  const start = process.hrtime.bigint();
  let sha = '';
  let error: string | undefined;
  try {
    if (kind === 'http') {
      sha = await benchHttp(base, direction, pattern, size, seed);
    } else if (kind === 'socks') {
      sha = await benchSocks(proxy, target, direction, pattern, size, seed);
    } else if (kind === 'ssh') {
      throw new Error('ssh scenario is not supported by the wanproxy.js runtime yet');
    } else {
      throw new Error('unknown scenario "' + kind + '"');
    }
  } catch (err) {
    error = err instanceof Error ? err.message : String(err);
  }
  const durationMs = Number(process.hrtime.bigint() - start) / 1e6;
  const result: BenchRunResult = {
    durationMs,
    ok: false,
    throughputMBps: durationMs > 0 ? size / (1 << 20) / (durationMs / 1000) : 0,
  };
  if (sha !== '') {
    result.sha256 = sha;
  }
  if (error !== undefined) {
    result.error = error;
    return result;
  }
  const expected = payloadSha256(pattern, size, seed);
  result.expected = expected;
  if (sha !== expected) {
    result.error =
      direction === 'upload'
        ? 'sink reported a different SHA-256'
        : 'payload SHA-256 mismatch';
    return result;
  }
  result.ok = true;
  return result;
}

// ---------------------------------------------------------------------
// HTTP transport (direct or through any TCP proxy in front of the SRV)
// ---------------------------------------------------------------------

async function benchHttp(
  base: string | undefined,
  direction: string,
  pattern: Pattern,
  size: number,
  seed: number,
): Promise<string> {
  if (base === undefined || base === '') {
    throw new Error('http scenario requires a base URL');
  }
  let last = '';
  if (direction === 'upload' || direction === 'roundtrip') {
    last = await httpUpload(base, pattern, size, seed);
  }
  if (direction === 'download' || direction === 'roundtrip') {
    last = await httpDownload(base, pattern, size, seed);
  }
  return last;
}

async function httpUpload(
  base: string,
  pattern: Pattern,
  size: number,
  seed: number,
): Promise<string> {
  const body = createPayloadStream(pattern, size, seed);
  const res = await fetch(base + '/api/bench/sink', {
    body: body as unknown as BodyInit,
    duplex: 'half',
    headers: { 'Content-Type': 'application/octet-stream' },
    method: 'POST',
  } as RequestInit);
  const text = await res.text();
  if (res.status !== 200) {
    throw new Error('sink status ' + res.status + ': ' + text.slice(0, 200));
  }
  const sink = JSON.parse(text) as { bytes: number; sha256: string };
  if (sink.bytes !== size) {
    throw new Error('sink received ' + sink.bytes + ' bytes, want ' + size);
  }
  return sink.sha256;
}

async function httpDownload(
  base: string,
  pattern: Pattern,
  size: number,
  seed: number,
): Promise<string> {
  const url =
    base +
    '/api/bench/source?size=' +
    size +
    '&seed=' +
    seed +
    '&pattern=' +
    pattern;
  const res = await fetch(url);
  if (res.status !== 200) {
    throw new Error('source status ' + res.status);
  }
  const hash = createHash('sha256');
  let received = 0;
  if (res.body !== null) {
    for await (const chunk of res.body) {
      hash.update(chunk as Uint8Array);
      received += (chunk as Uint8Array).length;
    }
  }
  if (received !== size) {
    throw new Error('downloaded ' + received + ' bytes, want ' + size);
  }
  return hash.digest('hex');
}

// ---------------------------------------------------------------------
// SOCKS5 transport
// ---------------------------------------------------------------------

async function benchSocks(
  proxyAddr: string | undefined,
  target: string | undefined,
  direction: string,
  pattern: Pattern,
  size: number,
  seed: number,
): Promise<string> {
  if (proxyAddr === undefined || proxyAddr === '' || target === undefined || target === '') {
    throw new Error('socks scenario requires a proxy and target');
  }
  let last = '';
  if (direction === 'upload' || direction === 'roundtrip') {
    last = await socksTransfer(proxyAddr, target, 'upload', pattern, size, seed);
  }
  if (direction === 'download' || direction === 'roundtrip') {
    last = await socksTransfer(proxyAddr, target, 'download', pattern, size, seed);
  }
  return last;
}

async function socksTransfer(
  proxyAddr: string,
  target: string,
  direction: 'upload' | 'download',
  pattern: Pattern,
  size: number,
  seed: number,
): Promise<string> {
  const sock = await socksConnect(proxyAddr, target);
  try {
    const [host, portStr] = splitHostPort(target);
    const port = Number.parseInt(portStr, 10);
    if (direction === 'upload') {
      const res = await httpOverSocket(
        sock,
        host,
        port,
        'POST',
        '/api/bench/sink',
        { 'Content-Type': 'application/octet-stream' },
        createPayloadStream(pattern, size, seed),
      );
      const text = await readAll(res);
      if (res.statusCode !== 200) {
        throw new Error('sink status ' + res.statusCode + ': ' + text.slice(0, 200));
      }
      const sink = JSON.parse(text) as { bytes: number; sha256: string };
      if (sink.bytes !== size) {
        throw new Error('sink received ' + sink.bytes + ' bytes, want ' + size);
      }
      return sink.sha256;
    }
    const path =
      '/api/bench/source?size=' + size + '&seed=' + seed + '&pattern=' + pattern;
    const res = await httpOverSocket(sock, host, port, 'GET', path, {});
    if (res.statusCode !== 200) {
      throw new Error('source status ' + res.statusCode);
    }
    const hash = createHash('sha256');
    let received = 0;
    for await (const chunk of res) {
      hash.update(chunk as Uint8Array);
      received += (chunk as Uint8Array).length;
    }
    if (received !== size) {
      throw new Error('downloaded ' + received + ' bytes, want ' + size);
    }
    return hash.digest('hex');
  } finally {
    sock.destroy();
  }
}

function splitHostPort(addr: string): [string, string] {
  const idx = addr.lastIndexOf(':');
  if (idx < 0) {
    throw new Error('invalid address "' + addr + '"');
  }
  return [addr.slice(0, idx), addr.slice(idx + 1)];
}

/** AsyncSock buffers socket data so readExact never loses bytes. */
class AsyncSock {
  private buf = Buffer.alloc(0);
  private waiters: Array<() => void> = [];
  private ended = false;
  private sockError: Error | undefined;

  public constructor(public readonly sock: net.Socket) {
    sock.on('data', (chunk: Buffer) => {
      this.buf = Buffer.concat([this.buf, chunk]);
      this.notify();
    });
    sock.on('end', () => {
      this.ended = true;
      this.notify();
    });
    sock.on('error', (error: Error) => {
      this.sockError = error;
      this.notify();
    });
  }

  public async readExact(n: number): Promise<Buffer> {
    for (;;) {
      if (this.sockError !== undefined) {
        throw this.sockError;
      }
      if (this.buf.length >= n) {
        const out = Buffer.from(this.buf.subarray(0, n));
        this.buf = this.buf.subarray(n);
        return out;
      }
      if (this.ended) {
        throw new Error('socks: unexpected EOF');
      }
      await new Promise<void>((resolve) => this.waiters.push(resolve));
    }
  }

  public write(data: Buffer): Promise<void> {
    return new Promise((resolve, reject) => {
      this.sock.write(data, (error) => (error ? reject(error) : resolve()));
    });
  }

  /** detach removes the buffering listeners and returns the raw socket. */
  public detach(): net.Socket {
    this.sock.removeAllListeners('data');
    this.sock.removeAllListeners('end');
    this.sock.removeAllListeners('error');
    this.sock.pause();
    return this.sock;
  }

  private notify(): void {
    const waiters = this.waiters;
    this.waiters = [];
    for (const w of waiters) {
      w();
    }
  }
}

function tcpConnect(host: string, port: number): Promise<net.Socket> {
  return new Promise((resolve, reject) => {
    const sock = net.connect({ host, port });
    sock.setTimeout(10000);
    sock.once('connect', () => {
      sock.setTimeout(0);
      resolve(sock);
    });
    sock.once('error', reject);
  });
}

/**
 * socksConnect performs a no-auth SOCKS5 handshake and CONNECT, matching the
 * wanproxy SOCKS proxy (which answers before dialing, C++ style).
 */
async function socksConnect(proxyAddr: string, target: string): Promise<net.Socket> {
  const [proxyHost, proxyPortStr] = splitHostPort(proxyAddr);
  const raw = await tcpConnect(proxyHost, Number.parseInt(proxyPortStr, 10));
  const sock = new AsyncSock(raw);
  raw.setTimeout(15000);

  await sock.write(Buffer.from([0x05, 0x01, 0x00]));
  const greeting = await sock.readExact(2);
  if (greeting[0] !== 0x05 || greeting[1] !== 0x00) {
    raw.destroy();
    throw new Error('socks greeting ' + greeting.toString('hex'));
  }

  const [host, portStr] = splitHostPort(target);
  const port = Number.parseInt(portStr, 10);
  const request: number[] = [0x05, 0x01, 0x00];
  const ipVersion = net.isIP(host);
  if (ipVersion === 4) {
    request.push(0x01);
    for (const octet of host.split('.')) {
      request.push(Number.parseInt(octet, 10));
    }
  } else if (ipVersion === 0) {
    request.push(0x03, host.length);
    request.push(...Buffer.from(host, 'utf8'));
  } else {
    raw.destroy();
    throw new Error('socks: IPv6 targets are not supported');
  }
  request.push((port >> 8) & 0xff, port & 0xff);
  await sock.write(Buffer.from(request));

  const resp = await sock.readExact(4);
  if (resp[0] !== 0x05 || resp[1] !== 0x00) {
    raw.destroy();
    throw new Error('socks connect response ' + resp.toString('hex'));
  }
  const atyp = resp[3];
  if (atyp === 0x01) {
    await sock.readExact(4 + 2);
  } else if (atyp === 0x03) {
    const lenByte = await sock.readExact(1);
    await sock.readExact(lenByte[0]! + 2);
  } else if (atyp === 0x04) {
    await sock.readExact(16 + 2);
  } else {
    raw.destroy();
    throw new Error('socks address type ' + atyp);
  }

  raw.setTimeout(0);
  return sock.detach();
}

/** httpOverSocket issues one HTTP request over an already-connected socket. */
function httpOverSocket(
  sock: net.Socket,
  host: string,
  port: number,
  method: string,
  path: string,
  headers: Record<string, string>,
  body?: Readable,
): Promise<http.IncomingMessage> {
  return new Promise((resolve, reject) => {
    const req = http.request(
      {
        createConnection: () => {
          // The socket was paused when detached from the SOCKS handshake;
          // resume it so the HTTP parser receives the response bytes.
          sock.resume();
          return sock;
        },
        headers,
        host,
        method,
        path,
        port,
      },
      (res) => resolve(res),
    );
    req.on('error', reject);
    if (body !== undefined) {
      body.pipe(req);
    } else {
      req.end();
    }
  });
}

function readAll(res: http.IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    res.on('data', (chunk: Buffer) => chunks.push(chunk));
    res.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    res.on('error', reject);
  });
}
