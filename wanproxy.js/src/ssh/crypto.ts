import {
  createCipheriv,
  createDecipheriv,
  createDiffieHellman,
  createHash,
  createHmac,
  createPrivateKey,
  createPublicKey,
  createSign,
  createVerify,
  generateKeyPairSync,
  getDiffieHellman,
  type KeyObject,
} from "node:crypto";

import { ByteReader, ByteWriter } from "./protocol.js";

export type HashAlgorithm = "sha1" | "sha256" | "sha512";

/** hash computes a digest, mirroring CryptoHash::hash. */
export function hash(algorithm: HashAlgorithm, data: Uint8Array): Buffer {
  return createHash(algorithm).update(data).digest();
}

/**
 * mpintFromBuffer normalizes a big-endian magnitude into SSH mpint bytes
 * (minimal length, with a leading zero byte when the high bit is set so the
 * value stays positive), mirroring SSH::MPInt::encode.
 */
export function mpintFromBuffer(magnitude: Uint8Array): Buffer {
  let start = 0;
  while (start < magnitude.length - 1 && magnitude[start] === 0) {
    start += 1;
  }
  let body = Buffer.from(magnitude.subarray(start));
  if (body.length === 0) {
    body = Buffer.alloc(0);
  }
  if (body.length > 0 && (body[0] ?? 0) & 0x80) {
    body = Buffer.concat([Buffer.from([0]), body]);
  }
  return body;
}

export function bigintFromBuffer(magnitude: Uint8Array): bigint {
  if (magnitude.length === 0) {
    return 0n;
  }
  return BigInt(`0x${Buffer.from(magnitude).toString("hex")}`);
}

/** The RFC 3526 2048-bit MODP group (modp14) used for DH group exchange. */
export interface DhGroup {
  readonly prime: Buffer;
  readonly generator: Buffer;
}

export function modp14Group(): DhGroup {
  const dh = getDiffieHellman("modp14");
  return { generator: dh.getGenerator(), prime: dh.getPrime() };
}

/**
 * DhKeyExchange performs one side of a Diffie-Hellman exchange over a fixed
 * group, wrapping Node's DiffieHellman.  The public value and shared secret
 * are exposed as big-endian magnitudes for SSH mpint encoding.
 */
export class DhKeyExchange {
  private readonly dh: ReturnType<typeof createDiffieHellman>;
  private publicKey: Buffer | undefined;

  public constructor(group: DhGroup) {
    // createDiffieHellman with an explicit prime/generator matches the
    // group the peer announced in SSH_MSG_KEX_DH_GEX_GROUP.
    this.dh = createDiffieHellman(group.prime, group.generator);
  }

  public generateKeys(): void {
    this.dh.generateKeys();
    this.publicKey = this.dh.getPublicKey();
  }

  public publicKeyMpint(): Buffer {
    if (this.publicKey === undefined) {
      throw new Error("ssh: DH keys not generated");
    }
    return mpintFromBuffer(this.publicKey);
  }

  public computeSecret(otherPublicMpint: Uint8Array): Buffer {
    // Strip a leading zero byte added for mpint sign, then compute.
    const other = Buffer.from(otherPublicMpint);
    return this.dh.computeSecret(other);
  }
}

export type CipherName =
  "aes128-ctr" | "aes256-ctr" | "aes128-cbc" | "aes256-cbc";

interface CipherSpec {
  readonly node: string;
  readonly keySize: number;
  readonly ivSize: number;
  readonly blockSize: number;
}

const CIPHER_SPECS: Record<CipherName, CipherSpec> = {
  "aes128-cbc": { blockSize: 16, ivSize: 16, keySize: 16, node: "aes-128-cbc" },
  "aes128-ctr": { blockSize: 16, ivSize: 16, keySize: 16, node: "aes-128-ctr" },
  "aes256-cbc": { blockSize: 16, ivSize: 16, keySize: 32, node: "aes-256-cbc" },
  "aes256-ctr": { blockSize: 16, ivSize: 16, keySize: 32, node: "aes-256-ctr" },
};

export function cipherSpec(name: CipherName): CipherSpec {
  return CIPHER_SPECS[name];
}

/**
 * Encryptor is a persistent stream cipher (CTR) or chained block cipher
 * (CBC) for one direction.  SSH continues the cipher state across packets,
 * so a single Node cipher object is reused for every update().
 */
export class Encryptor {
  private readonly cipher: ReturnType<typeof createCipheriv>;

  public constructor(name: CipherName, key: Uint8Array, iv: Uint8Array) {
    const spec = CIPHER_SPECS[name];
    this.cipher = createCipheriv(spec.node, Buffer.from(key), Buffer.from(iv));
    this.cipher.setAutoPadding(false);
  }

  public update(data: Uint8Array): Buffer {
    return Buffer.from(this.cipher.update(Buffer.from(data)));
  }
}

export class Decryptor {
  private readonly decipher: ReturnType<typeof createDecipheriv>;

  public constructor(name: CipherName, key: Uint8Array, iv: Uint8Array) {
    const spec = CIPHER_SPECS[name];
    this.decipher = createDecipheriv(
      spec.node,
      Buffer.from(key),
      Buffer.from(iv),
    );
    this.decipher.setAutoPadding(false);
  }

  public update(data: Uint8Array): Buffer {
    return Buffer.from(this.decipher.update(Buffer.from(data)));
  }
}

