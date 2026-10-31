import { describe, expect, it } from "vitest";

import { NullCache } from "@/xcodec/null-cache.js";

const UUID = new Uint8Array(16).fill(0xaa);
const PEER = new Uint8Array(16).fill(0xbb);
const SEGMENT = new Uint8Array(2048).fill(1);

describe("NullCache", () => {
  it("is out-of-band and never stores", () => {
    const cache = new NullCache(UUID);
    expect(cache.outOfBand()).toBe(true);
    expect(cache.size).toBe(0);
    cache.enter(1n, SEGMENT);
    cache.replace(1n, SEGMENT);
    expect(cache.has(1n)).toBe(false);
    expect(cache.lookup(1n)).toBeUndefined();
    expect(cache.size).toBe(0);
  });

  it("carries its identity and connects a peer null cache", () => {
    const cache = new NullCache(UUID);
    expect(cache.uuid).toEqual(UUID);
    const child = cache.connect(PEER);
    expect(child).toBeInstanceOf(NullCache);
    expect(child.uuid).toEqual(PEER);
    expect(child.outOfBand()).toBe(true);
  });
});
