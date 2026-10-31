import {
  CACHE_ID_LENGTH,
  type XCodecCache,
} from "../xcodec/cache-interface.js";
import { copyBytes } from "../xcodec/byte-array.js";

/**
 * TackNullCache mirrors the C++ TackNullCache: an out-of-band cache that
 * never stores anything.  It is used by `tack -N`, where the decoder is
 * expected to share the encoder's pre-populated knowledge, so the encoder
 * emits references and the compressed stream carries no in-band
 * declarations.
 */
export class TackNullCache implements XCodecCache {
  public readonly uuid: Uint8Array;

  public constructor(uuid: Uint8Array = new Uint8Array(CACHE_ID_LENGTH)) {
    if (uuid.length !== CACHE_ID_LENGTH) {
      throw new RangeError("cache uuid must be exactly 16 bytes");
    }
    this.uuid = copyBytes(uuid);
  }

  public get size(): number {
    return 0;
  }

  public connect(peerId: Uint8Array): XCodecCache {
    return new TackNullCache(peerId);
  }

  public enter(_hash: bigint, _segment: Uint8Array): void {
    // A null cache stores nothing.
  }

  public has(_hash: bigint): boolean {
    return false;
  }

  public lookup(_hash: bigint): Uint8Array | undefined {
    return undefined;
  }

  public replace(_hash: bigint, _segment: Uint8Array): void {
    // A null cache stores nothing, so there is nothing to replace.
  }

  public outOfBand(): boolean {
    return true;
  }
}
