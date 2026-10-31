#!/usr/bin/env node
import { readFileSync } from "node:fs";
import { argv, exit, stderr, stdout } from "node:process";

import type { WanproxyConfig } from "../config/proxy-config.js";
import { parseFwdProxyConfig } from "../fwdproxy/config.js";
import { ProxyFleet } from "../proxy/proxy-fleet.js";

const USAGE = "usage: fwdproxy [-q | -v] -c configfile\n";

interface FwdArgs {
  readonly configFile: string;
  readonly quiet: boolean;
  readonly verbose: boolean;
}

/**
 * fwdproxy is the simple file-driven port forwarder, mirroring the C++
 * programs/fwdproxy: `fwdproxy [-q | -v] -c configfile`.  Each config line
 * creates one plain TCP proxy (no codec) from the interface endpoint to the
 * remote endpoint.
 */
function parseArgs(args: readonly string[]): FwdArgs {
  let configFile = "";
  let quiet = false;
  let verbose = false;

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i] ?? "";
    switch (arg) {
      case "-c": {
        const next = args[i + 1];
        if (next === undefined) {
          stderr.write(USAGE);
          exit(1);
        }
        configFile = next;
        i += 1;
        break;
      }
      case "-q":
        quiet = true;
        break;
      case "-v":
        verbose = true;
        break;
      case "-?":
      case "-h":
      case "--help":
        stderr.write(USAGE);
        exit(arg === "-?" ? 1 : 0);
        break;
      default:
        stderr.write(USAGE);
        exit(1);
    }
  }
  return { configFile, quiet, verbose };
}

async function main(): Promise<void> {
  const { configFile, quiet, verbose } = parseArgs(argv.slice(2));
  if (configFile === "") {
    stderr.write(USAGE);
    exit(1);
    return;
  }
  if (quiet && verbose) {
    stderr.write("fwdproxy: -q and -v are mutually exclusive\n");
    exit(1);
    return;
  }

  let text: string;
  try {
    text = readFileSync(configFile, "utf8");
  } catch (error) {
    stderr.write(`fwdproxy: could not open file: ${errorMessage(error)}\n`);
    exit(1);
    return;
  }

  let proxies;
  try {
    proxies = parseFwdProxyConfig(text);
  } catch (error) {
    stderr.write(`fwdproxy: ${errorMessage(error)}\n`);
    exit(1);
    return;
  }
  if (proxies.length === 0) {
    stderr.write("fwdproxy: no proxies configured\n");
    exit(1);
    return;
  }

  const config: WanproxyConfig = { proxies };
  const fleet = new ProxyFleet(config);
  await fleet.start();

  if (!quiet) {
    for (const address of fleet.addresses()) {
      stdout.write(`proxy listening on ${address.address}:${address.port}\n`);
    }
  }

  const stop = async (): Promise<void> => {
    await fleet.stop();
    exit(0);
  };
  process.once("SIGINT", () => {
    void stop();
  });
  process.once("SIGTERM", () => {
    void stop();
  });
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

main().catch((error: unknown) => {
  stderr.write(`fwdproxy: ${errorMessage(error)}\n`);
  exit(1);
});
