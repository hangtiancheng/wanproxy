import type { Writable } from "node:stream";

import { setLogLevel } from "../common/log.js";
import { loadConfig } from "../config/load.js";
import { RuntimeFleet } from "../proxy/runtime-fleet.js";
import { parseProxyCliArgs } from "./proxy-cli-args.js";

export async function startConfiguredProxy(
  argv: readonly string[] = process.argv,
  output: Writable = process.stdout,
): Promise<RuntimeFleet> {
  const args = parseProxyCliArgs(argv);
  const config = await loadConfig(args.configPath);
  setLogLevel(config.logLevel);
  const fleet = new RuntimeFleet(config);
  await fleet.start();

  for (const address of fleet.addresses()) {
    output.write(`proxy listening on ${address.address}:${address.port}\n`);
  }
  const monitorAddress = fleet.monitorAddress();
  if (monitorAddress !== undefined) {
    output.write(
      `monitor listening on ${monitorAddress.address}:${monitorAddress.port}\n`,
    );
  }

  return fleet;
}

export function formatProxyCliError(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return "unknown proxy CLI error";
}
