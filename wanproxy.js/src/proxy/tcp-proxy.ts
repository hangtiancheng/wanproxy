import { connect, createServer, type Server, type Socket } from "node:net";

import type { TcpProxyConfig } from "../config/proxy-config.js";
import type { CodecStats } from "../pipeline/codec-stats.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";
import { MemoryCache } from "../xcodec/memory-cache.js";
import type { CodecStatus, TcpProxyStatus } from "./proxy-status.js";
import { closeServer, listen } from "./socket-utils.js";
import {
  relayStreams,
  type RelayCodecSpec,
  type RelayStats,
} from "./tcp-relay.js";

function needsXCodecCache(mode: string): boolean {
  return mode === "xcodec" || mode === "zlib-xcodec";
}

/**
 * renderCodecStatus renders a proxy's codec slot for the monitor from the
 * relay's interface/peer specs and counters, preferring the peer
 * (WAN-facing) codec and falling back to the interface codec.  This matches
 * how the Go fleet surfaces the statistics-bearing codec.
 */
export function renderCodecStatus(
  ifaceSpec: RelayCodecSpec | undefined,
  peerSpec: RelayCodecSpec | undefined,
  relayStats: RelayStats | undefined,
  fallback: CodecStatus,
): CodecStatus {
  const slot =
    peerSpec !== undefined
      ? { role: "incoming" as const, spec: peerSpec, stats: relayStats?.peer }
      : ifaceSpec !== undefined
        ? {
            role: "outgoing" as const,
            spec: ifaceSpec,
            stats: relayStats?.iface,
          }
        : undefined;
  if (slot === undefined) {
    return fallback;
  }
  const base: CodecStatus = {
    ...(slot.spec.compressorLevel === undefined
      ? {}
      : { compressorLevel: slot.spec.compressorLevel }),
    mode: slot.spec.mode,
    role: slot.role,
  };
  return slot.stats === undefined
    ? base
    : { ...base, stats: slot.stats.snapshot(), trackStatistics: true };
}

/**
 * TcpProxyRuntime carries runtime dependencies injected by the fleet.
 *
 * The single-codec fields (cache, stats) describe the proxy's one codec,
 * selected by TcpProxyConfig.codec.role.  The dual-codec fields (iface,
 * peer, ifaceStats, peerStats) describe a proxy that carries both an
 * interface and a peer codec, transcoding between them; when any of them
 * is present the relay is built from those specs directly.
 */
export interface TcpProxyRuntime {
  /**
   * The XCodec cache backing the single codec's session.  When omitted, a
   * fresh in-memory cache is created for XCodec modes.  The classic config
   * injects the activated cache (memory, disk or pair) here so that proxies
   * share it.
   */
  readonly cache?: XCodecCache;
  /** The single codec's byte counters, when track_statistics is set. */
  readonly stats?: CodecStats;
  /** Dual-codec interface codec spec (overrides the single-codec path). */
  readonly iface?: RelayCodecSpec;
  /** Dual-codec peer codec spec (overrides the single-codec path). */
  readonly peer?: RelayCodecSpec;
  /** Dual-codec interface codec counters. */
  readonly ifaceStats?: CodecStats;
  /** Dual-codec peer codec counters. */
  readonly peerStats?: CodecStats;
}

export class TcpProxyServer {
  private acceptedConnections = 0;
  private readonly connections = new Set<Socket>();
  private readonly ifaceSpec: RelayCodecSpec | undefined;
  private readonly peerSpec: RelayCodecSpec | undefined;
  private readonly relayStats: RelayStats | undefined;
  private readonly server: Server;

