/**
 * XCodecCache is the segment store used by the encoder and decoder,
 * mirroring the C++ XCodecCache interface.
 *
 * Every cache carries a stable 16-byte identity (the C++ UUID) that is
 * exchanged in the pipe protocol <HELLO> message.  Because the identity
 * is stable across connections, a peer that reconnects to the same cache
 * reuses the decoder cache it connected before, so references resolve
 * without new <ASK>/<LEARN> exchanges.
 */
export interface XCodecCache {
  readonly size: number;

  /** The stable 16-byte identity exchanged in <HELLO> (C++ uuid_). */
  readonly uuid: Uint8Array;

  /**
   * connect returns the cache view used to decode a stream whose <HELLO>
   * carried the given peer identity, mirroring the C++
   * XCodecCache::connect behaviour.  Implementations memoize the view per
   * peer identity so that a reconnecting peer reuses its earlier cache.
   */
  connect(peerId: Uint8Array): XCodecCache;

  enter(hash: bigint, segment: Uint8Array): void;
  has(hash: bigint): boolean;
  lookup(hash: bigint): Uint8Array | undefined;
  replace(hash: bigint, segment: Uint8Array): void;

  /**
   * touch hints that a segment was used, letting e.g. a secondary cache
   * refresh its eviction state.  Optional; the default is a no-op,
   * mirroring the C++ XCodecCache::touch default and the Go Cache.Touch.
   */
  touch?(hash: bigint, segment: Uint8Array): void;

  /**
   * outOfBand reports whether declarations may be exchanged out-of-band.
   * For an out-of-band cache the encoder emits references instead of
   * in-stream <EXTRACT> declarations, relying on the decoder sharing the
   * same pre-populated cache (the C++ XCodecCache::out_of_band, used by
   * tack's null and persistent caches).
   */
  outOfBand(): boolean;
}

/** CACHE_ID_LENGTH is the byte length of a cache identity (C++ UUID_SIZE). */
export const CACHE_ID_LENGTH = 16;

/** formatCacheId renders a cache identity as lowercase hex. */
export function formatCacheId(id: Uint8Array): string {
  return Buffer.from(id).toString("hex");
}
