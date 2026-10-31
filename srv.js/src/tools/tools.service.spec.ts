import { describe, expect, it } from 'vitest';

import { parseDump } from './tools.service.js';

describe('parseDump', () => {
  it('counts frame kinds, aggregates hashes and captures first frames', () => {
    const dump = [
      '<hash-declare hash="0xaaaaaaaaaaaaaaaa"/>',
      '<hash-reference hash="0xaaaaaaaaaaaaaaaa"/>',
      '<hash-reference hash="0xaaaaaaaaaaaaaaaa"/>',
      '<hash-reference hash="0xbbbbbbbbbbbbbbbb"/>',
      '<escape />',
      '<data/>',
      '<back-reference offset="3"/>',
    ].join('\n');
    const s = parseDump(dump);
    expect(s.extract).toBe(1);
    // 3 hash-reference + 1 back-reference
    expect(s.ref).toBe(4);
    expect(s.escape).toBe(1);
    expect(s.data).toBe(1);
    expect(s.dataBytes).toBe(0);
    expect(s.topHashes[0]).toEqual({ count: 3, hash: 'aaaaaaaaaaaaaaaa' });
    expect(s.firstFrames).toHaveLength(7);
    expect(s.firstFrames[0]).toEqual({
      detail: 'aaaaaaaaaaaa\u2026',
      kind: 'hash-declare',
    });
    expect(s.firstFrames[4]!.kind).toBe('escape');
    expect(s.firstFrames[6]!.kind).toBe('back-reference');
  });

  it('counts data bytes from a verbose hexdump and caps the lists', () => {
    const lines: string[] = [];
    for (let i = 0; i < 30; i++) {
      lines.push(
        '<hash-reference hash="0x' + i.toString(16).padStart(16, '0') + '"/>',
      );
    }
    lines.push('<data data="00ff"/>');
    const s = parseDump(lines.join('\n'));
    expect(s.ref).toBe(30);
    expect(s.data).toBe(1);
    expect(s.dataBytes).toBe(2);
    expect(s.firstFrames).toHaveLength(24);
    expect(s.topHashes).toHaveLength(8);
  });

  it('handles an empty dump', () => {
    const s = parseDump('');
    expect(s).toMatchObject({
      data: 0,
      escape: 0,
      extract: 0,
      firstFrames: [],
      ref: 0,
      topHashes: [],
    });
  });
});
