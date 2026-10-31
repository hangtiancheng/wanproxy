import {
  closeSync,
  constants as fsConstants,
  openSync,
  readSync,
  writeSync,
} from "node:fs";

import {
  CACHE_ID_LENGTH,
  type XCodecCache,
} from "../xcodec/cache-interface.js";
import { concatByteArrays, copyBytes } from "../xcodec/byte-array.js";
import { XCODEC_SEGMENT_LENGTH } from "../xcodec/constants.js";
import { XCodecHash } from "../xcodec/hash.js";
import { MemoryCache } from "../xcodec/memory-cache.js";

/**
 * TackPersistentCache mirrors the C++ TackPersistentCache: a memory cache
 * warmed from a file that is a concatenation of raw 2048-byte segments.
 * Newly entered segments are buffered and appended to the file on close.
 *
 * The cache is out-of-band, so the encoder emits references against the
 * dictionary and the compressed stream carries no in-band declarations;
 * decompression therefore requires the same dictionary file.
 */
export class TackPersistentCache implements XCodecCache {
  private readonly mem: MemoryCache;
  private readonly fd: number;
  private readonly writable: boolean;
  private readonly newData: Uint8Array[] = [];
  private writePosition: number;
  private closed = false;

  private constructor(
    fd: number,
    writable: boolean,
    writePosition: number,
    uuid: Uint8Array,
  ) {
    this.fd = fd;
    this.writable = writable;
    this.writePosition = writePosition;
    this.mem = new MemoryCache({ uuid });
  }

  /**
   * open opens (creating when writable) the dictionary file and warms the
   * cache from its segments.  When compressing the file is opened
   * read-write so new segments can be appended; when decompressing it is
   * opened read-only, exactly like the C++ tack.
   */
  public static open(
    path: string,
    writable: boolean,
    uuid: Uint8Array = new Uint8Array(CACHE_ID_LENGTH),
  ): TackPersistentCache {
    if (uuid.length !== CACHE_ID_LENGTH) {
      throw new RangeError("cache uuid must be exactly 16 bytes");
    }
    const flags = writable
      ? fsConstants.O_RDWR | fsConstants.O_CREAT
      : fsConstants.O_RDONLY;
    const fd = openSync(path, flags, 0o600);
    const cache = new TackPersistentCache(fd, writable, 0, copyBytes(uuid));
    cache.writePosition = cache.warmLoad();
    return cache;
  }

  public get size(): number {
    return this.mem.size;
  }

  public get uuid(): Uint8Array {
    return this.mem.uuid;
  }

  public connect(_peerId: Uint8Array): XCodecCache {
    throw new Error("tack persistent cache does not support connect");
  }

  public enter(hash: bigint, segment: Uint8Array): void {
    this.enterIntoMemory(hash, segment);
    this.newData.push(copyBytes(segment));
  }

  public has(hash: bigint): boolean {
    return this.mem.has(hash);
  }

  public lookup(hash: bigint): Uint8Array | undefined {
    return this.mem.lookup(hash);
  }

  public replace(hash: bigint, segment: Uint8Array): void {
    this.enterIntoMemory(hash, segment);
  }

  public outOfBand(): boolean {
    return true;
  }

  /** close appends any newly entered segments and closes the file. */
  public close(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    if (this.writable && this.newData.length > 0) {
      const data = concatByteArrays(this.newData);
      writeSync(this.fd, data, 0, data.length, this.writePosition);
      this.writePosition += data.length;
      this.newData.length = 0;
    }
    closeSync(this.fd);
  }

  private warmLoad(): number {
    const segment = Buffer.allocUnsafe(XCODEC_SEGMENT_LENGTH);
    let position = 0;
    for (;;) {
      const bytesRead = readSync(
        this.fd,
        segment,
        0,
        XCODEC_SEGMENT_LENGTH,
        position,
      );
      if (bytesRead === 0) {
        break;
      }
      if (bytesRead < XCODEC_SEGMENT_LENGTH) {
        throw new Error("corrupt persistent cache");
      }
      const hash = XCodecHash.hashSegment(segment);
      this.enterIntoMemory(hash, new Uint8Array(segment));
      position += bytesRead;
    }
    return position;
  }

  private enterIntoMemory(hash: bigint, segment: Uint8Array): void {
    if (this.mem.has(hash)) {
      this.mem.replace(hash, segment);
    } else {
      this.mem.enter(hash, segment);
    }
  }
}