export type MacName = "hmac-sha1" | "hmac-sha2-256" | "hmac-sha2-512";

const MAC_SPECS: Record<
  MacName,
  { node: string; size: number; keySize: number }
> = {
  "hmac-sha1": { keySize: 20, node: "sha1", size: 20 },
  "hmac-sha2-256": { keySize: 32, node: "sha256", size: 32 },
  "hmac-sha2-512": { keySize: 64, node: "sha512", size: 64 },
};

export function macSpec(name: MacName): { size: number; keySize: number } {
  return { keySize: MAC_SPECS[name].keySize, size: MAC_SPECS[name].size };
}

/** Mac computes the per-packet integrity check (RFC 4253 §6.4). */
export class Mac {
  public constructor(
    private readonly name: MacName,
    private readonly key: Uint8Array,
  ) {}

  public compute(sequenceNumber: number, packet: Uint8Array): Buffer {
    const spec = MAC_SPECS[this.name];
    const mac = createHmac(spec.node, Buffer.from(this.key));
    const seq = Buffer.allocUnsafe(4);
    seq.writeUInt32BE(sequenceNumber >>> 0, 0);
    mac.update(seq);
    mac.update(Buffer.from(packet));
    return mac.digest();
  }
}

/**
 * RsaHostKey is the ssh-rsa server host key (RFC 4253 §6.6, RSA-SHA1
 * signatures), mirroring the C++ RSAServerHostKey.
 */
export class RsaHostKey {
  private constructor(
    private readonly privateKey: KeyObject | undefined,
    private readonly publicKey: KeyObject,
  ) {}

  /** generate creates a fresh 2048-bit server host key. */
  public static generate(): RsaHostKey {
    const { privateKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
    });
    const publicKey = createPublicKey(privateKey);
    return new RsaHostKey(privateKey, publicKey);
  }

  /** fromPublic builds a verification-only key from a public KeyObject. */
  public static fromPublic(publicKey: KeyObject): RsaHostKey {
    return new RsaHostKey(undefined, publicKey);
  }

  /**
   * fromPrivatePem loads a PEM-encoded RSA private key, mirroring the C++
   * ServerHostKey::server() which reads PKCS#1 "RSA PRIVATE KEY" PEM files.
   * PKCS#8 "PRIVATE KEY" files are accepted as a convenience (Node's
   * createPrivateKey handles both).
   */
  public static fromPrivatePem(pem: string | Uint8Array): RsaHostKey {
    const privateKey = createPrivateKey(
      typeof pem === "string" ? pem : Buffer.from(pem),
    );
    if (privateKey.asymmetricKeyType !== "rsa") {
      throw new Error("ssh: PEM host key is not an RSA key");
    }
    const publicKey = createPublicKey(privateKey);
    return new RsaHostKey(privateKey, publicKey);
  }

  /** encodePublicKey returns the ssh-rsa public key blob (K_S). */
  public encodePublicKey(): Buffer {
    const jwk = this.publicKey.export({ format: "jwk" }) as {
      n: string;
      e: string;
    };
    const n = bigintFromJwk(jwk.n);
    const e = bigintFromJwk(jwk.e);
    const writer = new ByteWriter();
    writer.string("ssh-rsa");
    writer.mpint(e);
    writer.mpint(n);
    return writer.bytes();
  }

  /** sign returns the ssh-rsa signature blob over the exchange hash. */
  public sign(data: Uint8Array): Buffer {
    if (this.privateKey === undefined) {
      throw new Error("ssh: cannot sign without a private host key");
    }
    const signature = createSign("RSA-SHA1")
      .update(Buffer.from(data))
      .sign(this.privateKey);
    const writer = new ByteWriter();
    writer.string("ssh-rsa");
    writer.string(signature);
    return writer.bytes();
  }

  /** verify checks an ssh-rsa signature blob over the exchange hash. */
  public verify(signatureBlob: Uint8Array, data: Uint8Array): boolean {
    const reader = new ByteReader(Buffer.from(signatureBlob));
    const tag = reader.stringUtf8();
    if (tag !== "ssh-rsa") {
      return false;
    }
    const signature = reader.string();
    return createVerify("RSA-SHA1")
      .update(Buffer.from(data))
      .verify(this.publicKey, signature);
  }
}

/** decodeRsaPublicKeyBlob builds a verification key from an ssh-rsa blob. */
export function decodeRsaPublicKeyBlob(blob: Uint8Array): RsaHostKey {
  const reader = new ByteReader(Buffer.from(blob));
  const tag = reader.stringUtf8();
  if (tag !== "ssh-rsa") {
    throw new Error(`ssh: unsupported host key algorithm ${tag}`);
  }
  const e = reader.mpint();
  const n = reader.mpint();
  const publicKey = createPublicKey({
    format: "jwk",
    key: {
      e: jwkFromBigint(e),
      ext: true,
      key_ops: [],
      kty: "RSA",
      n: jwkFromBigint(n),
    },
  });
  return RsaHostKey.fromPublic(publicKey);
}

function bigintFromJwk(value: string): bigint {
  const bytes = Buffer.from(value, "base64url");
  return bigintFromBuffer(bytes);
}

function jwkFromBigint(value: bigint): string {
  let hex = value.toString(16);
  if (hex.length % 2 !== 0) {
    hex = `0${hex}`;
  }
  return Buffer.from(hex, "hex").toString("base64url");
}
