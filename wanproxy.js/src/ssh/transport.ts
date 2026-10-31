import { randomBytes } from "node:crypto";
import type { Duplex } from "node:stream";

import { buildKexInit, chooseAlgorithms, hashForKex } from "./algorithms.js";
import {
  bigintFromBuffer,
  cipherSpec,
  Decryptor,
  DhKeyExchange,
  Encryptor,
  Mac,
  macSpec,
  modp14Group,
  RsaHostKey,
  decodeRsaPublicKeyBlob,
  hash,
} from "./crypto.js";
import { ByteReader, ByteWriter, Message } from "./protocol.js";
import { SshSession, type SshRole } from "./session.js";

const DH_GROUP_MIN = 1024;
const DH_GROUP_MAX = 8192;
const MAX_PACKET_LENGTH = 256 * 1024;

export interface SshTransportOptions {
  readonly role: SshRole;
  /** The server's RSA host key; required for the server role. */
  readonly hostKey?: RsaHostKey;
  /** Software comment for the identification string. */
  readonly software?: string;
}

type HandshakeState =
  "identification" | "kexinit" | "key-exchange" | "newkeys" | "ready";

/**
 * SshTransport is the SSH-2 transport layer over a duplex socket, mirroring
 * the C++ SSH::TransportPipe: it exchanges the identification string and
 * SSH_MSG_KEXINIT, runs diffie-hellman-group-exchange key exchange with an
 * ssh-rsa host key, switches to the negotiated cipher and MAC on
 * SSH_MSG_NEWKEYS, and then frames, encrypts and integrity-protects
 * application payloads.
 */
export class SshTransport {
  private readonly session: SshSession;
  private readonly hostKey: RsaHostKey | undefined;
  private readonly software: string;

  private input = Buffer.alloc(0);
  private firstBlockPlain: Buffer | undefined;
  private encryptor: Encryptor | undefined;
  private decryptor: Decryptor | undefined;
  private localMac: Mac | undefined;
  private remoteMac: Mac | undefined;

  private state: HandshakeState = "identification";
  private dh: DhKeyExchange | undefined;
  private readonly kexData = new ByteWriter();
  private gexMin = 0;
  private gexN = 0;
  private gexMax = 0;
  private hostKeyBlob: Uint8Array = new Uint8Array();

  private newkeysSent = false;
  private newkeysReceived = false;

  private payloadHandler: ((payload: Uint8Array) => void) | undefined;
  /**
   * Payloads that arrive after the handshake but before a handler is
   * registered are buffered here.  This matters because NEWKEYS and the
   * first application packet can be delivered in a single socket read, so
   * consume() reaches ready state before the caller's .then() microtask has
   * attached its handler.
   */
  private readonly pendingPayloads: Uint8Array[] = [];
  private resolveReady: () => void = () => undefined;
  private rejectReady: (error: Error) => void = () => undefined;
  private readonly ready: Promise<void>;
  private settled = false;

  public constructor(
    private readonly socket: Duplex,
    options: SshTransportOptions,
  ) {
    this.session = new SshSession(options.role);
    this.hostKey = options.hostKey;
    this.software = options.software ?? "WANProxy.js";
    this.ready = new Promise<void>((resolve, reject) => {
      this.resolveReady = resolve;
      this.rejectReady = reject;
    });
    if (options.role === "server" && this.hostKey === undefined) {
      throw new Error("ssh: server role requires a host key");
    }
  }

  /** start sends the identification string and resolves when ready. */
  public start(): Promise<void> {
    this.socket.on("data", this.onData);
    this.socket.once("error", this.onError);
    this.socket.once("close", this.handleSocketClose);
    const version = `SSH-2.0-${this.software}`;
    this.session.localVersion = Buffer.from(version, "utf8");
    this.socket.write(`${version}\r\n`);
    return this.ready;
  }

  /** send writes one application payload as an encrypted SSH packet. */
  public send(payload: Uint8Array): void {
    if (this.state !== "ready") {
      throw new Error("ssh: transport is not ready");
    }
    this.sendPacket(Buffer.from(payload));
  }

  public onPayload(handler: (payload: Uint8Array) => void): void {
    this.payloadHandler = handler;
    if (this.pendingPayloads.length > 0) {
      const buffered = this.pendingPayloads.splice(
        0,
        this.pendingPayloads.length,
      );
      for (const payload of buffered) {
        handler(payload);
      }
    }
  }

