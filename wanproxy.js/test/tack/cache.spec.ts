import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { buildTackCache } from "@/tack/cache.js";
import { TackNullCache } from "@/tack/null-cache.js";
import type { TackOptions } from "@/tack/options.js";
import { MemoryCache } from "@/xcodec/memory-cache.js";
import { TackPersistentCache } from "@/tack/persistent-cache.js";

function opts(overrides: Partial<TackOptions> = {}): TackOptions {
  return {
    action: "compress",
    codecTiming: false,
    fifo: "",
    nullCache: false,
    persist: "",
    quietOutput: false,
    stats: false,
    timingEach: false,
    timingSamples: false,
    verbose: false,
    ...overrides,
  };
}

describe("buildTackCache", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "wanproxy-tack-cache-"));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("defaults to an in-band memory cache", () => {
    const built = buildTackCache(opts());
    expect(built.cache).toBeInstanceOf(MemoryCache);
    expect(built.cache.outOfBand()).toBe(false);
    expect(() => built.close()).not.toThrow();
  });

  it("builds an out-of-band null cache for -N", () => {
    const built = buildTackCache(opts({ nullCache: true }));
    expect(built.cache).toBeInstanceOf(TackNullCache);
    expect(built.cache.outOfBand()).toBe(true);
    built.close();
  });

  it("builds a writable persistent dictionary for -p", () => {
    const path = join(dir, "dict");
    const built = buildTackCache(opts({ persist: path }));
    expect(built.cache).toBeInstanceOf(TackPersistentCache);
    expect(built.cache.outOfBand()).toBe(true);
    built.close();
  });

  it("builds a read-only persistent dictionary for -p when decompressing", () => {
    const path = join(dir, "dict-ro");
    // Seed the dictionary so the read-only open has a file to load.
    const seed = buildTackCache(opts({ persist: path }));
    seed.close();
    const built = buildTackCache(opts({ action: "decompress", persist: path }));
    expect(built.cache).toBeInstanceOf(TackPersistentCache);
    built.close();
  });

  it("opens a pre-sized XCDFS FIFO disk for -F", () => {
    const path = join(dir, "fifo.xcache");
    // XCDFS opens with size 0 and uses the existing file size, so the file
    // must be pre-sized large enough for the registry plus one index block.
    writeFileSync(path, Buffer.alloc(1024 * 1024));
    const built = buildTackCache(opts({ fifo: path }));
    expect(built.cache.outOfBand()).toBe(false);
    built.close();
  });
});

describe("TackNullCache", () => {
  it("never stores and is out-of-band", () => {
    const cache = new TackNullCache();
    expect(cache.size).toBe(0);
    expect(cache.outOfBand()).toBe(true);
    cache.enter(1n, new Uint8Array(2048));
    cache.replace(1n, new Uint8Array(2048));
    expect(cache.has(1n)).toBe(false);
    expect(cache.lookup(1n)).toBeUndefined();
  });

  it("connects a peer null cache and validates the uuid length", () => {
    const peer = new Uint8Array(16).fill(0x42);
    const cache = new TackNullCache(peer);
    expect(cache.uuid).toEqual(peer);
    const child = cache.connect(new Uint8Array(16).fill(0x99));
    expect(child).toBeInstanceOf(TackNullCache);
    expect(() => new TackNullCache(new Uint8Array(8))).toThrow(RangeError);
  });
});
