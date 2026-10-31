import { once } from "node:events";
import { type AddressInfo, connect, createServer, type Server } from "node:net";
import { describe, expect, it } from "vitest";

import { SshTunnelClient, SshTunnelServer } from "@/proxy/ssh-proxy.js";
import { RsaHostKey } from "@/ssh/index.js";
import { concatByteArrays } from "@/xcodec/index.js";

async function createEchoServer(): Promise<{
  readonly port: number;
  readonly server: Server;
}> {
  const server = createServer({ allowHalfOpen: true }, (socket) => {
    socket.on("data", (chunk: Uint8Array) => socket.write(chunk));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return { port: (server.address() as AddressInfo).port, server };
}

describe("SSH tunnel proxy", () => {
  it("tunnels a local connection over SSH to a target", async () => {
    const echo = await createEchoServer();
    const hostKey = RsaHostKey.generate();
    const sshServer = new SshTunnelServer({
      hostKey,
      listen: { host: "127.0.0.1", port: 0 },
      target: { host: "127.0.0.1", port: echo.port },
    });
    await sshServer.start();
    const sshClient = new SshTunnelClient({
      listen: { host: "127.0.0.1", port: 0 },
      sshServer: { host: "127.0.0.1", port: sshServer.address.port },
    });
    await sshClient.start();

    try {
      const socket = connect(sshClient.address.port, "127.0.0.1");
      await once(socket, "connect");

      const payload = Buffer.from("tunneled over an encrypted ssh link");
      const echoed = new Promise<Buffer>((resolve) => {
        const chunks: Uint8Array[] = [];
        socket.on("data", (chunk: Uint8Array) => {
          chunks.push(chunk.slice());
          const total = concatByteArrays(chunks);
          if (total.length >= payload.length) {
            resolve(Buffer.from(total.subarray(0, payload.length)));
          }
        });
      });

      socket.write(payload);
      await expect(echoed).resolves.toEqual(payload);
      socket.destroy();
    } finally {
      await sshClient.stop();
      await sshServer.stop();
      echo.server.close();
    }
  });
});
