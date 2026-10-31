/**
 * loadConfig reads a configuration file, detecting the format: files whose
 * first non-space, non-comment byte is '{' are parsed as JSON (the
 * TypeScript port format), everything else as the classic wanproxy.conf
 * directive format of the C++ original.  Both produce the same runtime
 * model, mirroring the Go config.Load.
 */

import { readFile } from "node:fs/promises";

import { MemoryCache } from "../xcodec/memory-cache.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";
import { ClassicParser } from "./classic.js";
import type {
  CodecMode,
  CodecSpec,
  Endpoint,
  RuntimeConfig,
  RuntimeMonitorConfig,
  RuntimeProxyConfig,
  RuntimeSocksConfig,
} from "./model.js";
import { parseWanproxyConfig } from "./proxy-config.js";

/** looksLikeJson reports whether the document is in the JSON format. */
export function looksLikeJson(data: string): boolean {
  for (const line of data.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "" || trimmed.startsWith("#")) {
      continue;
    }
    return trimmed.startsWith("{");
  }
  return false;
}

/** parseConfig detects the format of an in-memory configuration. */
export function parseConfig(data: string): RuntimeConfig {
  if (looksLikeJson(data)) {
    return parseJsonRuntimeConfig(data);
  }
  return new ClassicParser().parse(data);
}

/** loadConfig reads and parses a configuration file from disk. */
export async function loadConfig(path: string): Promise<RuntimeConfig> {
  const data = await readFile(path, "utf8");
  return parseConfig(data);
}

/**
 * parseJsonRuntimeConfig parses the JSON format and adapts it onto the
 * unified runtime model.  The codec role maps onto the C++ model exactly
 * like the Go port: "incoming" attaches the codec to the peer (upstream)
 * side, "outgoing" attaches it to the interface (listen) side.
 */
export function parseJsonRuntimeConfig(data: string): RuntimeConfig {
  const file = parseWanproxyConfig(JSON.parse(data));
  const proxies: RuntimeProxyConfig[] = [];
  const caches: XCodecCache[] = [];

  for (const proxy of file.proxies ?? []) {
    const codec = proxy.codec;
    const mode = codec.mode as CodecMode;
    const cache = needsXCodecCache(mode) ? new MemoryCache() : undefined;
    if (cache !== undefined) {
      caches.push(cache);
    }
    const spec: CodecSpec = {
      cache,
      compressorLevel: codec.compressorLevel ?? -1,
      mode,
      stats: undefined,
      trackStatistics: false,
    };
    const interfaceCodec = codec.role === "outgoing" ? spec : undefined;
    const peerCodec = codec.role === "incoming" ? spec : undefined;
    proxies.push({
      interface: ipEndpoint(proxy.listen.host, proxy.listen.port),
      interfaceCodec,
      name: proxy.name,
      peer: ipEndpoint(proxy.upstream.host, proxy.upstream.port),
      peerCodec,
      ssh: undefined,
    });
  }

  const socksProxies: RuntimeSocksConfig[] = (file.socksProxies ?? []).map(
    (socks) => ({
      interface: ipEndpoint(socks.listen.host, socks.listen.port),
      name: socks.name,
    }),
  );

  let monitor: RuntimeMonitorConfig | undefined;
  if (file.monitor !== undefined) {
    monitor = {
      interface: ipEndpoint(file.monitor.listen.host, file.monitor.listen.port),
      name: "monitor",
    };
  }

  return {
    caches,
    disks: [],
    logLevel: "info",
    monitor,
    objects: [],
    proxies,
    socksProxies,
  };
}

function ipEndpoint(host: string, port: number): Endpoint {
  return { family: "IP", host, path: "", port: String(port) };
}

function needsXCodecCache(mode: CodecMode): boolean {
  return mode === "xcodec" || mode === "zlib-xcodec";
}