  public constructor(
    private readonly config: TcpProxyConfig,
    runtime: TcpProxyRuntime = {},
  ) {
    this.server = createServer({ allowHalfOpen: true }, (socket) =>
      this.accept(socket),
    );

    if (runtime.iface !== undefined || runtime.peer !== undefined) {
      // Dual-codec proxy: use the injected interface/peer specs directly.
      this.ifaceSpec = runtime.iface;
      this.peerSpec = runtime.peer;
      this.relayStats =
        runtime.ifaceStats === undefined && runtime.peerStats === undefined
          ? undefined
          : {
              ...(runtime.ifaceStats === undefined
                ? {}
                : { iface: runtime.ifaceStats }),
              ...(runtime.peerStats === undefined
                ? {}
                : { peer: runtime.peerStats }),
            };
      return;
    }

    // Single-codec proxy: place the codec on the interface or peer slot
    // according to its role.
    const cache =
      runtime.cache ??
      (needsXCodecCache(config.codec.mode) ? new MemoryCache() : undefined);
    const spec: RelayCodecSpec = {
      ...(cache === undefined ? {} : { cache }),
      ...(config.codec.compressorLevel === undefined
        ? {}
        : { compressorLevel: config.codec.compressorLevel }),
      mode: config.codec.mode,
    };
    if (config.codec.role === "outgoing") {
      this.ifaceSpec = spec;
      this.peerSpec = undefined;
      this.relayStats =
        runtime.stats === undefined ? undefined : { iface: runtime.stats };
    } else {
      this.ifaceSpec = undefined;
      this.peerSpec = spec;
      this.relayStats =
        runtime.stats === undefined ? undefined : { peer: runtime.stats };
    }
  }

  public get address(): { readonly address: string; readonly port: number } {
    const address = this.server.address();
    if (address === null || typeof address === "string") {
      throw new Error("TCP proxy server is not listening on a TCP address");
    }
    return { address: address.address, port: address.port };
  }

  public async start(): Promise<void> {
    await listen(this.server, this.config.listen);
  }

  public async stop(): Promise<void> {
    for (const socket of this.connections) {
      socket.destroy();
    }
    await closeServer(this.server);
  }

  public status(): TcpProxyStatus {
    const address = this.tryAddress();
    return address === undefined
      ? this.baseStatus()
      : { ...this.baseStatus(), address };
  }

  private accept(local: Socket): void {
    this.acceptedConnections += 1;
    this.track(local);
    const upstream = connect({
      allowHalfOpen: true,
      host: this.config.upstream.host,
      port: this.config.upstream.port,
    });
    this.track(upstream);
    upstream.once("connect", () => {
      const relay = relayStreams(
        local,
        upstream,
        this.ifaceSpec,
        this.peerSpec,
        this.relayStats,
      );
      relay.closed
        .catch(() => undefined)
        .finally(() => {
          this.connections.delete(local);
          this.connections.delete(upstream);
        });
    });
    upstream.once("error", () => local.destroy());
  }

  private track(socket: Socket): void {
    this.connections.add(socket);
    socket.once("close", () => this.connections.delete(socket));
  }

  private baseStatus(): Omit<TcpProxyStatus, "address"> {
    return {
      acceptedConnections: this.acceptedConnections,
      activeSockets: this.connections.size,
      codec: this.statusCodec(),
      kind: "tcp",
      listen: this.config.listen,
      listening: this.server.listening,
      name: this.config.name,
      upstream: this.config.upstream,
    };
  }

  /**
   * statusCodec renders the proxy's codec slot for the monitor.  The common
   * single-codec proxy reports its one codec; a dual-codec proxy reports the
   * peer codec (the WAN-facing slot), matching how the Go fleet surfaces the
   * statistics-bearing codec.
   */
  private statusCodec(): CodecStatus {
    return renderCodecStatus(
      this.ifaceSpec,
      this.peerSpec,
      this.relayStats,
      this.config.codec,
    );
  }

  private tryAddress():
    { readonly address: string; readonly port: number } | undefined {
    const address = this.server.address();
    if (address === null || typeof address === "string") {
      return undefined;
    }
    return { address: address.address, port: address.port };
  }
}