  /** close tears down the underlying socket, ending the SSH session. */
  public close(): void {
    this.socket.destroy();
  }

  /** onClose registers a handler invoked when the SSH connection closes. */
  public onClose(handler: () => void): void {
    this.socket.once("close", handler);
  }

  public get remoteVersionString(): string {
    return Buffer.from(this.session.remoteVersion).toString("utf8");
  }

  public get sessionId(): Uint8Array {
    return this.session.sessionId;
  }

  private readonly onData = (chunk: Buffer): void => {
    try {
      this.consume(chunk);
    } catch (error) {
      this.fail(
        error instanceof Error ? error : new Error("ssh: consume failed"),
      );
    }
  };

  private readonly onError = (error: Error): void => {
    this.fail(error);
  };

  private readonly handleSocketClose = (): void => {
    this.fail(new Error("ssh: socket closed during transport"));
  };

  private fail(error: Error): void {
    if (this.settled) {
      return;
    }
    this.settled = true;
    this.rejectReady(error);
  }

  private becomeReady(): void {
    if (this.settled) {
      return;
    }
    this.settled = true;
    this.state = "ready";
    this.socket.off("error", this.onError);
    this.socket.off("close", this.handleSocketClose);
    this.resolveReady();
  }

  private consume(data: Buffer): void {
    this.input = Buffer.concat([this.input, data]);

    if (this.state === "identification") {
      if (!this.consumeIdentification()) {
        return;
      }
    }

    for (;;) {
      const packet = this.tryReadPacket();
      if (packet === undefined) {
        return;
      }
      this.handlePacket(packet);
      if (this.state === "ready") {
        // Drain any pipelined application packets.
        continue;
      }
    }
  }

  /** consumeIdentification reads banner lines until the SSH version line. */
  private consumeIdentification(): boolean {
    for (;;) {
      const lineEnd = indexOfLineEnd(this.input);
      if (lineEnd === undefined) {
        return false;
      }
      const line = this.input.subarray(0, lineEnd.index);
      this.input = this.input.subarray(lineEnd.next);
      const text = line.toString("utf8");
      if (!text.startsWith("SSH-")) {
        continue; // Ignore pre-banner lines.
      }
      if (!text.startsWith("SSH-2.0")) {
        throw new Error(`ssh: unsupported version ${text}`);
      }
      this.session.remoteVersion = Buffer.from(text, "utf8");
      this.state = "kexinit";
      this.sendKexInit();
      return true;
    }
  }

  private sendKexInit(): void {
    const cookie = new Uint8Array(randomBytes(16));
    const payload = buildKexInit(cookie);
    this.session.localKexInit = payload;
    this.sendPacket(payload);
  }

  private handlePacket(payload: Uint8Array): void {
    if (this.state === "ready") {
      if (this.payloadHandler !== undefined) {
        this.payloadHandler(payload);
      } else {
        this.pendingPayloads.push(payload);
      }
      return;
    }

    const reader = new ByteReader(payload);
    const message = reader.byte();
    switch (message) {
      case Message.KexInit:
        this.onKexInit(payload);
        break;
      case Message.KexDhGexRequest:
        this.onGexRequest(reader);
        break;
      case Message.KexDhGexGroup:
        this.onGexGroup(reader);
        break;
      case Message.KexDhGexInit:
        this.onGexInit(reader);
        break;
      case Message.KexDhGexReply:
        this.onGexReply(reader);
        break;
      case Message.NewKeys:
        this.onNewKeys();
        break;
      case Message.Ignore:
      case Message.Debug:
        break;
      case Message.Disconnect:
        throw new Error("ssh: peer disconnected");
      default:
        throw new Error(`ssh: unexpected message ${message} during handshake`);
    }
  }

  private onKexInit(payload: Uint8Array): void {
    if (this.state !== "kexinit") {
      throw new Error("ssh: unexpected KEXINIT");
    }
    this.session.remoteKexInit = payload;
    this.session.chosen = chooseAlgorithms(
      this.session.localKexInit,
      this.session.remoteKexInit,
      this.session.role,
    );
    this.state = "key-exchange";
    if (this.session.role === "client") {
      this.sendGexRequest();
    }
  }

