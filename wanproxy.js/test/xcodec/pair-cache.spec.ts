import { describe, expect, it } from "vitest";

import { MemoryCache } from "@/xcodec/memory-cache.js";
import { PairCache } from "@/xcodec/pair-cache.js";

const PRIMARY_UUID = new Uint8Array(16).fill(0x11);
const SECONDARY_UUID = new Uint8Array(16).fill(0x22);
const PEER_UUID = new Uint8Array(16).fill(0x33);

function segment(fill: number): Uint8Array {
  return new Uint8Array(2048).fill(fill);
}

function makePair(): {
  readonly pair: PairCache;
  readonly primary: MemoryCache;
  readonly secondary: MemoryCache;
} {
  const primary = new MemoryCache({ uuid: PRIMARY_UUID });
  const secondary = new MemoryCache({ uuid: SECONDARY_UUID });
  return { pair: new PairCache(primary, secondary), primary, secondary };
}

describe("PairCache", () => {
  it("inherits the secondary identity and sums sizes", () => {
    const { pair, primary, secondary } = makePair();
    expect(pair.uuid).toEqual(SECONDARY_UUID);
    primary.enter(1n, segment(1));
    secondary.enter(2n, segment(2));
    expect(pair.size).toBe(2);
  });

  it("writes to both levels on enter", () => {
    const { pair, primary, secondary } = makePair();
    pair.enter(1n, segment(7));
    expect(primary.has(1n)).toBe(true);
    expect(secondary.has(1n)).toBe(true);
    expect(pair.has(1n)).toBe(true);
  });

  it("promotes a secondary hit into the primary", () => {
    const { pair, primary, secondary } = makePair();
    secondary.enter(9n, segment(9));
    expect(primary.has(9n)).toBe(false);
    const hit = pair.lookup(9n);
    expect(hit).toEqual(segment(9));
    expect(primary.has(9n)).toBe(true);
  });

  it("returns a primary hit without re-entering", () => {
    const { pair, primary } = makePair();
    primary.enter(5n, segment(5));
    expect(pair.lookup(5n)).toEqual(segment(5));
  });

  it("reports has when either level holds the hash", () => {
    const { pair, primary, secondary } = makePair();
    primary.enter(1n, segment(1));
    secondary.enter(2n, segment(2));
    expect(pair.has(1n)).toBe(true);
    expect(pair.has(2n)).toBe(true);
    expect(pair.has(3n)).toBe(false);
  });

  it("is in-band because its primary memory cache is in-band", () => {
    const { pair } = makePair();
    expect(pair.outOfBand()).toBe(false);
  });

  it("connects a pair of per-peer child caches", () => {
    const { pair } = makePair();
    const child = pair.connect(PEER_UUID);
    expect(child).toBeInstanceOf(PairCache);
    expect(child.uuid).toEqual(PEER_UUID);
  });

  it("tolerates replace on a missing hash (lenient like Go)", () => {
    const { pair, primary, secondary } = makePair();
    expect(() => pair.replace(4n, segment(4))).not.toThrow();
    expect(primary.has(4n)).toBe(true);
    expect(secondary.has(4n)).toBe(true);
  });
});
