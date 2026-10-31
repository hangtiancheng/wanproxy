import { randomBytes } from "node:crypto";

import { copyBytes } from "./byte-array.js";
import {
  CACHE_ID_LENGTH,
  formatCacheId,
  type XCodecCache,
} from "./cache-interface.js";
import { XCODEC_SEGMENT_LENGTH } from "./constants.js";
import { formatUint64Hex } from "./uint64.js";

export interface MemoryCacheOptions {
  readonly maxSegments?: number;
  /**
   * The stable 16-byte identity exchanged in <HELLO>.  When omitted a
   * fresh random identity is generated, mirroring the C++ activation that
   * generates a UUID when the config does not specify one.
   */
  readonly uuid?: Uint8Array;
}

export class MemoryCache implements XCodecCache {
  private readonly entries = new Map<string, Uint8Array>();
  private readonly maxSegments: number | undefined;
  private readonly children = new Map<string, MemoryCache>();
  public readonly uuid: Uint8Array;

  public constructor(options: MemoryCacheOptions = {}) {
    const maxSegments = options.maxSegments;
    if (
      maxSegments !== undefined &&
      (!Number.isInteger(maxSegments) || maxSegments < 1)
    ) {
      throw new RangeError("maxSegments must be a positive integer");
    }
    this.maxSegments = maxSegments;
    this.uuid = normalizeCacheId(options.uuid);
  }

  public get size(): number {
    return this.entries.size;
  }

  /**
   * connect spins up (or reuses) a sibling memory cache of the same size
   * for the given peer identity, exactly like the C++ XCodecMemoryCache.
   * The view is memoized per peer so a reconnecting peer reuses the cache
   * it connected before, which is what makes cross-connection dedup work.
   */
  public connect(peerId: Uint8Array): XCodecCache {
    const key = formatCacheId(peerId);
    const existing = this.children.get(key);
    if (existing !== undefined) {
      return existing;
    }
    const child = new MemoryCache(
      this.maxSegments === undefined
        ? { uuid: peerId }
        : { maxSegments: this.maxSegments, uuid: peerId },
    );
    this.children.set(key, child);
    return child;
  }

  /**
   * Memory caches are not exchanged out-of-band; references must be
   * extracted in-stream, mirroring the C++ XCodecMemoryCache.
   */
  public outOfBand(): boolean {
    return false;
  }

  public enter(hash: bigint, segment: Uint8Array): void {
    const key = validateEntry(hash, segment);
    if (this.entries.has(key)) {
      throw new Error("cache entry already exists");
    }

    this.evictOldestIfFull();
    this.entries.set(key, copyBytes(segment));
  }

  public has(hash: bigint): boolean {
    return this.entries.has(formatUint64Hex(hash));
  }

  public lookup(hash: bigint): Uint8Array | undefined {
    const key = formatUint64Hex(hash);
    const segment = this.entries.get(key);
    if (segment === undefined) {
      return undefined;
    }

    this.entries.delete(key);
    this.entries.set(key, segment);
    return copyBytes(segment);
  }

  public replace(hash: bigint, segment: Uint8Array): void {
    const key = validateEntry(hash, segment);
    if (!this.entries.has(key)) {
      throw new Error("cache entry does not exist");
    }

    this.entries.delete(key);
    this.entries.set(key, copyBytes(segment));
  }

  private evictOldestIfFull(): void {
    if (
      this.maxSegments === undefined ||
      this.entries.size < this.maxSegments
    ) {
      return;
    }

    const oldestKey = this.entries.keys().next().value;
    if (oldestKey !== undefined) {
      this.entries.delete(oldestKey);
    }
  }
}

function validateEntry(hash: bigint, segment: Uint8Array): string {
  if (segment.length !== XCODEC_SEGMENT_LENGTH) {
    throw new RangeError("cache segment must be exactly 2048 bytes");
  }
  return formatUint64Hex(hash);
}

function normalizeCacheId(uuid: Uint8Array | undefined): Uint8Array {
  if (uuid === undefined) {
    return new Uint8Array(randomBytes(CACHE_ID_LENGTH));
  }
  if (uuid.length !== CACHE_ID_LENGTH) {
    throw new RangeError("cache uuid must be exactly 16 bytes");
  }
  return copyBytes(uuid);
}
