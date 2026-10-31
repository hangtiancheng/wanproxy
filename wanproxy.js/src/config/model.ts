/**
 * The unified runtime configuration model, mirroring the Go config
 * package's Config/ProxyConfig/SocksConfig/MonitorConfig and the C++
 * activated config objects.  Both the classic wanproxy.conf directive
 * format and the JSON format parse into this model.
 */

import type { LogLevel } from "../common/log.js";
import type { CodecStats } from "../pipeline/codec-stats.js";
import type { RsaHostKey } from "../ssh/crypto.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";

/** AddressFamily mirrors the C++ SocketAddressFamily values. */
export type AddressFamily = "IP" | "IPv4" | "IPv6" | "Unix";

/**
 * Endpoint is one listen or peer address, mirroring the C++
 * ConfigClassAddress instance (family, host, port, path).  Host and port
 * are strings, exactly as the classic config stores them.
 */
export interface Endpoint {
  readonly family: AddressFamily;
  readonly host: string;
  readonly port: string;
  readonly path: string;
}

/** CodecMode is the resolved pipeline mode of an activated codec. */
export type CodecMode = "none" | "zlib" | "xcodec" | "zlib-xcodec";

/**
 * CodecSpec is one activated codec instance: the C++ "codec" config object
 * (XCodec on/off, zlib compressor on/off, level, cache, statistics).
 */
export interface CodecSpec {
  readonly mode: CodecMode;
  /** zlib level, -1 for the default. */
  readonly compressorLevel: number;
  /** The cache backing an XCodec session; undefined for non-XCodec modes. */
  readonly cache: XCodecCache | undefined;
  readonly trackStatistics: boolean;
  readonly stats: CodecStats | undefined;
}

/** SSHProxySettings holds the loaded SSH server host key, mirroring C++. */
export interface SSHProxySettings {
  readonly hostKey: RsaHostKey;
  readonly hostKeyPath: string;
}

/** ProxyConfig is one activated TCP-TCP (or SSH-SSH) proxy. */
export interface RuntimeProxyConfig {
  readonly name: string;
  readonly interface: Endpoint;
  readonly interfaceCodec: CodecSpec | undefined;
  readonly peer: Endpoint;
  readonly peerCodec: CodecSpec | undefined;
  /** Non-undefined for SSH-SSH proxies. */
  readonly ssh: SSHProxySettings | undefined;
}

/** SocksConfig is one activated SOCKS proxy. */
export interface RuntimeSocksConfig {
  readonly name: string;
  readonly interface: Endpoint;
}

/** RuntimeMonitorConfig is one activated monitor. */
export interface RuntimeMonitorConfig {
  readonly name: string;
  readonly interface: Endpoint;
}

/** ObjectField is one field of a config object, retained for export. */
export interface ObjectField {
  readonly name: string;
  readonly type: string;
  readonly value: string;
}

/** ConfigObject is a created config object, retained for monitor export. */
export interface ConfigObject {
  readonly name: string;
  readonly class: string;
  readonly fields: readonly ObjectField[];
}

/** RuntimeConfig is the fully loaded runtime configuration. */
export interface RuntimeConfig {
  readonly proxies: readonly RuntimeProxyConfig[];
  readonly socksProxies: readonly RuntimeSocksConfig[];
  readonly monitor: RuntimeMonitorConfig | undefined;
  /** The level requested by log-mask objects. */
  readonly logLevel: LogLevel;
  /** Every created object, for monitor export. */
  readonly objects: readonly ConfigObject[];
  /** The activated caches, so the owner can close them. */
  readonly caches: readonly XCodecCache[];
  /** Opened XCDFS disk volumes, so the owner can close them. */
  readonly disks: readonly DiskHandle[];
}

/** DiskHandle is the closeable surface of an opened XCDFS volume. */
export interface DiskHandle {
  close(): void;
}

/**
 * parseAddressFamily maps a C++ address family name, defaulting to IP for
 * the empty string, mirroring proxy.ParseFamily.
 */
export function parseAddressFamily(name: string): AddressFamily {
  switch (name) {
    case "IP":
    case "":
      return "IP";
    case "IPv4":
      return "IPv4";
    case "IPv6":
      return "IPv6";
    case "Unix":
      return "Unix";
    default:
      throw new Error(`proxy: unknown address family "${name}"`);
  }
}
