import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { type INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import type { App } from 'supertest/types.js';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { AppModule } from '../src/app.module.js';
import { getPaths } from '../src/common/paths.js';
import { payloadSha256 } from '../src/common/payload.js';

const here = dirname(fileURLToPath(import.meta.url));
const realWanproxyJs = join(here, '..', '..', 'wanproxy.js');

let tempRoot: string;
let app: INestApplication<App>;
let server: App;

beforeAll(async () => {
  tempRoot = mkdtempSync(join(tmpdir(), 'srvjs-e2e-'));
  process.env.SRV_ROOT = tempRoot;
  process.env.WANPROXY_JS_ROOT = realWanproxyJs;
  process.env.SRV_NO_DAEMON = '1';
  const moduleFixture = await Test.createTestingModule({
    imports: [AppModule],
  }).compile();
  app = moduleFixture.createNestApplication();
  await app.init();
  server = app.getHttpServer();
});

afterAll(async () => {
  await app?.close();
  if (tempRoot !== undefined) {
    rmSync(tempRoot, { force: true, recursive: true });
  }
  delete process.env.SRV_ROOT;
  delete process.env.WANPROXY_JS_ROOT;
  delete process.env.SRV_NO_DAEMON;
});

describe('srv.js HTTP API (e2e)', () => {
  it('GET / reports health', async () => {
    const res = await request(server).get('/').expect(200);
    expect(res.body).toEqual({ service: 'wanproxy-server', status: 'ok' });
  });

  it('GET /api/monitor/scenarios lists the demo topologies', async () => {
    const res = await request(server).get('/api/monitor/scenarios').expect(200);
    const ids = (res.body as { id: string }[]).map((s) => s.id);
    expect(ids).toEqual([
      'direct',
      'passthrough',
      'zlib-chain',
      'xcodec-chain',
      'wan-chain',
      'socks',
      'ssh-chain',
    ]);
  });

  it('GET /api/monitor/status is a bad gateway when the daemon is down', async () => {
    await request(server).get('/api/monitor/status').expect(502);
  });

  it('generates, lists, serves (with Range), analyzes and deletes a file', async () => {
    const size = 65536;
    const gen = await request(server)
      .post('/api/files/generate')
      .send({ name: 'repeat.bin', pattern: 'repeat', seed: 42, size })
      .expect(201);
    const meta = gen.body as { id: string; sha256: string; size: number };
    expect(meta.size).toBe(size);
    expect(meta.sha256).toBe(payloadSha256('repeat', size, 42));

    const list = await request(server).get('/api/files').expect(200);
    expect((list.body as { id: string }[]).some((f) => f.id === meta.id)).toBe(
      true,
    );

    const full = await request(server)
      .get('/api/files/' + meta.id + '/content')
      .expect(200);
    expect(full.headers['content-length']).toBe(String(size));
    expect(full.headers['accept-ranges']).toBe('bytes');

    const partial = await request(server)
      .get('/api/files/' + meta.id + '/content')
      .set('Range', 'bytes=0-99')
      .expect(206);
    expect(partial.headers['content-range']).toBe('bytes 0-99/' + size);
    expect(partial.headers['content-length']).toBe('100');

    const badRange = await request(server)
      .get('/api/files/' + meta.id + '/content')
      .set('Range', 'bytes=' + size + '-' + size)
      .expect(416);
    expect(badRange.headers['content-range']).toBe('bytes */' + size);

    if (existsSync(getPaths().tackCli)) {
      const analyze = await request(server)
        .post('/api/tools/analyze')
        .send({ fileId: meta.id })
        .expect(201);
      const result = analyze.body as {
        file: { size: number };
        inband: { compressedSize: number };
        frames: { extract: number };
      };
      expect(result.file.size).toBe(size);
      expect(result.inband.compressedSize).toBeGreaterThan(0);
      expect(result.frames.extract).toBeGreaterThan(0);
    }

    await request(server)
      .delete('/api/files/' + meta.id)
      .expect(204);
    await request(server)
      .get('/api/files/' + meta.id + '/content')
      .expect(404);
  });

  it('streams a deterministic payload from /api/bench/source', async () => {
    const res = await request(server)
      .get('/api/bench/source?size=4096&pattern=mixed&seed=3')
      .expect(200);
    expect(res.headers['content-type']).toContain('application/octet-stream');
    expect(res.headers['content-length']).toBe('4096');
  });

  it('rejects an invalid source size', async () => {
    await request(server).get('/api/bench/source?size=abc').expect(400);
  });

  it('hashes an uploaded body at /api/bench/sink', async () => {
    const body = Buffer.from('hello wanproxy');
    const res = await request(server)
      .post('/api/bench/sink')
      .set('Content-Type', 'application/octet-stream')
      .send(body)
      .expect(200);
    expect(res.body.bytes).toBe(body.length);
    expect(res.body.sha256).toBe(
      createHash('sha256').update(body).digest('hex'),
    );
  });

  it('stores an upload and rejects an unknown bench scenario', async () => {
    const upload = await request(server)
      .post('/api/files/upload?name=note.txt')
      .set('Content-Type', 'application/octet-stream')
      .send(Buffer.from('some text'))
      .expect(201);
    expect((upload.body as { name: string }).name).toBe('note.txt');

    await request(server)
      .post('/api/bench/run')
      .send({ scenario: 'nope' })
      .expect(404);
  });
});
