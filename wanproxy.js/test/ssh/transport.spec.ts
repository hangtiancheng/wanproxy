import { once } from "node:events";
import {
  type AddressInfo,
  connect,
  createServer,
  type Server,
  type Socket,
} from "node:net";
import { afterEach, describe, expect, it } from "vitest";

import { Message, RsaHostKey, SshTransport } from "@/ssh/index.js";

interface Harness {
  readonly client: Socket;
  readonly server: Socket;
  readonly listener: Server;
}

async function socketPair(): Promise<Harness> {
  const listener = createServer({ allowHalfOpen: true });
  listener.listen(0, "127.0.0.1");
  await once(listener, "listening");
  const port = (listener.address() as AddressInfo).port;

  const connection = once(listener, "connection") as Promise<[Socket]>;
  const client = connect(port, "127.0.0.1");
  await once(client, "connect");
  const [server] = await connection;
  return { client, listener, server };
}

describe("SshTransport", () => {
  let harness: Harness | undefined;

  afterEach(() => {
    harness?.client.destroy();
    harness?.server.destroy();
    harness?.listener.close();
    harness = undefined;
  });

  it("completes a handshake and exchanges encrypted payloads", async () => {
    harness = await socketPair();
    const hostKey = RsaHostKey.generate();

    const clientTransport = new SshTransport(harness.client, {
      role: "client",
    });
    const serverTransport = new SshTransport(harness.server, {
      hostKey,
      role: "server",
    });

    const clientGot = firstPayload(clientTransport);
    const serverGot = firstPayload(serverTransport);

    await Promise.all([clientTransport.start(), serverTransport.start()]);

    // A successful key exchange yields the same session id on both sides.
    expect(Buffer.from(clientTransport.sessionId).toString("hex")).toBe(
      Buffer.from(serverTransport.sessionId).toString("hex"),
    );
    expect(clientTransport.remoteVersionString).toMatch(/^SSH-2\.0-/);

    const payload = Uint8Array.from([
      Message.Ignore,
      ...Buffer.from("hello over ssh", "utf8"),
    ]);
    clientTransport.send(payload);
    serverTransport.send(payload);

    const [onServer, onClient] = await Promise.all([serverGot, clientGot]);
    expect(Buffer.from(onServer)).toEqual(Buffer.from(payload));
    expect(Buffer.from(onClient)).toEqual(Buffer.from(payload));
  });

  it("rejects a server transport without a host key", async () => {
    const pair = await socketPair();
    harness = pair;
    expect(() => new SshTransport(pair.server, { role: "server" })).toThrow(
      /host key/,
    );
  });

  it("encrypts application payloads on the wire", async () => {
    harness = await socketPair();
    const hostKey = RsaHostKey.generate();
    const clientWire = recordWrites(harness.client);

    const clientTransport = new SshTransport(harness.client, {
      role: "client",
    });
    const serverTransport = new SshTransport(harness.server, {
      hostKey,
      role: "server",
    });
    const serverGot = firstPayload(serverTransport);

    await Promise.all([clientTransport.start(), serverTransport.start()]);

    const secret = Buffer.from("top-secret-payload", "utf8");
    clientTransport.send(Uint8Array.from([Message.Ignore, ...secret]));
    await serverGot;

    // The plaintext must reach the peer decrypted, yet never appear in the
    // bytes actually written to the wire.
    expect(clientWire.written().includes(secret)).toBe(false);
  });
});

function recordWrites(socket: Socket): { written(): Buffer } {
  const chunks: Buffer[] = [];
  const original = socket.write.bind(socket);
  socket.write = ((chunk: Uint8Array | string, ...rest: unknown[]): boolean => {
    chunks.push(
      typeof chunk === "string" ? Buffer.from(chunk) : Buffer.from(chunk),
    );
    return (original as (...args: unknown[]) => boolean)(chunk, ...rest);
  }) as typeof socket.write;
  return { written: () => Buffer.concat(chunks) };
}

function firstPayload(transport: SshTransport): Promise<Uint8Array> {
  return new Promise((resolve) => {
    transport.onPayload((payload) => resolve(payload));
  });
}
