import { randomBytes } from "node:crypto";

import {
  CACHE_ID_LENGTH,
  type XCodecCache,
} from "../xcodec/cache-interface.js";
import { XCodecDisk } from "../xcodec/disk-cache.js";
import { MemoryCache } from "../xcodec/memory-cache.js";
import type { TackOptions } from "./options.js";
import { TackNullCache } from "./null-cache.js";
import { TackPersistentCache } from "./persistent-cache.js";

export interface TackCache {
  readonly cache: XCodecCache;
  close(): void;
}

/**
 * buildTackCache selects the cache backend from the tack options,
 * mirroring the C++ main(): a null cache for -N, a persistent dictionary
 * file for -p, an XCDFS FIFO disk for -F, or a plain memory cache.  A
 * fresh UUID is generated for the run, exactly as the C++ tack does.
 */
export function buildTackCache(options: TackOptions): TackCache {
  const uuid = new Uint8Array(randomBytes(CACHE_ID_LENGTH));

  if (options.nullCache) {
    return { cache: new TackNullCache(uuid), close: () => undefined };
  }
  if (options.persist !== "") {
    const writable = options.action !== "decompress";
    const cache = TackPersistentCache.open(options.persist, writable, uuid);
    return { cache, close: () => cache.close() };
  }
  if (options.fifo !== "") {
    // The XCDFS FIFO cache uses the existing file size (size 0), like the
    // C++ tack opening XCodecDisk with size 0; the file must be pre-sized.
    const disk = XCodecDisk.open(options.fifo, 0);
    return { cache: disk.local(), close: () => disk.close() };
  }
  return { cache: new MemoryCache({ uuid }), close: () => undefined };
}
