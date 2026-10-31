#!/usr/bin/env node
/**
 * ssh-echo is the in-process SSH echo endpoint that the SSH-SSH proxy chain
 * terminates on, mirroring `wanbench ssh-echo` in the Go stack.  It accepts
 * SSH connections (untagged framing) and echoes every decrypted application
 * byte back to the sender.
 *
 * Usage: node dist/cli/ssh-echo.js --listen=127.0.0.1:8349 --hostkey=<path>
 */

import { readFileSync } from "node:fs";
import { createServer, type Server, type Socket } from "node:net";
import { stderr, stdout } from "node:process";

import { RsaHostKey, SshTransport } from "../ssh/index.js";

export interface SshEchoOptions {
  readonly listen: string;
  readonly hostKeyPath: string;
}

export interface SshEchoServer {
  readonly address: { readonly host: string; readonly port: number };
  readonly stop: () => Promise<void>;
}

export function parseSshEchoArgs(argv: readonly string[]): SshEchoOptions {
  let listen = "127.0.0.1:8349";
  let hostKeyPath = "";
  for (const arg of argv) {
    if (arg.startsWith("--listen=")) {
      listen = arg.slice("--listen=".length);
    } else if (arg.startsWith("--hostkey=")) {
      hostKeyPath = arg.slice("--hostkey=".length);
    }
  }
  if (hostKeyPath === "") {
    throw new Error("ssh-echo: --hostkey=<path> is required");
  }
  return { hostKeyPath, listen };
}

export async function startSshEcho(
  options: SshEchoOptions,
): Promise<SshEchoServer> {
  const hostKey = RsaHostKey.fromPrivatePem(readFileSync(options.hostKeyPath));
  const [host, portStr] = splitListen(options.listen);
  const port = Number.parseInt(portStr, 10);
  const connections = new Set<Socket>();

  const server: Server = createServer({ allowHalfOpen: true }, (socket) => {
    connections.add(socket);
    socket.once("close", () => connections.delete(socket));
    const transport = new SshTransport(socket, { hostKey, role: "server" });
    transport
      .start()
      .then(() => {
        transport.onAppData((data) => transport.writeApp(data));
      })
      .catch(() => socket.destroy());
  });

  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => resolve());
  });

  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("ssh-echo: did not bind to a TCP address");
  }

  return {
    address: { host: address.address, port: address.port },
    stop: () =>
      new Promise<void>((resolve) => {
        for (const socket of connections) {
          socket.destroy();
        }
        server.close(() => resolve());
      }),
  };
}

function splitListen(listen: string): [string, string] {
  const idx = listen.lastIndexOf(":");
  if (idx < 0) {
    return ["127.0.0.1", listen];
  }
  return [listen.slice(0, idx), listen.slice(idx + 1)];
}

async function main(): Promise<void> {
  const options = parseSshEchoArgs(process.argv.slice(2));
  const echo = await startSshEcho(options);
  stdout.write(
    `ssh-echo listening on ${echo.address.host}:${echo.address.port}\n`,
  );
  const stop = async (): Promise<void> => {
    await echo.stop();
    process.exit(0);
  };
  process.once("SIGINT", () => void stop());
  process.once("SIGTERM", () => void stop());
}

// Run as a script only when executed directly (not when imported for tests).
if (
  process.argv[1] !== undefined &&
  /ssh-echo(\.js|\.cjs)?$/.test(process.argv[1])
) {
  main().catch((error: unknown) => {
    stderr.write(
      `ssh-echo: ${error instanceof Error ? error.message : String(error)}\n`,
    );
    process.exit(1);
  });
}
