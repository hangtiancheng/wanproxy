import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  ftruncateSync,
  openSync,
  readSync,
  writeSync,
} from "node:fs";
import { randomBytes } from "node:crypto";

import {
  CACHE_ID_LENGTH,
  formatCacheId,
  type XCodecCache,
} from "./cache-interface.js";
import { copyBytes } from "./byte-array.js";
import { XCODEC_SEGMENT_LENGTH } from "./constants.js";
import { XCodecHash } from "./hash.js";
import { formatUint64Hex } from "./uint64.js";

/*
 * The XCDFS on-disk layout, mirroring the C++ xcodec_cache_disk.cc:
 *
 *   block size            = 2048 (== XCODEC_SEGMENT_LENGTH)
 *   registry blocks       = 8     (map 1024 XUIDs -> 16-byte UUIDs)
 *   index blocks          = (disk_blocks - registry) / (1 + entries_per_index)
 *   entries per index blk = (2048 - 8) / (2 + 8) = 204
 *   data blocks           = everything after the registry and index blocks
 *
 * An index block holds a uint64 counter followed by 204 (uint16 xuid,
 * uint64 hash) entries; entry i of index block b maps to data block
 * data_block_address(b, i).  The disk is a circular FIFO of index blocks:
 * the write head advances and wraps, invalidating the entries of the block
 * it is about to overwrite.  Counters order the blocks so the head and tail
 * can be rediscovered on load, and the first/last XCDFS_CHECK_BOUNDARY
 * blocks are hash-verified on load to discard torn writes.
 */
const XCDFS_BLOCK_SIZE = 2048;
const XCDFS_ENTRIES_PER_INDEX_BLOCK = Math.floor(
  (XCDFS_BLOCK_SIZE - 8) / (2 + 8),
);
const XCDFS_XUID_LOCAL = 0;
const XCDFS_XUID_COUNT = 1024;
const XCDFS_REGISTRY_BLOCKS = Math.floor(
  (XCDFS_XUID_COUNT * CACHE_ID_LENGTH) / XCDFS_BLOCK_SIZE,
);
const XCDFS_REGISTRY_BLOCK_ENTRIES = Math.floor(
  XCDFS_BLOCK_SIZE / CACHE_ID_LENGTH,
);
const XCDFS_CHECK_BOUNDARY = 80;

/**
 * XCodecDiskCache is one front-end of the shared on-disk store, associated
 * with a single UUID/XUID.  It keeps the in-memory hash -> data block index
 * for its namespace, mirroring the C++ XCodecDiskCache.
 */
export class XCodecDiskCache implements XCodecCache {
  public readonly hashCache = new Map<string, number>();

  public constructor(
    public readonly uuid: Uint8Array,
    private readonly disk: XCodecDisk,
    public readonly xuid: number,
  ) {}

  public get size(): number {
    return this.hashCache.size;
  }

  public connect(peerId: Uint8Array): XCodecCache {
    return this.disk.connect(peerId);
  }

  public enter(hash: bigint, segment: Uint8Array): void {
    this.disk.enter(this, hash, segment);
  }

  public replace(hash: bigint, segment: Uint8Array): void {
    this.disk.remove(this, hash);
    this.disk.enter(this, hash, segment);
  }

  public has(hash: bigint): boolean {
    return this.hashCache.has(formatUint64Hex(hash));
  }

  public lookup(hash: bigint): Uint8Array | undefined {
    return this.disk.lookup(this, hash);
  }

  public touch(hash: bigint, segment: Uint8Array): void {
    this.disk.touch(this, hash, segment);
  }

  public outOfBand(): boolean {
    return false;
  }
}

/**
 * XCodecDisk is the shared on-disk backend.  Many front-ends (one per
 * UUID) store their indices here; the data blocks are content-addressed by
 * position and hash-verified on read.
 */
export class XCodecDisk {
  private readonly fd: number;
  private readonly diskBlocks: number;
  private readonly indexBlocks: number;
  private readonly xuidCacheMap = new Map<number, XCodecDiskCache>();
  private readonly uuidXuidMap = new Map<string, number>();

  private currentIndexBlock = 0;
  private indexBlock: Buffer;
  private indexBlockNext = 0;
  private indexBlockCounter = 0;

  private constructor(fd: number, diskSize: number) {
    this.fd = fd;
    this.diskBlocks = Math.floor(diskSize / XCDFS_BLOCK_SIZE);
    this.indexBlocks = Math.floor(
      (this.diskBlocks - XCDFS_REGISTRY_BLOCKS) /
        (1 + XCDFS_ENTRIES_PER_INDEX_BLOCK),
    );
    this.indexBlock = Buffer.alloc(XCDFS_BLOCK_SIZE);
  }

