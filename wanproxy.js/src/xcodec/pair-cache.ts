import type { XCodecCache } from "./cache-interface.js";

/**
 * PairCache layers a primary cache over a secondary one, mirroring the C++
 * XCodecCachePair and the Go PairCache: entries are written to both levels,
 * lookups check the primary first and promote secondary hits into the
 * primary.
 */
export class PairCache implements XCodecCache {
  public constructor(
    private readonly primary: XCodecCache,
    private readonly secondary: XCodecCache,
  ) {}

  /** The pair inherits the (most persistent) secondary's identity. */
  public get uuid(): Uint8Array {
    return this.secondary.uuid;
  }

  public get size(): number {
    return this.primary.size + this.secondary.size;
  }

  public lookup(hash: bigint): Uint8Array | undefined {
    const primaryHit = this.primary.lookup(hash);
    if (primaryHit !== undefined) {
      // Let the secondary refresh its eviction state.
      this.secondary.touch?.(hash, primaryHit);
      return primaryHit;
    }
    const secondaryHit = this.secondary.lookup(hash);
    if (secondaryHit !== undefined) {
      enterLenient(this.primary, hash, secondaryHit);
    }
    return secondaryHit;
  }

  public enter(hash: bigint, segment: Uint8Array): void {
    enterLenient(this.primary, hash, segment);
    enterLenient(this.secondary, hash, segment);
  }

  public replace(hash: bigint, segment: Uint8Array): void {
    enterLenient(this.primary, hash, segment);
    enterLenient(this.secondary, hash, segment);
  }

  public has(hash: bigint): boolean {
    return this.primary.has(hash) || this.secondary.has(hash);
  }

  /** A pair is out-of-band only when its primary is, like the Go port. */
  public outOfBand(): boolean {
    return this.primary.outOfBand();
  }

  public connect(peerId: Uint8Array): XCodecCache {
    return new PairCache(
      this.primary.connect(peerId),
      this.secondary.connect(peerId),
    );
  }
}

/**
 * enterLenient stores a segment whether or not the hash is already present,
 * mirroring the Go MemoryCache.Enter/Replace leniency that keeps streams
 * decodable instead of asserting.
 */
function enterLenient(
  cache: XCodecCache,
  hash: bigint,
  segment: Uint8Array,
): void {
  if (cache.has(hash)) {
    cache.replace(hash, segment);
  } else {
    cache.enter(hash, segment);
  }
}
