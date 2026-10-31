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
import { SshSshProxyServer } from "./ssh-ssh-proxy.js";
import { TcpProxyServer, type TcpProxyRuntime } from "./tcp-proxy.js";
import type { RelayCodecSpec } from "./tcp-relay.js";
import type { CodecSpec } from "../config/model.js";

type FleetServer = TcpProxyServer | SshSshProxyServer | SocksProxyServer;

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
 * buildTcpProxy adapts one runtime proxy onto a proxy server.  SSH-SSH
 * proxies run over the SshSshProxyServer; TCP-TCP proxies run over the
 * TcpProxyServer, which supports both the common single-codec topology (a
 * peer codec maps onto the "incoming" role and an interface codec onto the
 * "outgoing" role) and combined interface+peer codecs, mirroring the Go
 * proxy.Fleet.
 */
function buildTcpProxy(
  proxy: RuntimeProxyConfig,
): TcpProxyServer | SshSshProxyServer {
  const peerSpec = toRelaySpec(proxy.peerCodec);
  const listen = {
    host: proxy.interface.host,
    port: Number(proxy.interface.port),
  };
  const upstream = { host: proxy.peer.host, port: Number(proxy.peer.port) };

  if (proxy.ssh !== undefined) {
    return new SshSshProxyServer(
      { hostKey: proxy.ssh.hostKey, listen, name: proxy.name, upstream },
      dualRuntime(proxy),
    );
  }

  // Dual-codec TCP proxy: pass both specs through the runtime.
  if (proxy.interfaceCodec !== undefined && proxy.peerCodec !== undefined) {
    const config: TcpProxyConfig = {
      codec: { mode: peerSpec?.mode ?? "none", role: "incoming" },
      listen,
      name: proxy.name,
      upstream,
    };
    return new TcpProxyServer(config, dualRuntime(proxy));
  }

  // Single-codec (or pass-through) TCP proxy.
  const spec = proxy.interfaceCodec ?? proxy.peerCodec;
  const role: "incoming" | "outgoing" =
    proxy.interfaceCodec !== undefined ? "outgoing" : "incoming";
  const mode = spec?.mode ?? "none";
  const codec =
    spec !== undefined && spec.compressorLevel >= 0
      ? { compressorLevel: spec.compressorLevel, mode, role }
      : { mode, role };
  const config: TcpProxyConfig = { codec, listen, name: proxy.name, upstream };
  const runtime: TcpProxyRuntime = {
    ...(spec?.cache === undefined ? {} : { cache: spec.cache }),
    ...(spec?.stats === undefined ? {} : { stats: spec.stats }),
  };
  return new TcpProxyServer(config, runtime);
}

/** dualRuntime builds the interface/peer relay specs and counters. */
function dualRuntime(proxy: RuntimeProxyConfig): TcpProxyRuntime {
  const ifaceSpec = toRelaySpec(proxy.interfaceCodec);
  const peerSpec = toRelaySpec(proxy.peerCodec);
  return {
    ...(ifaceSpec === undefined ? {} : { iface: ifaceSpec }),
    ...(peerSpec === undefined ? {} : { peer: peerSpec }),
    ...(proxy.interfaceCodec?.stats === undefined
      ? {}
      : { ifaceStats: proxy.interfaceCodec.stats }),
    ...(proxy.peerCodec?.stats === undefined
      ? {}
      : { peerStats: proxy.peerCodec.stats }),
  };
}

/** toRelaySpec maps a config CodecSpec onto the relay's codec spec. */
function toRelaySpec(spec: CodecSpec | undefined): RelayCodecSpec | undefined {
  if (spec === undefined) {
    return undefined;
  }
  return {
    ...(spec.cache === undefined ? {} : { cache: spec.cache }),
    ...(spec.compressorLevel >= 0
      ? { compressorLevel: spec.compressorLevel }
      : {}),
    mode: spec.mode,
  };
}