  /**
   * open opens (creating if necessary) the XCDFS file at path, sizing it
   * to size bytes when given (otherwise using the existing file size), and
   * loads the registry and index, mirroring XCodecDisk::open.
   */
  public static open(path: string, size: number): XCodecDisk {
    const fd = openSync(path, fsConstants.O_RDWR | fsConstants.O_CREAT, 0o700);
    try {
      const stat = fstatSync(fd);
      let diskSize = size;
      if (size !== 0 && stat.isFile()) {
        ftruncateSync(fd, size);
      }
      if (size === 0) {
        diskSize = stat.size;
        if (diskSize === 0) {
          throw new Error("could not determine disk size");
        }
      }
      const disk = new XCodecDisk(fd, diskSize);
      disk.initialize();
      return disk;
    } catch (error) {
      closeSync(fd);
      throw error;
    }
  }

  /**
   * close flushes any partially filled index block and releases the file
   * descriptor.  The C++ implementation only writes an index block once it
   * is full, discarding the trailing partial block on close; flushing it
   * here (the unused entries are zero and skipped on load) keeps the most
   * recently entered segments durable without changing the on-disk format.
   */
  public close(): void {
    if (this.indexBlockNext > 0) {
      this.blockWriteBuffer(
        this.indexBlock,
        this.indexBlockAddress(this.currentIndexBlock),
      );
      this.indexBlockNext = 0;
    }
    closeSync(this.fd);
  }

  /** local returns the front-end for the local XUID (0). */
  public local(): XCodecDiskCache {
    const cache = this.xuidCacheMap.get(XCDFS_XUID_LOCAL);
    if (cache === undefined) {
      throw new Error("XCDFS local cache is not initialized");
    }
    return cache;
  }

  /** connect returns (or allocates) the front-end for a peer UUID. */
  public connect(uuid: Uint8Array): XCodecDiskCache {
    const key = formatCacheId(uuid);
    const existingXuid = this.uuidXuidMap.get(key);
    if (existingXuid !== undefined) {
      const existing = this.xuidCacheMap.get(existingXuid);
      if (existing !== undefined) {
        return existing;
      }
    }

    let xuid = this.findFreeXuid();
    if (xuid === XCDFS_XUID_COUNT) {
      this.registryCollect();
      xuid = this.findFreeXuid();
      if (xuid === XCDFS_XUID_COUNT) {
        throw new Error("exhausted XUIDs for disk; cannot connect new UUID");
      }
    }

    this.registryWrite(xuid, uuid);
    const cache = new XCodecDiskCache(copyBytes(uuid), this, xuid);
    this.xuidCacheMap.set(xuid, cache);
    this.uuidXuidMap.set(key, xuid);
    return cache;
  }

  public enter(
    cache: XCodecDiskCache,
    hash: bigint,
    segment: Uint8Array,
  ): void {
    if (segment.length !== XCODEC_SEGMENT_LENGTH) {
      throw new RangeError("cache segment must be exactly 2048 bytes");
    }
    const position = 8 + this.indexBlockNext * 10;
    this.indexBlock.writeUInt16BE(cache.xuid, position);
    this.indexBlock.writeBigUInt64BE(hash, position + 2);

    const offset = this.dataBlockAddress(
      this.currentIndexBlock,
      this.indexBlockNext,
    );
    this.blockWriteSegment(segment, offset);
    cache.hashCache.set(formatUint64Hex(hash), offset);

    this.indexBlockNext += 1;
    if (this.indexBlockNext === XCDFS_ENTRIES_PER_INDEX_BLOCK) {
      this.flushIndexBlock();
    }
  }

  public lookup(cache: XCodecDiskCache, hash: bigint): Uint8Array | undefined {
    const key = formatUint64Hex(hash);
    const offset = cache.hashCache.get(key);
    if (offset === undefined) {
      return undefined;
    }
    const segment = this.blockReadSegment(offset);
    if (segment === undefined) {
      cache.hashCache.delete(key);
      return undefined;
    }
    if (XCodecHash.hashSegment(segment) !== hash) {
      cache.hashCache.delete(key);
      return undefined;
    }
    return segment;
  }

  public remove(cache: XCodecDiskCache, hash: bigint): void {
    cache.hashCache.delete(formatUint64Hex(hash));
  }

  public touch(
    cache: XCodecDiskCache,
    hash: bigint,
    segment: Uint8Array,
  ): void {
    if (cache.hashCache.has(formatUint64Hex(hash))) {
      return;
    }
    this.enter(cache, hash, segment);
  }

