import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import {
  loadConfig,
  looksLikeJson,
  parseConfig,
  parseJsonRuntimeConfig,
} from "@/config/load.js";

const tempDirs: string[] = [];

function makeTempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "wanproxy-load-"));
  tempDirs.push(dir);
  return dir;
}

afterEach(() => {
  for (const dir of tempDirs.splice(0)) {
    rmSync(dir, { force: true, recursive: true });
  }
});

describe("looksLikeJson", () => {
  it("detects a leading brace, skipping blanks and comments", () => {
    expect(looksLikeJson('{ "proxies": [] }')).toBe(true);
    expect(looksLikeJson('# comment\n\n  { "proxies": [] }')).toBe(true);
    expect(looksLikeJson("create interface if0")).toBe(false);
    expect(looksLikeJson("")).toBe(false);
    expect(looksLikeJson("# only a comment")).toBe(false);
  });
});

describe("parseJsonRuntimeConfig", () => {
  it("maps an incoming role onto the peer codec slot", () => {
    const config = parseJsonRuntimeConfig(
      JSON.stringify({
        proxies: [
          {
            codec: { mode: "zlib-xcodec", role: "incoming" },
            listen: { host: "127.0.0.1", port: 3300 },
            name: "p0",
            upstream: { host: "127.0.0.1", port: 3301 },
          },
        ],
      }),
    );
    const proxy = config.proxies[0]!;
    expect(proxy.name).toBe("p0");
    expect(proxy.interface.port).toBe("3300");
    expect(proxy.peer.port).toBe("3301");
    expect(proxy.interfaceCodec).toBeUndefined();
    expect(proxy.peerCodec?.mode).toBe("zlib-xcodec");
    expect(proxy.peerCodec?.cache).toBeDefined();
    expect(config.caches).toHaveLength(1);
  });

  it("maps an outgoing role onto the interface codec slot", () => {
    const config = parseJsonRuntimeConfig(
      JSON.stringify({
        proxies: [
          {
            codec: { compressorLevel: 9, mode: "zlib", role: "outgoing" },
            listen: { host: "h", port: 1 },
            name: "p",
            upstream: { host: "h", port: 2 },
          },
        ],
      }),
    );
    const proxy = config.proxies[0]!;
    expect(proxy.peerCodec).toBeUndefined();
    expect(proxy.interfaceCodec?.mode).toBe("zlib");
    expect(proxy.interfaceCodec?.compressorLevel).toBe(9);
    expect(proxy.interfaceCodec?.cache).toBeUndefined();
  });

  it("adapts socks proxies and the monitor", () => {
    const config = parseJsonRuntimeConfig(
      JSON.stringify({
        monitor: { listen: { host: "127.0.0.1", port: 9900 } },
        socksProxies: [{ listen: { host: "h", port: 1080 }, name: "socks0" }],
      }),
    );
    expect(config.socksProxies[0]?.name).toBe("socks0");
    expect(config.socksProxies[0]?.interface.port).toBe("1080");
    expect(config.monitor?.interface.port).toBe("9900");
    expect(config.monitor?.name).toBe("monitor");
  });
});

describe("parseConfig", () => {
  it("routes JSON documents to the JSON adapter", () => {
    const config = parseConfig(
      JSON.stringify({
        proxies: [
          {
            listen: { host: "h", port: 1 },
            name: "p",
            upstream: { host: "h", port: 2 },
          },
        ],
      }),
    );
    expect(config.proxies[0]?.name).toBe("p");
    expect(config.proxies[0]?.peerCodec?.mode).toBe("none");
  });

  it("routes classic documents to the classic parser", () => {
    const config = parseConfig(`
create interface if0
set if0.family IP
set if0.host "h"
set if0.port "1"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "h"
set peer0.port "2"
activate peer0
create proxy p0
set p0.interface if0
set p0.peer peer0
activate p0
`);
    expect(config.proxies[0]?.name).toBe("p0");
    expect(config.objects.length).toBeGreaterThan(0);
  });
});

describe("loadConfig", () => {
  it("reads and parses a JSON file from disk", async () => {
    const dir = makeTempDir();
    const path = join(dir, "proxy.json");
    writeFileSync(
      path,
      JSON.stringify({
        proxies: [
          {
            listen: { host: "127.0.0.1", port: 0 },
            name: "disk-proxy",
            upstream: { host: "127.0.0.1", port: 1 },
          },
        ],
      }),
    );
    const config = await loadConfig(path);
    expect(config.proxies[0]?.name).toBe("disk-proxy");
  });

  it("reads and parses a classic file from disk", async () => {
    const dir = makeTempDir();
    const path = join(dir, "wanproxy.conf");
    writeFileSync(
      path,
      `create interface if0
set if0.family IP
set if0.host "h"
set if0.port "1"
activate if0
create proxy-socks s0
set s0.interface if0
activate s0
`,
    );
    const config = await loadConfig(path);
    expect(config.socksProxies[0]?.name).toBe("s0");
  });
});