  private sendGexRequest(): void {
    this.gexMin = DH_GROUP_MIN;
    this.gexN = DH_GROUP_MIN;
    this.gexMax = DH_GROUP_MAX;
    this.kexData.uint32(this.gexMin);
    this.kexData.uint32(this.gexN);
    this.kexData.uint32(this.gexMax);
    const writer = new ByteWriter();
    writer.byte(Message.KexDhGexRequest);
    writer.uint32(this.gexMin);
    writer.uint32(this.gexN);
    writer.uint32(this.gexMax);
    this.sendPacket(writer.bytes());
  }

  private onGexRequest(reader: ByteReader): void {
    this.assertServer("GEX_REQUEST");
    this.gexMin = reader.uint32();
    this.gexN = reader.uint32();
    this.gexMax = reader.uint32();
    this.kexData.uint32(this.gexMin);
    this.kexData.uint32(this.gexN);
    this.kexData.uint32(this.gexMax);

    const group = modp14Group();
    this.kexData.mpint(bigintFromBuffer(group.prime));
    this.kexData.mpint(bigintFromBuffer(group.generator));
    this.dh = new DhKeyExchange(group);

    const writer = new ByteWriter();
    writer.byte(Message.KexDhGexGroup);
    writer.mpint(bigintFromBuffer(group.prime));
    writer.mpint(bigintFromBuffer(group.generator));
    this.sendPacket(writer.bytes());
  }

  private onGexGroup(reader: ByteReader): void {
    this.assertClient("GEX_GROUP");
    const primeBytes = reader.string();
    const generatorBytes = reader.string();
    this.kexData.mpint(bigintFromBuffer(primeBytes));
    this.kexData.mpint(bigintFromBuffer(generatorBytes));
    this.dh = new DhKeyExchange({
      generator: mpintToMagnitude(generatorBytes),
      prime: mpintToMagnitude(primeBytes),
    });
    this.dh.generateKeys();
    const e = this.dh.publicKeyMpint();
    this.kexData.mpint(bigintFromBuffer(e));

    const writer = new ByteWriter();
    writer.byte(Message.KexDhGexInit);
    writer.raw(encodeMpintBytes(e));
    this.sendPacket(writer.bytes());
  }

  private onGexInit(reader: ByteReader): void {
    this.assertServer("GEX_INIT");
    if (this.dh === undefined) {
      throw new Error("ssh: premature GEX_INIT");
    }
    const e = reader.string(); // mpint bytes
    this.kexData.mpint(bigintFromBuffer(e));
    this.dh.generateKeys();
    const f = this.dh.publicKeyMpint();
    this.kexData.mpint(bigintFromBuffer(f));

    const secret = this.dh.computeSecret(mpintToMagnitude(e));
    this.finishKeyExchange(secret, true);

    const hostKey = this.hostKey;
    if (hostKey === undefined) {
      throw new Error("ssh: server has no host key");
    }
    this.hostKeyBlob = hostKey.encodePublicKey();
    const signature = hostKey.sign(this.session.exchangeHash);

    const writer = new ByteWriter();
    writer.byte(Message.KexDhGexReply);
    writer.string(this.hostKeyBlob);
    writer.raw(encodeMpintBytes(f));
    writer.string(signature);
    this.sendPacket(writer.bytes());

    this.sendNewKeys();
  }

  private onGexReply(reader: ByteReader): void {
    this.assertClient("GEX_REPLY");
    if (this.dh === undefined) {
      throw new Error("ssh: premature GEX_REPLY");
    }
    const hostKeyBlob = reader.string();
    const f = reader.string(); // mpint bytes
    const signature = reader.string();
    this.kexData.mpint(bigintFromBuffer(f));
    this.hostKeyBlob = hostKeyBlob;

    const secret = this.dh.computeSecret(mpintToMagnitude(f));
    this.finishKeyExchange(secret, false);

    const hostKey = decodeRsaPublicKeyBlob(hostKeyBlob);
    if (!hostKey.verify(signature, this.session.exchangeHash)) {
      throw new Error("ssh: host key signature verification failed");
    }
    this.sendNewKeys();
  }

