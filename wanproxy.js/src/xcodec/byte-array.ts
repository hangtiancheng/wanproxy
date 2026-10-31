export function concatByteArrays(parts: readonly Uint8Array[]): Uint8Array {
  const length = parts.reduce((sum, part) => sum + part.length, 0);
  const output = new Uint8Array(length);
  let offset = 0;
  for (const part of parts) {
    output.set(part, offset);
    offset += part.length;
  }
  return output;
}

export function byteArraysEqual(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) {
    return false;
  }
  return left.every((byte, index) => byte === right[index]);
}

/**
 * copyBytes returns a standalone copy of source.
 *
 * This must be used instead of `source.slice()` whenever the bytes are a
 * segment that may be retained: a Node Buffer's slice()/subarray() return a
 * VIEW that aliases the original memory, so retaining it across a reused
 * read buffer (as tack does) would silently corrupt the stored segment.
 * `new Uint8Array(source)` always copies, for both Buffer and Uint8Array.
 */
export function copyBytes(source: Uint8Array): Uint8Array {
  return new Uint8Array(source);
}
