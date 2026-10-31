import { type CipherName, type MacName } from "./crypto.js";
import { ByteReader, ByteWriter, Message } from "./protocol.js";

/**
 * The algorithm suites this SSH transport offers, a focused subset of the
 * C++ ssh_* algorithm tables that Node's crypto supports directly.
 */
export const KEX_ALGORITHMS = [
  "diffie-hellman-group-exchange-sha256",
  "diffie-hellman-group-exchange-sha1",
] as const;

export const HOST_KEY_ALGORITHMS = ["ssh-rsa"] as const;

export const CIPHER_ALGORITHMS = [
  "aes128-ctr",
  "aes256-ctr",
  "aes128-cbc",
  "aes256-cbc",
] as const;

export const MAC_ALGORITHMS = [
  "hmac-sha2-256",
  "hmac-sha1",
  "hmac-sha2-512",
] as const;

export const COMPRESSION_ALGORITHMS = ["none"] as const;

export interface ChosenAlgorithms {
  readonly kex: (typeof KEX_ALGORITHMS)[number];
  readonly hostKey: (typeof HOST_KEY_ALGORITHMS)[number];
  readonly encryptionClientToServer: CipherName;
  readonly encryptionServerToClient: CipherName;
  readonly macClientToServer: MacName;
  readonly macServerToClient: MacName;
}

export interface KexInitLists {
  readonly kex: readonly string[];
  readonly hostKey: readonly string[];
  readonly encryptionC2S: readonly string[];
  readonly encryptionS2C: readonly string[];
  readonly macC2S: readonly string[];
  readonly macS2C: readonly string[];
  readonly compressionC2S: readonly string[];
  readonly compressionS2C: readonly string[];
}

/** buildKexInit returns the SSH_MSG_KEXINIT payload (RFC 4253 §7.1). */
export function buildKexInit(cookie: Uint8Array): Buffer {
  if (cookie.length !== 16) {
    throw new RangeError("kexinit cookie must be 16 bytes");
  }
  const writer = new ByteWriter();
  writer.byte(Message.KexInit);
  writer.raw(cookie);
  writer.nameList(KEX_ALGORITHMS);
  writer.nameList(HOST_KEY_ALGORITHMS);
  writer.nameList(CIPHER_ALGORITHMS);
  writer.nameList(CIPHER_ALGORITHMS);
  writer.nameList(MAC_ALGORITHMS);
  writer.nameList(MAC_ALGORITHMS);
  writer.nameList(COMPRESSION_ALGORITHMS);
  writer.nameList(COMPRESSION_ALGORITHMS);
  writer.nameList([]);
  writer.nameList([]);
  writer.boolean(false);
  writer.uint32(0);
  return writer.bytes();
}

/** parseKexInit extracts the algorithm name-lists from a KEXINIT payload. */
export function parseKexInit(payload: Uint8Array): KexInitLists {
  const reader = new ByteReader(payload);
  const message = reader.byte();
  if (message !== Message.KexInit) {
    throw new Error(`ssh: expected KEXINIT, got message ${message}`);
  }
  reader.skip(16); // cookie
  return {
    kex: reader.nameList(),
    hostKey: reader.nameList(),
    encryptionC2S: reader.nameList(),
    encryptionS2C: reader.nameList(),
    macC2S: reader.nameList(),
    macS2C: reader.nameList(),
    compressionC2S: reader.nameList(),
    compressionS2C: reader.nameList(),
  };
}

/**
 * chooseAlgorithms picks one algorithm per category.  Per RFC 4253 §7.1 the
 * client's list takes precedence: the chosen algorithm is the first on the
 * client's list that also appears on the server's list.
 */
export function chooseAlgorithms(
  local: Uint8Array,
  remote: Uint8Array,
  role: "client" | "server",
): ChosenAlgorithms {
  const localLists = parseKexInit(local);
  const remoteLists = parseKexInit(remote);
  const client = role === "client" ? localLists : remoteLists;
  const server = role === "client" ? remoteLists : localLists;

  return {
    encryptionClientToServer: pick(
      client.encryptionC2S,
      server.encryptionC2S,
      "encryption (client->server)",
    ) as CipherName,
    encryptionServerToClient: pick(
      client.encryptionS2C,
      server.encryptionS2C,
      "encryption (server->client)",
    ) as CipherName,
    hostKey: pick(
      client.hostKey,
      server.hostKey,
      "server host key",
    ) as ChosenAlgorithms["hostKey"],
    kex: pick(
      client.kex,
      server.kex,
      "key exchange",
    ) as ChosenAlgorithms["kex"],
    macClientToServer: pick(
      client.macC2S,
      server.macC2S,
      "MAC (client->server)",
    ) as MacName,
    macServerToClient: pick(
      client.macS2C,
      server.macS2C,
      "MAC (server->client)",
    ) as MacName,
  };
}

function pick(
  clientList: readonly string[],
  serverList: readonly string[],
  what: string,
): string {
  for (const name of clientList) {
    if (serverList.includes(name)) {
      return name;
    }
  }
  throw new Error(`ssh: no matching ${what} algorithm`);
}

/** hashForKex maps a kex algorithm name to its exchange hash. */
export function hashForKex(kex: string): "sha1" | "sha256" {
  return kex.endsWith("sha1") ? "sha1" : "sha256";
}
