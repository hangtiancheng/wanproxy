/**
 * RuntimeFleet runs the unified runtime configuration model produced by
 * either the classic wanproxy.conf parser or the JSON adapter.  It builds
 * TCP-TCP proxies (injecting the activated XCodec cache so chained proxies
 * share it), SOCKS proxies, and the monitor with its config-object export,
 * mirroring the Go proxy.Fleet driven by cmd/wanproxy.
 */

import type { RuntimeConfig, RuntimeProxyConfig } from "../config/model.js";
import type {
  SocksProxyConfig,
  TcpProxyConfig,
} from "../config/proxy-config.js";
import { MonitorServer } from "../monitor/monitor-server.js";
import type { BoundAddress, FleetStatus, ProxyStatus } from "./proxy-status.js";
import { SocksProxyServer } from "./socks-proxy.js";
import { TcpProxyServer, type TcpProxyRuntime } from "./tcp-proxy.js";

type FleetServer = TcpProxyServer | SocksProxyServer;

export class RuntimeFleet {
  private readonly config: RuntimeConfig;
  private readonly monitor: MonitorServer | undefined;
  private readonly servers: readonly FleetServer[];

  public constructor(config: RuntimeConfig) {
    this.config = config;
    this.servers = [
      ...config.proxies.map((proxy) => buildTcpProxy(proxy)),
      ...config.socksProxies.map(
        (socks): SocksProxyServer =>
          new SocksProxyServer({
            listen: {
              host: socks.interface.host,
              port: Number(socks.interface.port),
            },
            name: socks.name,
          } satisfies SocksProxyConfig),
      ),
    ];
    this.monitor =
      config.monitor === undefined
        ? undefined
        : new MonitorServer(
            {
              listen: {
                host: config.monitor.interface.host,
                port: Number(config.monitor.interface.port),
              },
            },
            {
              objects: () => this.config.objects,
              status: () => this.status(),
            },
          );
  }

  public async start(): Promise<void> {
    await Promise.all(this.servers.map((server) => server.start()));
    await this.monitor?.start();
  }

  public async stop(): Promise<void> {
    await this.monitor?.stop();
    await Promise.all(this.servers.map((server) => server.stop()));
    for (const disk of this.config.disks) {
      disk.close();
    }
  }

  public addresses(): readonly BoundAddress[] {
    return this.servers.map((server) => server.address);
  }

  public monitorAddress(): BoundAddress | undefined {
    return this.monitor?.address;
  }

  public status(): FleetStatus {
    return {
      generatedAt: new Date().toISOString(),
      proxies: this.servers.map((server): ProxyStatus => server.status()),
    };
  }
}

/**
 * buildTcpProxy adapts one runtime proxy onto the TCP proxy server.  The
 * common WAN-chain topology configures at most one codec per proxy: a peer
 * codec maps onto the "incoming" role and an interface codec onto the
 * "outgoing" role, exactly like the JSON adapter.  SSH-SSH proxies and
 * combined interface+peer codecs parse and validate but are not yet
 * runnable here (see the README residual gaps).
 */
function buildTcpProxy(proxy: RuntimeProxyConfig): TcpProxyServer {
  if (proxy.ssh !== undefined) {
    throw new Error(
      `proxy "${proxy.name}": SSH-SSH proxies are not yet runnable by the TypeScript fleet`,
    );
  }
  if (proxy.interfaceCodec !== undefined && proxy.peerCodec !== undefined) {
    throw new Error(
      `proxy "${proxy.name}": combined interface and peer codecs are not yet runnable by the TypeScript fleet`,
    );
  }

  const spec = proxy.interfaceCodec ?? proxy.peerCodec;
  const role: "incoming" | "outgoing" =
    proxy.interfaceCodec !== undefined ? "outgoing" : "incoming";
  const mode = spec?.mode ?? "none";
  const codec =
    spec !== undefined && spec.compressorLevel >= 0
      ? { compressorLevel: spec.compressorLevel, mode, role }
      : { mode, role };

  const config: TcpProxyConfig = {
    codec,
    listen: { host: proxy.interface.host, port: Number(proxy.interface.port) },
    name: proxy.name,
    upstream: { host: proxy.peer.host, port: Number(proxy.peer.port) },
  };
  const runtime: TcpProxyRuntime =
    spec?.cache === undefined ? {} : { cache: spec.cache };
  return new TcpProxyServer(config, runtime);
}
