import {
  cipherSpec,
  hash,
  macSpec,
  type CipherName,
  type HashAlgorithm,
  type MacName,
} from "./crypto.js";
import { ByteWriter } from "./protocol.js";
import type { ChosenAlgorithms } from "./algorithms.js";

export type SshRole = "client" | "server";

/**
 * SshSession holds the negotiated state of one SSH transport connection,
 * mirroring the C++ SSH::Session: the version strings, the KEXINIT
 * payloads, the chosen algorithms, the exchange hash / session id / shared
 * secret, the derived keys, and the packet sequence numbers.
 */
export class SshSession {
  public localVersion: Uint8Array = new Uint8Array();
  public remoteVersion: Uint8Array = new Uint8Array();
  public localKexInit: Uint8Array = new Uint8Array();
  public remoteKexInit: Uint8Array = new Uint8Array();
  public chosen: ChosenAlgorithms | undefined;
  public sessionId: Uint8Array = new Uint8Array();
  public exchangeHash: Uint8Array = new Uint8Array();
  /** K, the DH shared secret, kept as a bigint for mpint encoding. */
  public sharedSecret: bigint = 0n;
  public localSequenceNumber = 0;
  public remoteSequenceNumber = 0;

  public ivC2S: Uint8Array = new Uint8Array();
  public ivS2C: Uint8Array = new Uint8Array();
  public keyC2S: Uint8Array = new Uint8Array();
  public keyS2C: Uint8Array = new Uint8Array();
  public macKeyC2S: Uint8Array = new Uint8Array();
  public macKeyS2C: Uint8Array = new Uint8Array();

  public constructor(public readonly role: SshRole) {}

  /** The cipher used to send (local->remote). */
  public localEncryption(): CipherName {
    const chosen = this.requireChosen();
    return this.role === "client"
      ? chosen.encryptionClientToServer
      : chosen.encryptionServerToClient;
  }

  /** The cipher used to receive (remote->local). */
  public remoteEncryption(): CipherName {
    const chosen = this.requireChosen();
    return this.role === "client"
      ? chosen.encryptionServerToClient
      : chosen.encryptionClientToServer;
  }

  public localMac(): MacName {
    const chosen = this.requireChosen();
    return this.role === "client"
      ? chosen.macClientToServer
      : chosen.macServerToClient;
  }

  public remoteMac(): MacName {
    const chosen = this.requireChosen();
    return this.role === "client"
      ? chosen.macServerToClient
      : chosen.macClientToServer;
  }

  public localKey(): Uint8Array {
    return this.role === "client" ? this.keyC2S : this.keyS2C;
  }

  public remoteKey(): Uint8Array {
    return this.role === "client" ? this.keyS2C : this.keyC2S;
  }

  public localIv(): Uint8Array {
    return this.role === "client" ? this.ivC2S : this.ivS2C;
  }

  public remoteIv(): Uint8Array {
    return this.role === "client" ? this.ivS2C : this.ivC2S;
  }

  public localMacKey(): Uint8Array {
    return this.role === "client" ? this.macKeyC2S : this.macKeyS2C;
  }

  public remoteMacKey(): Uint8Array {
    return this.role === "client" ? this.macKeyS2C : this.macKeyC2S;
  }

  /**
   * deriveKeys computes the six key-material values (IVs, encryption keys,
   * integrity keys) from K, H and the session id, per RFC 4253 §7.2 and the
   * C++ Session::activate_chosen.
   */
  public deriveKeys(hashAlgorithm: HashAlgorithm): void {
    const chosen = this.requireChosen();
    const encC2S = cipherSpec(chosen.encryptionClientToServer);
    const encS2C = cipherSpec(chosen.encryptionServerToClient);
    const macC2S = macSpec(chosen.macClientToServer);
    const macS2C = macSpec(chosen.macServerToClient);

    this.ivC2S = this.deriveKey("A", encC2S.ivSize, hashAlgorithm);
    this.ivS2C = this.deriveKey("B", encS2C.ivSize, hashAlgorithm);
    this.keyC2S = this.deriveKey("C", encC2S.keySize, hashAlgorithm);
    this.keyS2C = this.deriveKey("D", encS2C.keySize, hashAlgorithm);
    this.macKeyC2S = this.deriveKey("E", macC2S.keySize, hashAlgorithm);
    this.macKeyS2C = this.deriveKey("F", macS2C.keySize, hashAlgorithm);
  }

  /**
   * deriveKey expands K, H and the session id into key_size bytes:
   *   K1 = HASH(K || H || letter || session_id)
   *   K2 = HASH(K || H || K1)
   *   key = K1 || K2 || ... truncated to key_size
   */
  private deriveKey(
    letter: string,
    size: number,
    hashAlgorithm: HashAlgorithm,
  ): Uint8Array {
    const parts: Uint8Array[] = [];
    let previous: Uint8Array = new Uint8Array();
    let total = 0;
    while (total < size) {
      const writer = new ByteWriter();
      writer.mpint(this.sharedSecret);
      writer.raw(this.exchangeHash);
      if (parts.length === 0) {
        writer.string(letter);
        writer.raw(this.sessionId);
      } else {
        writer.raw(previous);
      }
      previous = hash(hashAlgorithm, writer.bytes());
      parts.push(previous);
      total += previous.length;
    }
    return concatBytes(parts).subarray(0, size);
  }

  private requireChosen(): ChosenAlgorithms {
    if (this.chosen === undefined) {
      throw new Error("ssh: algorithms not yet negotiated");
    }
    return this.chosen;
  }
}

function concatBytes(parts: readonly Uint8Array[]): Uint8Array {
  const length = parts.reduce((sum, part) => sum + part.length, 0);
  const output = new Uint8Array(length);
  let offset = 0;
  for (const part of parts) {
    output.set(part, offset);
    offset += part.length;
  }
  return output;
}
