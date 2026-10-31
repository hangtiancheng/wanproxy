import { createHash } from 'node:crypto';

import { describe, expect, it } from 'vitest';

import {
  createPayloadStream,
  parsePattern,
  PayloadReader,
  payloadSha256,
} from './payload.js';

// Reference digests produced by the Go generator
// (srv/cmd/wanbench gen | sha256sum).  The TypeScript port must match
// byte-for-byte so the SRV, bench source and file generation agree.
const VECTORS = [
  ['mixed', 1048576, 42, '2b6402950610b17db847a7524417bbc73b157caad5b0ee294f9fb8fe92078de2'],
  ['mixed', 16777216, 7, '4ead5522f25caf738794c95b841b27d373f476d892f00b6dee13fc4e1a743a1e'],
  ['random', 65536, 42, '09a583233550509615882d5b31270150c7d35d7b93287616f862833dce8bba0f'],
  ['repeat', 1048576, 42, '63daee8d1eba26cf1e7e375891878e49840180410687348be6a353a73352e171'],
  ['text', 1048576, 42, '426be7c2cd80cd09294a792ed30e7f6d7950a3f37917c9274af2b026b4638df2'],
  ['text', 4194304, 42, 'c4f9b03ab79008a726da223036c6008acf537aca5889e30c54c07f7cf93643f5'],
] as const;

describe('payload generator', () => {
  it.each(VECTORS)(
    'matches the Go digest for %s size=%d seed=%d',
    (pattern, size, seed, digest) => {
      expect(payloadSha256(pattern, size, seed)).toBe(digest);
    },
  );

  it('is deterministic and seed-sensitive', () => {
    expect(payloadSha256('text', 1 << 20, 42)).toBe(
      payloadSha256('text', 1 << 20, 42),
    );
    expect(payloadSha256('text', 1 << 20, 42)).not.toBe(
      payloadSha256('text', 1 << 20, 43),
    );
  });

  it('streams the same bytes as the synchronous reader', async () => {
    const stream = createPayloadStream('mixed', 200000, 5);
    const hash = createHash('sha256');
    let bytes = 0;
    for await (const chunk of stream) {
      hash.update(chunk as Buffer);
      bytes += (chunk as Buffer).length;
    }
    expect(bytes).toBe(200000);
    expect(hash.digest('hex')).toBe(payloadSha256('mixed', 200000, 5));
  });

  it('returns 0 at EOF and honors partial reads', () => {
    const reader = new PayloadReader('random', 10, 1);
    const buf = new Uint8Array(4);
    expect(reader.read(buf)).toBe(4);
    expect(reader.read(buf)).toBe(4);
    expect(reader.read(buf)).toBe(2);
    expect(reader.read(buf)).toBe(0);
  });

  it('validates pattern names', () => {
    expect(parsePattern('mixed')).toBe('mixed');
    expect(() => parsePattern('nope')).toThrow(/unknown pattern/);
  });
});