  private initialize(): void {
    if (this.indexBlocks === 0) {
      throw new Error("disk too small; reduce XCDFS_BLOCK_SIZE");
    }
    this.registryLoad();

    // Scan index block counters to find the FIFO write head and order the
    // blocks oldest-to-newest for loading.
    const counterIndexMap = new Map<bigint, number>();
    for (let o = 0; o < this.indexBlocks; o += 1) {
      const counter = this.indexReadCounter(o);
      if (counter === 0n) {
        // First free index block becomes the write head.
        counterIndexMap.set(0n, o);
        break;
      }
      if (counter >= BigInt(this.indexBlockCounter)) {
        this.indexBlockCounter = Number(counter) + 1;
      }
      counterIndexMap.set(counter, o);
    }

    const head = counterIndexMap.get(0n);
    if (head !== undefined) {
      this.currentIndexBlock = head;
      counterIndexMap.delete(0n);
    }

    // Load entries in counter order (oldest first) so the newest copy of a
    // reused hash wins, checking the boundary blocks for torn writes.
    const ordered = [...counterIndexMap.entries()].sort((a, b) =>
      a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0,
    );
    let leading = XCDFS_CHECK_BOUNDARY;
    for (const [counter, block] of ordered) {
      void counter;
      let needCheck: boolean;
      if (leading !== 0) {
        needCheck = true;
        leading -= 1;
      } else if (ordered.length <= XCDFS_CHECK_BOUNDARY) {
        needCheck = true;
      } else {
        needCheck = false;
      }
      this.indexLoadEntries(block, needCheck);
    }

    this.registryCollect();

    if (this.indexBlockCounter === 0) {
      this.indexBlockCounter = 1;
    }
    this.indexBlock = Buffer.alloc(XCDFS_BLOCK_SIZE);
    this.indexBlock.writeBigUInt64BE(BigInt(this.indexBlockCounter), 0);
  }

  private flushIndexBlock(): void {
    this.blockWriteBuffer(
      this.indexBlock,
      this.indexBlockAddress(this.currentIndexBlock),
    );
    this.currentIndexBlock = (this.currentIndexBlock + 1) % this.indexBlocks;
    this.indexBlockNext = 0;
    this.indexInvalidateEntries(this.currentIndexBlock);
    this.indexBlockCounter = this.indexBlockCounter + 1 || 1;
    this.indexBlock = Buffer.alloc(XCDFS_BLOCK_SIZE);
    this.indexBlock.writeBigUInt64BE(BigInt(this.indexBlockCounter), 0);
  }

  private dataBlockAddress(indexBlock: number, entry: number): number {
    return (
      XCDFS_REGISTRY_BLOCKS +
      this.indexBlocks +
      indexBlock * XCDFS_ENTRIES_PER_INDEX_BLOCK +
      entry
    );
  }

  private indexBlockAddress(indexBlock: number): number {
    return XCDFS_REGISTRY_BLOCKS + indexBlock;
  }

  private indexReadCounter(indexBlock: number): bigint {
    const idx = this.blockReadBuffer(this.indexBlockAddress(indexBlock));
    return idx.readBigUInt64BE(0);
  }

  private indexInvalidateEntries(indexBlock: number): void {
    const idx = this.blockReadBuffer(this.indexBlockAddress(indexBlock));
    if (idx.readBigUInt64BE(0) === 0n) {
      return;
    }
    for (let i = 0; i < XCDFS_ENTRIES_PER_INDEX_BLOCK; i += 1) {
      const position = 8 + i * 10;
      const xuid = idx.readUInt16BE(position);
      const hash = idx.readBigUInt64BE(position + 2);
      if (hash === 0n) {
        continue;
      }
      const cache = this.xuidCacheMap.get(xuid);
      if (cache === undefined) {
        continue;
      }
      const key = formatUint64Hex(hash);
      const offset = cache.hashCache.get(key);
      if (offset === undefined) {
        continue;
      }
      if (offset !== this.dataBlockAddress(indexBlock, i)) {
        continue;
      }
      cache.hashCache.delete(key);
    }
  }

  private indexLoadEntries(indexBlock: number, check: boolean): void {
    const idx = this.blockReadBuffer(this.indexBlockAddress(indexBlock));
    if (idx.readBigUInt64BE(0) === 0n) {
      return;
    }
    for (let i = 0; i < XCDFS_ENTRIES_PER_INDEX_BLOCK; i += 1) {
      const position = 8 + i * 10;
      const xuid = idx.readUInt16BE(position);
      const hash = idx.readBigUInt64BE(position + 2);
      if (hash === 0n) {
        continue;
      }
      const cache = this.xuidCacheMap.get(xuid);
      if (cache === undefined) {
        continue;
      }
      const key = formatUint64Hex(hash);
      const offset = this.dataBlockAddress(indexBlock, i);

      if (check) {
        const segment = this.blockReadSegment(offset);
        if (segment === undefined) {
          continue;
        }
        if (XCodecHash.hashSegment(segment) !== hash) {
          // Torn write: if this block is behind the write head, reset the
          // head to rewrite it; otherwise just drop the entry.
          if (this.currentIndexBlock > indexBlock) {
            this.indexInvalidateEntries(indexBlock);
            this.currentIndexBlock = indexBlock;
            return;
          }
          continue;
        }
      }
      cache.hashCache.set(key, offset);
    }
  }

