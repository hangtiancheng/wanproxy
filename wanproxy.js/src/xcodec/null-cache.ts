import type { XCodecCache } from "./cache-interface.js";

/**
 * NullCache never stores anything; it mirrors the out-of-band null cache
 * used by tack (-N) and the C++ XCodecCacheNull / Go NullCache, where the
 * decoder is expected to share the encoder's pre-populated knowledge.
 */
export class NullCache implements XCodecCache {
  public constructor(public readonly uuid: Uint8Array) {}

  public get size(): number {
    return 0;
  }

  public connect(peerId: Uint8Array): XCodecCache {
    return new NullCache(peerId);
  }

  public enter(_hash: bigint, _segment: Uint8Array): void {
    // A null cache discards every declaration.
  }

  public has(_hash: bigint): boolean {
    return false;
  }

  public lookup(_hash: bigint): Uint8Array | undefined {
    return undefined;
  }

  public replace(_hash: bigint, _segment: Uint8Array): void {
    // A null cache discards every declaration.
  }

  /** Null caches are exchanged out-of-band, like the C++ null cache. */
  public outOfBand(): boolean {
    return true;
  }
}
