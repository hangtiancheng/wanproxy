import { once } from "node:events";
import { connect, createServer, type Server, type Socket } from "node:net";

import { describe, expect, it } from "vitest";

import { SshSshProxyServer } from "@/proxy/ssh-ssh-proxy.js";
import { RsaHostKey, SshStream, SshTransport } from "@/ssh/index.js";
import { concatByteArrays } from "@/xcodec/index.js";

/** frame wraps app bytes in the untagged [uint32 length][data] framing. */
function frame(data: Buffer): Buffer {
  const out = Buffer.alloc(4 + data.length);
  out.writeUInt32BE(data.length, 0);
  data.copy(out, 4);
  return out;
}

/**
 * createSshEchoServer accepts SSH connections (untagged) and echoes the
 * application byte stream back, standing in for the wanbench SSH echo
 * server at the far end of the chain.
 */
async function createSshEchoServer(hostKey: RsaHostKey): Promise<{
  readonly port: number;
  readonly server: Server;
}> {
  const server = createServer({ allowHalfOpen: true }, (socket: Socket) => {
    const transport = new SshTransport(socket, { hostKey, role: "server" });
    transport
      .start()
      .then(() => {
        transport.onAppData((data) => transport.writeApp(data));
      })
      .catch(() => socket.destroy());
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("ssh echo server did not bind");
  }
  return { port: address.port, server };
}

describe("SshSshProxyServer", () => {
  it("relays a byte stream over two SSH hops", async () => {
    const hostKey = RsaHostKey.generate();
    const echo = await createSshEchoServer(hostKey);
    const proxy = new SshSshProxyServer({
      hostKey,
      listen: { host: "127.0.0.1", port: 0 },
      name: "ssh-ssh",
      upstream: { host: "127.0.0.1", port: echo.port },
    });
    await proxy.start();

    // The client speaks SSH (untagged) toward the proxy's interface hop.
    const clientSocket = connect(proxy.address.port, "127.0.0.1");
    await once(clientSocket, "connect");
    const clientTransport = new SshTransport(clientSocket, { role: "client" });
    await clientTransport.start();
    const clientStream = new SshStream(clientTransport);

    try {
      const payload = Buffer.from("relayed over two encrypted ssh hops");
      const chunks: Uint8Array[] = [];
      const echoed = new Promise<Buffer>((resolve) => {
        clientStream.on("data", (chunk: Uint8Array) => {
          chunks.push(chunk.slice());
          const total = Buffer.from(concatByteArrays(chunks));
          // Untagged app stream carries [uint32 length][data] frames.
          if (total.length >= 4) {
            const len = total.readUInt32BE(0);
            if (total.length >= 4 + len) {
              resolve(total.subarray(4, 4 + len));
            }
          }
        });
      });

      clientStream.write(frame(payload));
      await expect(echoed).resolves.toEqual(payload);
    } finally {
      clientStream.destroy();
      clientSocket.destroy();
      await proxy.stop();
      echo.server.close();
    }
  });
});
