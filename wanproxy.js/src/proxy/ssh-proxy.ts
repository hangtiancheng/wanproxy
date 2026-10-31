import { connect, createServer, type Server, type Socket } from "node:net";

import type { NetworkEndpoint } from "../config/network-endpoint.js";
import { RsaHostKey, SshTransport } from "../ssh/index.js";
import { closeServer, listen } from "./socket-utils.js";

/**
 * SshTunnelServer is the server side of an SSH WAN link, mirroring the C++
 * SSHProxyListener: it accepts SSH transport connections, completes the
 * server handshake, and forwards the decrypted byte stream to the target
 * endpoint.
 */
export class SshTunnelServer {
  private readonly connections = new Set<Socket>();
  private readonly server: Server;

  public constructor(private readonly config: SshTunnelServerConfig) {
    this.server = createServer({ allowHalfOpen: true }, (socket) =>
      this.accept(socket),
    );
  }

  public get address(): { readonly address: string; readonly port: number } {
    const address = this.server.address();
    if (address === null || typeof address === "string") {
      throw new Error("SSH tunnel server is not listening on a TCP address");
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

  private accept(wanSocket: Socket): void {
    this.track(wanSocket);
    const transport = new SshTransport(wanSocket, {
      hostKey: this.config.hostKey,
      role: "server",
    });
    transport
      .start()
      .then(() => {
        const target = connect({
          allowHalfOpen: true,
          host: this.config.target.host,
          port: this.config.target.port,
        });
        this.track(target);
        target.once("error", () => transport.close());
        wireSocketToTransport(target, transport);
      })
      .catch(() => wanSocket.destroy());
  }

  private track(socket: Socket): void {
    this.connections.add(socket);
    socket.once("close", () => this.connections.delete(socket));
  }
}

/**
 * SshTunnelClient is the client side of an SSH WAN link, mirroring the C++
 * SSHProxyConnector: it listens for local connections and tunnels each one
 * to the remote SSH server, relaying the local byte stream over the
 * encrypted transport.
 */
export class SshTunnelClient {
  private readonly connections = new Set<Socket>();
  private readonly server: Server;

  public constructor(private readonly config: SshTunnelClientConfig) {
    this.server = createServer({ allowHalfOpen: true }, (socket) =>
      this.accept(socket),
    );
  }

  public get address(): { readonly address: string; readonly port: number } {
    const address = this.server.address();
    if (address === null || typeof address === "string") {
      throw new Error("SSH tunnel client is not listening on a TCP address");
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

  private accept(localSocket: Socket): void {
    this.track(localSocket);
    const wanSocket = connect({
      allowHalfOpen: true,
      host: this.config.sshServer.host,
      port: this.config.sshServer.port,
    });
    this.track(wanSocket);
    wanSocket.once("connect", () => {
      const transport = new SshTransport(wanSocket, { role: "client" });
      transport
        .start()
        .then(() => wireSocketToTransport(localSocket, transport))
        .catch(() => {
          localSocket.destroy();
          wanSocket.destroy();
        });
    });
    wanSocket.once("error", () => localSocket.destroy());
  }

  private track(socket: Socket): void {
    this.connections.add(socket);
    socket.once("close", () => this.connections.delete(socket));
  }
}

export interface SshTunnelServerConfig {
  readonly listen: NetworkEndpoint;
  readonly target: NetworkEndpoint;
  readonly hostKey: RsaHostKey;
}

export interface SshTunnelClientConfig {
  readonly listen: NetworkEndpoint;
  readonly sshServer: NetworkEndpoint;
}

/**
 * wireSocketToTransport relays bytes between a plain socket and an SSH
 * transport in both directions, propagating closure.
 */
function wireSocketToTransport(socket: Socket, transport: SshTransport): void {
  socket.on("data", (chunk: Buffer) => {
    try {
      transport.send(chunk);
    } catch {
      socket.destroy();
      transport.close();
    }
  });
  transport.onPayload((payload) => {
    if (!socket.destroyed) {
      socket.write(payload);
    }
  });
  socket.once("close", () => transport.close());
  transport.onClose(() => {
    if (!socket.destroyed) {
      socket.destroy();
    }
  });
}
