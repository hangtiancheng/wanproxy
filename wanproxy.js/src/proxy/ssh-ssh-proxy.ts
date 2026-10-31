import { connect, createServer, type Server, type Socket } from "node:net";

import type { NetworkEndpoint } from "../config/network-endpoint.js";
import type { RsaHostKey } from "../ssh/crypto.js";
import { SshStream, SshTransport } from "../ssh/index.js";
import type { TcpProxyStatus } from "./proxy-status.js";
import { closeServer, listen } from "./socket-utils.js";
import { renderCodecStatus, type TcpProxyRuntime } from "./tcp-proxy.js";
import {
  relayStreams,
  type RelayCodecSpec,
  type RelayStats,
} from "./tcp-relay.js";

/** Handshake budget for each SSH transport, mirroring the Go listener's 15s. */
const HANDSHAKE_TIMEOUT_MS = 15_000;

export interface SshSshProxyConfig {
  readonly name: string;
  readonly listen: NetworkEndpoint;
  readonly upstream: NetworkEndpoint;
  readonly hostKey: RsaHostKey;
}

/**
 * SshSshProxyServer runs the SSH-SSH proxy type, mirroring the C++
 * SSHProxyListener/SSHProxyConnector pair and the Go Listener with
 * SSHSettings: the interface hop runs an SSH server transport and the peer
 * hop an SSH client transport, and the codec pipeline is spliced between
 * the two decrypted streams.
 *
 * Packet tagging follows the C++ SSHStream framing rule: a hop is
 * 0xff-tagged exactly when the codec on that hop is active.
 */
export class SshSshProxyServer {
  private acceptedConnections = 0;
  private readonly connections = new Set<Socket>();
  private readonly ifaceSpec: RelayCodecSpec | undefined;
  private readonly peerSpec: RelayCodecSpec | undefined;
  private readonly relayStats: RelayStats | undefined;
  private readonly server: Server;

  public constructor(
    private readonly config: SshSshProxyConfig,
    runtime: TcpProxyRuntime = {},
  ) {
    this.server = createServer({ allowHalfOpen: true }, (socket) => {
      void this.accept(socket);
    });
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
  }

  public get address(): { readonly address: string; readonly port: number } {
    const address = this.server.address();
    if (address === null || typeof address === "string") {
      throw new Error("SSH-SSH proxy server is not listening on a TCP address");
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
    const base: Omit<TcpProxyStatus, "address"> = {
      acceptedConnections: this.acceptedConnections,
      activeSockets: this.connections.size,
      codec: renderCodecStatus(this.ifaceSpec, this.peerSpec, this.relayStats, {
        mode: "none",
        role: "incoming",
      }),
      kind: "tcp",
      listen: this.config.listen,
      listening: this.server.listening,
      name: this.config.name,
      upstream: this.config.upstream,
    };
    return address === undefined ? base : { ...base, address };
  }

  private async accept(client: Socket): Promise<void> {
    this.acceptedConnections += 1;
    this.track(client);
    const upstream = connect({
      allowHalfOpen: true,
      host: this.config.upstream.host,
      port: this.config.upstream.port,
    });
    this.track(upstream);
    upstream.once("error", () => client.destroy());
    try {
      await onceConnect(upstream);
      const streams = await this.handshakeSsh(client, upstream);
      this.relay(
        streams.clientStream,
        streams.upstreamStream,
        client,
        upstream,
      );
    } catch {
      client.destroy();
      upstream.destroy();
    }
  }

  /**
   * handshakeSsh runs the interface-side SSH server transport and the
   * peer-side SSH client transport concurrently, mirroring the Go
   * Listener.handshakeSSH.
   */
  private async handshakeSsh(
    client: Socket,
    upstream: Socket,
  ): Promise<{ clientStream: SshStream; upstreamStream: SshStream }> {
    client.setTimeout(HANDSHAKE_TIMEOUT_MS);
    upstream.setTimeout(HANDSHAKE_TIMEOUT_MS);
    const serverTransport = new SshTransport(client, {
      hostKey: this.config.hostKey,
      role: "server",
      tagged: codecActive(this.ifaceSpec),
    });
    const clientTransport = new SshTransport(upstream, {
      role: "client",
      tagged: codecActive(this.peerSpec),
    });
    try {
      await Promise.all([serverTransport.start(), clientTransport.start()]);
    } finally {
      client.setTimeout(0);
      upstream.setTimeout(0);
    }
    return {
      clientStream: new SshStream(serverTransport),
      upstreamStream: new SshStream(clientTransport),
    };
  }

  private relay(
    clientStream: SshStream,
    upstreamStream: SshStream,
    client: Socket,
    upstream: Socket,
  ): void {
    const relay = relayStreams(
      clientStream,
      upstreamStream,
      this.ifaceSpec,
      this.peerSpec,
      this.relayStats,
    );
    relay.closed
      .catch(() => undefined)
      .finally(() => {
        this.connections.delete(client);
        this.connections.delete(upstream);
      });
  }

  private track(socket: Socket): void {
    this.connections.add(socket);
    socket.once("close", () => this.connections.delete(socket));
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

function codecActive(spec: RelayCodecSpec | undefined): boolean {
  return spec !== undefined && spec.mode !== "none";
}

function onceConnect(socket: Socket): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    socket.once("connect", () => resolve());
    socket.once("error", reject);
  });
}
