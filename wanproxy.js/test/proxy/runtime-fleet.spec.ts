import { once } from "node:events";
import { connect, createServer, type Server } from "node:net";

import { describe, expect, it } from "vitest";

import { ClassicParser } from "@/config/classic.js";
import type { RuntimeConfig } from "@/config/model.js";
import { RuntimeFleet } from "@/proxy/runtime-fleet.js";
import { concatByteArrays } from "@/xcodec/index.js";

async function listen(server: Server): Promise<number> {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("server did not bind to a TCP address");
  }
  return address.port;
}

async function close(server: Server): Promise<void> {
  if (!server.listening) {
    return;
  }
  await new Promise<void>((resolve, reject) => {
    server.close((error) => (error === undefined ? resolve() : reject(error)));
  });
}

async function createEchoServer(): Promise<{
  readonly port: number;
  readonly server: Server;
}> {
  const server = createServer({ allowHalfOpen: true }, (socket) => {
    socket.on("data", (chunk: Uint8Array) => socket.write(chunk));
    socket.on("end", () => socket.end());
  });
  return { port: await listen(server), server };
}

async function request(port: number, bytes: Uint8Array): Promise<Uint8Array> {
  const socket = connect(port, "127.0.0.1");
  await once(socket, "connect");
  const chunks: Uint8Array[] = [];
  socket.on("data", (chunk: Uint8Array) => chunks.push(chunk.slice()));
  socket.end(bytes);
  await once(socket, "end");
  socket.destroy();
  return concatByteArrays(chunks);
}

// A classic wanproxy.conf: a memory cache, an XCodec codec, one TCP-TCP
// proxy with the codec on the peer slot, and a monitor.  Ports are 0 so
// the OS assigns ephemeral ones; the peer port is injected per test.
function classicConfig(echoPort: number): string {
  return `
create cache mem0
set mem0.type Memory
set mem0.size 4MB
activate mem0

create codec codec0
set codec0.codec XCodec
set codec0.cache mem0
set codec0.track_statistics true
activate codec0

create interface if0
set if0.family IP
set if0.host "127.0.0.1"
set if0.port "0"
activate if0

create peer peer0
set peer0.family IP
set peer0.host "127.0.0.1"
set peer0.port "${echoPort}"
activate peer0

create proxy proxy0
set proxy0.type TCP-TCP
set proxy0.interface if0
set proxy0.interface_codec None
set proxy0.peer peer0
set proxy0.peer_codec codec0
activate proxy0

create interface ifm
set ifm.family IP
set ifm.host "127.0.0.1"
set ifm.port "0"
activate ifm

create monitor mon0
set mon0.interface ifm
activate mon0
`;
}

describe("RuntimeFleet", () => {
  it("runs a classic wanproxy.conf XCodec proxy and exports the monitor", async () => {
    const echo = await createEchoServer();
    const config = new ClassicParser().parse(classicConfig(echo.port));
    const fleet = new RuntimeFleet(config);
    try {
      await fleet.start();
      const proxyAddress = fleet.addresses().at(0);
      const monitorAddress = fleet.monitorAddress();
      expect(proxyAddress).toBeDefined();
      expect(monitorAddress).toBeDefined();

      // XCodec dedup round-trips repetitive data through the echo server.
      const input = new Uint8Array(2048 * 4).fill(0x5a);
      await expect(request(proxyAddress!.port, input)).resolves.toEqual(input);

      // The monitor JSON status lists the running proxy, and the activated
      // track_statistics counters reflect the transfer.
      const statusResponse = await fetch(
        `http://127.0.0.1:${monitorAddress!.port}/status`,
      );
      const status = (await statusResponse.json()) as {
        proxies: {
          name: string;
          kind: string;
          codec?: {
            mode: string;
            trackStatistics?: boolean;
            stats?: {
              incomingToCodecBytes: number;
              codecToOutgoingBytes: number;
              outgoingToCodecBytes: number;
              codecToIncomingBytes: number;
            };
          };
        }[];
      };
      expect(status.proxies.some((p) => p.name === "proxy0")).toBe(true);
      const proxy0 = status.proxies.find((p) => p.name === "proxy0");
      expect(proxy0?.codec?.trackStatistics).toBe(true);
      expect(proxy0?.codec?.stats?.incomingToCodecBytes).toBe(input.length);
      expect(proxy0?.codec?.stats?.codecToIncomingBytes).toBe(input.length);
      // The wire side carries the XCodec-framed stream; the echo replays it
      // verbatim, so both wire counters agree and exceed zero.
      expect(proxy0?.codec?.stats?.codecToOutgoingBytes).toBeGreaterThan(0);
      expect(proxy0?.codec?.stats?.outgoingToCodecBytes).toBe(
        proxy0?.codec?.stats?.codecToOutgoingBytes,
      );

      // The monitor HTML root exports the activated config objects.
      const htmlResponse = await fetch(
        `http://127.0.0.1:${monitorAddress!.port}/`,
      );
      const html = await htmlResponse.text();
      expect(html).toContain("codec0");
      expect(html).toContain("proxy0");
    } finally {
      await fleet.stop();
      await close(echo.server);
    }
  });

  it("rejects SSH-SSH proxies at runtime", () => {
    const config: RuntimeConfig = {
      caches: [],
      disks: [],
      logLevel: "info",
      monitor: undefined,
      objects: [],
      proxies: [
        {
          interface: { family: "IP", host: "h", path: "", port: "1" },
          interfaceCodec: undefined,
          name: "ssh0",
          peer: { family: "IP", host: "h", path: "", port: "2" },
          peerCodec: undefined,
          ssh: {
            hostKey: undefined as never,
            hostKeyPath: "k.pem",
          },
        },
      ],
      socksProxies: [],
    };
    expect(() => new RuntimeFleet(config)).toThrow(/SSH-SSH/);
  });

  it("rejects combined interface and peer codecs at runtime", () => {
    const spec = {
      cache: undefined,
      compressorLevel: -1,
      mode: "none" as const,
      stats: undefined,
      trackStatistics: false,
    };
    const config: RuntimeConfig = {
      caches: [],
      disks: [],
      logLevel: "info",
      monitor: undefined,
      objects: [],
      proxies: [
        {
          interface: { family: "IP", host: "h", path: "", port: "1" },
          interfaceCodec: spec,
          name: "dual0",
          peer: { family: "IP", host: "h", path: "", port: "2" },
          peerCodec: spec,
          ssh: undefined,
        },
      ],
      socksProxies: [],
    };
    expect(() => new RuntimeFleet(config)).toThrow(/combined interface/);
  });
});