  private registryLoad(): void {
    for (let r = 0; r < XCDFS_REGISTRY_BLOCKS; r += 1) {
      const reg = this.blockReadBuffer(r);
      for (let i = 0; i < XCDFS_REGISTRY_BLOCK_ENTRIES; i += 1) {
        const xuid = r * XCDFS_REGISTRY_BLOCK_ENTRIES + i;
        const uuid = new Uint8Array(
          reg.subarray(i * CACHE_ID_LENGTH, (i + 1) * CACHE_ID_LENGTH),
        );
        if (isZeroUuid(uuid)) {
          continue;
        }
        const key = formatCacheId(uuid);
        if (this.uuidXuidMap.has(key)) {
          continue;
        }
        const cache = new XCodecDiskCache(uuid, this, xuid);
        this.xuidCacheMap.set(xuid, cache);
        this.uuidXuidMap.set(key, xuid);
      }
    }

    if (this.xuidCacheMap.size === 0) {
      const uuid = new Uint8Array(randomBytes(CACHE_ID_LENGTH));
      this.registryWrite(XCDFS_XUID_LOCAL, uuid);
      const cache = new XCodecDiskCache(uuid, this, XCDFS_XUID_LOCAL);
      this.xuidCacheMap.set(XCDFS_XUID_LOCAL, cache);
      this.uuidXuidMap.set(formatCacheId(uuid), XCDFS_XUID_LOCAL);
    }
  }

  private registryCollect(): void {
    for (let xuid = 0; xuid < XCDFS_XUID_COUNT; xuid += 1) {
      const cache = this.xuidCacheMap.get(xuid);
      if (cache === undefined) {
        continue;
      }
      if (cache.hashCache.size > 0) {
        continue;
      }
      if (xuid === XCDFS_XUID_LOCAL) {
        continue;
      }
      this.registryWrite(xuid, new Uint8Array(CACHE_ID_LENGTH));
      this.uuidXuidMap.delete(formatCacheId(cache.uuid));
      this.xuidCacheMap.delete(xuid);
    }
  }

  private registryWrite(xuid: number, uuid: Uint8Array): void {
    const block = Math.floor(xuid / XCDFS_REGISTRY_BLOCK_ENTRIES);
    const reg = this.blockReadBuffer(block);
    reg.set(uuid, (xuid % XCDFS_REGISTRY_BLOCK_ENTRIES) * CACHE_ID_LENGTH);
    this.blockWriteBuffer(reg, block);
  }

  private findFreeXuid(): number {
    for (let xuid = 0; xuid < XCDFS_XUID_COUNT; xuid += 1) {
      if (!this.xuidCacheMap.has(xuid)) {
        return xuid;
      }
    }
    return XCDFS_XUID_COUNT;
  }

  private blockReadBuffer(blockno: number): Buffer {
    const block = Buffer.alloc(XCDFS_BLOCK_SIZE);
    const bytesRead = readSync(
      this.fd,
      block,
      0,
      XCDFS_BLOCK_SIZE,
      blockno * XCDFS_BLOCK_SIZE,
    );
    if (bytesRead !== XCDFS_BLOCK_SIZE) {
      // Beyond EOF reads as zeros, matching a freshly truncated file.
      block.fill(0, bytesRead < 0 ? 0 : bytesRead);
    }
    return block;
  }

  private blockWriteBuffer(block: Buffer, blockno: number): void {
    writeSync(this.fd, block, 0, XCDFS_BLOCK_SIZE, blockno * XCDFS_BLOCK_SIZE);
  }

  private blockReadSegment(blockno: number): Uint8Array | undefined {
    const segment = Buffer.alloc(XCODEC_SEGMENT_LENGTH);
    const bytesRead = readSync(
      this.fd,
      segment,
      0,
      XCODEC_SEGMENT_LENGTH,
      blockno * XCDFS_BLOCK_SIZE,
    );
    if (bytesRead !== XCODEC_SEGMENT_LENGTH) {
      return undefined;
    }
    return new Uint8Array(segment);
  }

  private blockWriteSegment(segment: Uint8Array, blockno: number): void {
    writeSync(
      this.fd,
      segment,
      0,
      XCODEC_SEGMENT_LENGTH,
      blockno * XCDFS_BLOCK_SIZE,
    );
  }
}

function isZeroUuid(uuid: Uint8Array): boolean {
  return uuid.every((byte) => byte === 0);
}
