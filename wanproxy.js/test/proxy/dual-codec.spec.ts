import { once } from "node:events";
import { connect, createServer, type Server } from "node:net";
import { deflateSync, inflateSync } from "node:zlib";

import { describe, expect, it } from "vitest";

import { CodecStats } from "@/pipeline/codec-stats.js";
import { TcpProxyServer } from "@/proxy/tcp-proxy.js";
import { concatByteArrays } from "@/xcodec/index.js";

async function createEchoServer(): Promise<{
  readonly port: number;
  readonly server: Server;
}> {
  const server = createServer({ allowHalfOpen: true }, (socket) => {
    socket.on("data", (chunk: Uint8Array) => socket.write(chunk));
    socket.on("end", () => socket.end());
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("echo server did not bind");
  }
  return { port: address.port, server };
}

describe("dual-codec TCP proxy", () => {
  it("transcodes between an interface and a peer codec", async () => {
    const echo = await createEchoServer();
    const ifaceStats = new CodecStats();
    const peerStats = new CodecStats();
    const proxy = new TcpProxyServer(
      {
        codec: { mode: "zlib", role: "incoming" },
        listen: { host: "127.0.0.1", port: 0 },
        name: "dual",
        upstream: { host: "127.0.0.1", port: echo.port },
      },
      {
        iface: { compressorLevel: 6, mode: "zlib" },
        ifaceStats,
        peer: { compressorLevel: 6, mode: "zlib" },
        peerStats,
      },
    );
    try {
      await proxy.start();
      const socket = connect(proxy.address.port, "127.0.0.1");
      await once(socket, "connect");

      const payload = Buffer.from(
        "dual codec transcodes between two zlib links",
      );
      const chunks: Uint8Array[] = [];
      const received = new Promise<Buffer>((resolve) => {
        socket.on("data", (chunk: Uint8Array) => {
          chunks.push(chunk.slice());
          const total = concatByteArrays(chunks);
          // The response is zlib-compressed; wait for enough bytes to inflate.
          try {
            const inflated = inflateSync(Buffer.from(total));
            resolve(inflated);
          } catch {
            // Incomplete deflate stream yet; keep collecting.
          }
        });
      });

      // The client speaks the interface codec: send compressed data.
      socket.end(deflateSync(payload));
      await expect(received).resolves.toEqual(payload);

      // The interface codec saw the compressed request decompress to the
      // payload, and the peer codec re-compressed it toward the echo server.
      const iface = ifaceStats.snapshot();
      const peer = peerStats.snapshot();
      expect(iface.incomingToCodecBytes).toBeGreaterThan(0);
      expect(iface.codecToIncomingBytes).toBeGreaterThan(0);
      expect(peer.codecToOutgoingBytes).toBeGreaterThan(0);
      expect(peer.outgoingToCodecBytes).toBeGreaterThan(0);
      // Internal boundaries agree: plaintext leaving the interface codec
      // equals plaintext entering the peer codec.
      expect(iface.codecToOutgoingBytes).toBe(peer.incomingToCodecBytes);
      expect(peer.codecToIncomingBytes).toBe(iface.outgoingToCodecBytes);
    } finally {
      await proxy.stop();
      echo.server.close();
    }
  });
});