  /**
   * finishKeyExchange computes the shared secret K, the exchange hash H and
   * the session id, then derives the keys, mirroring the C++
   * exchange_finish.  isServer selects the host-key blob source.
   */
  private finishKeyExchange(secret: Buffer, isServer: boolean): void {
    const chosen = this.session.chosen;
    if (chosen === undefined) {
      throw new Error("ssh: algorithms not negotiated");
    }
    this.session.sharedSecret = bigintFromBuffer(secret);

    if (isServer && this.hostKey !== undefined) {
      this.hostKeyBlob = this.hostKey.encodePublicKey();
    }

    const clientVersion =
      this.session.role === "client"
        ? this.session.localVersion
        : this.session.remoteVersion;
    const serverVersion =
      this.session.role === "client"
        ? this.session.remoteVersion
        : this.session.localVersion;
    const clientKexInit =
      this.session.role === "client"
        ? this.session.localKexInit
        : this.session.remoteKexInit;
    const serverKexInit =
      this.session.role === "client"
        ? this.session.remoteKexInit
        : this.session.localKexInit;

    const writer = new ByteWriter();
    writer.string(clientVersion);
    writer.string(serverVersion);
    writer.string(clientKexInit);
    writer.string(serverKexInit);
    writer.string(this.hostKeyBlob);
    writer.raw(this.kexData.bytes());
    writer.mpint(this.session.sharedSecret);

    const hashAlgorithm = hashForKex(chosen.kex);
    this.session.exchangeHash = hash(hashAlgorithm, writer.bytes());
    if (this.session.sessionId.length === 0) {
      this.session.sessionId = this.session.exchangeHash;
    }
    this.session.deriveKeys(hashAlgorithm);
  }

  private sendNewKeys(): void {
    const writer = new ByteWriter();
    writer.byte(Message.NewKeys);
    this.sendPacket(writer.bytes());
    this.newkeysSent = true;
    this.activateOutgoing();
    this.maybeReady();
  }

  private onNewKeys(): void {
    if (this.newkeysReceived) {
      throw new Error("ssh: duplicate NEWKEYS");
    }
    this.newkeysReceived = true;
    this.activateIncoming();
    this.maybeReady();
  }

  private maybeReady(): void {
    if (this.newkeysSent && this.newkeysReceived) {
      this.becomeReady();
    }
  }

  private activateOutgoing(): void {
    this.encryptor = new Encryptor(
      this.session.localEncryption(),
      this.session.localKey(),
      this.session.localIv(),
    );
    this.localMac = new Mac(
      this.session.localMac(),
      this.session.localMacKey(),
    );
  }

  private activateIncoming(): void {
    this.decryptor = new Decryptor(
      this.session.remoteEncryption(),
      this.session.remoteKey(),
      this.session.remoteIv(),
    );
    this.remoteMac = new Mac(
      this.session.remoteMac(),
      this.session.remoteMacKey(),
    );
  }

  private sendPacket(payload: Buffer): void {
    const blockSize = this.sendBlockSize();
    let paddingLength = blockSize - ((5 + payload.length) % blockSize);
    if (paddingLength < 4) {
      paddingLength += blockSize;
    }
    const packetLength = 1 + payload.length + paddingLength;

    const writer = new ByteWriter();
    writer.uint32(packetLength);
    writer.byte(paddingLength);
    writer.raw(payload);
    writer.raw(new Uint8Array(paddingLength)); // zero padding, like the C++
    const plaintext = writer.bytes();

    let mac: Buffer = Buffer.alloc(0);
    if (this.localMac !== undefined) {
      mac = this.localMac.compute(this.session.localSequenceNumber, plaintext);
    }
    const onWire =
      this.encryptor !== undefined
        ? this.encryptor.update(plaintext)
        : plaintext;

    this.socket.write(Buffer.concat([onWire, mac]));
    this.session.localSequenceNumber =
      (this.session.localSequenceNumber + 1) >>> 0;
  }

