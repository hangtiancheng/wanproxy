import type { NetworkEndpoint } from "../config/network-endpoint.js";
import type { TcpProxyConfig } from "../config/proxy-config.js";

/**
 * parseFwdProxyConfig parses the C++ fwdproxy columnar configuration: each
 * non-empty, non-comment line holds four whitespace-separated words
 *
 *   interface-host interface-port remote-host remote-port
 *
 * and produces one plain (codec "none") TCP proxy forwarding the interface
 * endpoint to the remote endpoint, mirroring FWDProxyConfig::parse.
 */
export function parseFwdProxyConfig(text: string): TcpProxyConfig[] {
  const proxies: TcpProxyConfig[] = [];
  const lines = text.split("\n");

  for (let index = 0; index < lines.length; index += 1) {
    const line = (lines[index] ?? "").trim();
    if (line === "" || line.startsWith("#")) {
      continue;
    }
    const tokens = line.split(/\s+/);
    if (tokens.length !== 4) {
      throw new Error(
        `wrong number of items in configuration entry on line ${index + 1}: expected 4, got ${tokens.length}`,
      );
    }
    const [interfaceHost, interfacePort, remoteHost, remotePort] = tokens as [
      string,
      string,
      string,
      string,
    ];
    proxies.push({
      codec: { mode: "none", role: "incoming" },
      listen: endpoint(interfaceHost, interfacePort, index + 1, "interface"),
      name: `configured-${index + 1}`,
      upstream: endpoint(remoteHost, remotePort, index + 1, "remote"),
    });
  }

  return proxies;
}

function endpoint(
  host: string,
  port: string,
  line: number,
  side: string,
): NetworkEndpoint {
  return { host, port: parsePort(port, line, side) };
}

function parsePort(port: string, line: number, side: string): number {
  if (!/^\d+$/.test(port)) {
    throw new Error(
      `${side} port on line ${line} must be numeric (symbolic service names are not supported): ${port}`,
    );
  }
  const value = Number.parseInt(port, 10);
  if (value < 0 || value > 65535) {
    throw new Error(`${side} port on line ${line} is out of range: ${port}`);
  }
  return value;
}
