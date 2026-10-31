import type { CodecConfig } from "../config/codec-config.js";
import type { NetworkEndpoint } from "../config/network-endpoint.js";
import type { CodecStatsSnapshot } from "../pipeline/codec-stats.js";

export interface BoundAddress {
  readonly address: string;
  readonly port: number;
}

/**
 * CodecStatus is the codec slot of a TCP proxy in status output.  It
 * extends the configured codec with the Go proxy.CodecStatus statistics
 * fields: `trackStatistics` is set when the classic config activated
 * `track_statistics`, and `stats` carries the four boundary counters.
 */
export interface CodecStatus extends CodecConfig {
  readonly trackStatistics?: boolean;
  readonly stats?: CodecStatsSnapshot;
}

export interface TcpProxyStatus {
  readonly acceptedConnections: number;
  readonly activeSockets: number;
  readonly address?: BoundAddress;
  readonly codec: CodecStatus;
  readonly kind: "tcp";
  readonly listen: NetworkEndpoint;
  readonly listening: boolean;
  readonly name: string;
  readonly upstream: NetworkEndpoint;
}

export interface SocksProxyStatus {
  readonly acceptedConnections: number;
  readonly activeSockets: number;
  readonly address?: BoundAddress;
  readonly kind: "socks";
  readonly listen: NetworkEndpoint;
  readonly listening: boolean;
  readonly name: string;
}

export type ProxyStatus = TcpProxyStatus | SocksProxyStatus;

export interface FleetStatus {
  readonly generatedAt: string;
  readonly proxies: readonly ProxyStatus[];
}