  private tryReadPacket(): Uint8Array | undefined {
    const blockSize = this.receiveBlockSize();
    const macSize =
      this.remoteMac !== undefined ? macSpec(this.session.remoteMac()).size : 0;

    let plaintextPacket: Buffer;
    if (this.decryptor !== undefined) {
      if (this.firstBlockPlain === undefined) {
        if (this.input.length < blockSize) {
          return undefined;
        }
        this.firstBlockPlain = this.decryptor.update(
          this.input.subarray(0, blockSize),
        );
        this.input = this.input.subarray(blockSize);
      }
      const packetLength = this.firstBlockPlain.readUInt32BE(0);
      if (packetLength < 1 || packetLength > MAX_PACKET_LENGTH) {
        throw new Error(`ssh: invalid packet length ${packetLength}`);
      }
      const remaining = 4 + packetLength - blockSize;
      if (this.input.length < remaining + macSize) {
        return undefined;
      }
      const rest =
        remaining > 0
          ? this.decryptor.update(this.input.subarray(0, remaining))
          : Buffer.alloc(0);
      this.input = this.input.subarray(remaining);
      const mac = this.input.subarray(0, macSize);
      this.input = this.input.subarray(macSize);
      plaintextPacket = Buffer.concat([this.firstBlockPlain, rest]);
      this.firstBlockPlain = undefined;
      this.verifyMac(plaintextPacket, mac);
    } else {
      if (this.input.length < 5) {
        return undefined;
      }
      const packetLength = this.input.readUInt32BE(0);
      if (packetLength < 1 || packetLength > MAX_PACKET_LENGTH) {
        throw new Error(`ssh: invalid packet length ${packetLength}`);
      }
      const total = 4 + packetLength + macSize;
      if (this.input.length < total) {
        return undefined;
      }
      plaintextPacket = this.input.subarray(0, 4 + packetLength);
      const mac = this.input.subarray(4 + packetLength, total);
      this.input = this.input.subarray(total);
      this.verifyMac(plaintextPacket, mac);
    }

    this.session.remoteSequenceNumber =
      (this.session.remoteSequenceNumber + 1) >>> 0;

    const packetLength = plaintextPacket.readUInt32BE(0);
    const paddingLength = plaintextPacket[4] ?? 0;
    const payloadLength = packetLength - paddingLength - 1;
    if (payloadLength < 1 || 5 + payloadLength > plaintextPacket.length) {
      throw new Error("ssh: malformed packet payload");
    }
    return Buffer.from(plaintextPacket.subarray(5, 5 + payloadLength));
  }

  private verifyMac(plaintextPacket: Buffer, mac: Buffer): void {
    if (this.remoteMac === undefined) {
      return;
    }
    const expected = this.remoteMac.compute(
      this.session.remoteSequenceNumber,
      plaintextPacket,
    );
    if (expected.length !== mac.length || !expected.equals(mac)) {
      throw new Error("ssh: MAC verification failed");
    }
  }

  private sendBlockSize(): number {
    return this.encryptor === undefined
      ? 8
      : Math.max(cipherSpec(this.session.localEncryption()).blockSize, 8);
  }

  private receiveBlockSize(): number {
    return this.decryptor === undefined
      ? 8
      : Math.max(cipherSpec(this.session.remoteEncryption()).blockSize, 8);
  }

  private assertServer(message: string): void {
    if (this.session.role !== "server") {
      throw new Error(`ssh: received ${message} as client`);
    }
  }

  private assertClient(message: string): void {
    if (this.session.role !== "client") {
      throw new Error(`ssh: received ${message} as server`);
    }
  }
}

function indexOfLineEnd(
  buffer: Buffer,
): { index: number; next: number } | undefined {
  for (let i = 0; i < buffer.length; i += 1) {
    if (buffer[i] === 0x0a) {
      let end = i;
      if (end > 0 && buffer[end - 1] === 0x0d) {
        end -= 1;
      }
      return { index: end, next: i + 1 };
    }
  }
  return undefined;
}

/** mpintToMagnitude strips the optional leading sign byte from mpint bytes. */
function mpintToMagnitude(mpint: Uint8Array): Buffer {
  const bytes = Buffer.from(mpint);
  if (bytes.length > 1 && bytes[0] === 0) {
    return bytes.subarray(1);
  }
  return bytes;
}

/** encodeMpintBytes frames a magnitude as a length-prefixed mpint. */
function encodeMpintBytes(magnitude: Uint8Array): Buffer {
  const writer = new ByteWriter();
  writer.mpint(bigintFromBuffer(magnitude));
  return writer.bytes();
}
