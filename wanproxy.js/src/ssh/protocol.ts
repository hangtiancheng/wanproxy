/**
 * SSH-2 binary packet protocol primitives, mirroring ssh/ssh_protocol.*
 * and RFC 4251 §5.  All multi-byte integers are big-endian.
 */

/** SSH transport-layer and connection message numbers (RFC 4250/4253). */
export const Message = {
  Disconnect: 1,
  Ignore: 2,
  Unimplemented: 3,
  Debug: 4,
  ServiceRequest: 5,
  ServiceAccept: 6,
  KexInit: 20,
  NewKeys: 21,
  KexDhGexRequest: 34,
  KexDhGexGroup: 31,
  KexDhGexInit: 32,
  KexDhGexReply: 33,
} as const;

export const Disconnect = {
  HostNotAllowed: 1,
  ProtocolError: 2,
  KeyExchangeFailed: 3,
  MacError: 5,
  ConnectionLost: 10,
  ByApplication: 11,
} as const;

/** A growable byte writer for building SSH payloads. */
export class ByteWriter {
  private readonly parts: Buffer[] = [];

  public byte(value: number): this {
    this.parts.push(Buffer.from([value & 0xff]));
    return this;
  }

  public boolean(value: boolean): this {
    return this.byte(value ? 1 : 0);
  }

  public uint32(value: number): this {
    const buf = Buffer.allocUnsafe(4);
    buf.writeUInt32BE(value >>> 0, 0);
    this.parts.push(buf);
    return this;
  }

  public uint64(value: bigint): this {
    const buf = Buffer.allocUnsafe(8);
    buf.writeBigUInt64BE(value, 0);
    this.parts.push(buf);
    return this;
  }

  public raw(data: Uint8Array): this {
    this.parts.push(Buffer.from(data));
    return this;
  }

  /** string: a uint32 length followed by that many bytes (RFC 4251). */
  public string(data: Uint8Array | string): this {
    const buf =
      typeof data === "string" ? Buffer.from(data) : Buffer.from(data);
    this.uint32(buf.length);
    if (buf.length > 0) {
      this.parts.push(buf);
    }
    return this;
  }

  /** mpint: a two's-complement big-endian integer, minimally encoded. */
  public mpint(value: bigint): this {
    if (value === 0n) {
      return this.uint32(0);
    }
    if (value < 0n) {
      throw new RangeError("negative mpint is not supported");
    }
    let hex = value.toString(16);
    if (hex.length % 2 !== 0) {
      hex = `0${hex}`;
    }
    let bytes = Buffer.from(hex, "hex");
    if ((bytes[0] ?? 0) & 0x80) {
      bytes = Buffer.concat([Buffer.from([0]), bytes]);
    }
    return this.string(bytes);
  }

  /** name-list: a comma-separated list encoded as a string. */
  public nameList(names: readonly string[]): this {
    return this.string(names.join(","));
  }

  public bytes(): Buffer {
    return Buffer.concat(this.parts);
  }

  public get length(): number {
    return this.parts.reduce((sum, part) => sum + part.length, 0);
  }
}

/** A sequential reader over an SSH payload buffer. */
export class ByteReader {
  private readonly buffer: Buffer;
  private offset = 0;

  public constructor(data: Uint8Array) {
    this.buffer = Buffer.isBuffer(data) ? data : Buffer.from(data);
  }

  public get remaining(): number {
    return this.buffer.length - this.offset;
  }

  public byte(): number {
    this.require(1);
    return this.buffer[this.offset++] ?? 0;
  }

  public boolean(): boolean {
    return this.byte() !== 0;
  }

  public uint32(): number {
    this.require(4);
    const value = this.buffer.readUInt32BE(this.offset);
    this.offset += 4;
    return value;
  }

  public uint64(): bigint {
    this.require(8);
    const value = this.buffer.readBigUInt64BE(this.offset);
    this.offset += 8;
    return value;
  }

  public raw(length: number): Buffer {
    this.require(length);
    const slice = this.buffer.subarray(this.offset, this.offset + length);
    this.offset += length;
    return Buffer.from(slice);
  }

  public string(): Buffer {
    const length = this.uint32();
    return this.raw(length);
  }

  public stringUtf8(): string {
    return this.string().toString("utf8");
  }

  public mpint(): bigint {
    const bytes = this.string();
    if (bytes.length === 0) {
      return 0n;
    }
    return BigInt(`0x${bytes.toString("hex")}`);
  }

  public nameList(): string[] {
    const merged = this.stringUtf8();
    return merged.length === 0 ? [] : merged.split(",");
  }

  public skip(length: number): void {
    this.require(length);
    this.offset += length;
  }

  private require(length: number): void {
    if (this.remaining < length) {
      throw new Error(
        `ssh: truncated payload (need ${length}, have ${this.remaining})`,
      );
    }
  }
}
