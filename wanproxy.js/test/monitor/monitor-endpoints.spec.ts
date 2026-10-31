import { afterEach, describe, expect, it } from "vitest";

import type { ConfigObject } from "@/config/model.js";
import { MonitorServer } from "@/monitor/monitor-server.js";
import type { FleetStatus } from "@/proxy/proxy-status.js";

const OBJECTS: readonly ConfigObject[] = [
  {
    class: "codec",
    fields: [{ name: "codec", type: "string", value: "XCodec" }],
    name: "codec0",
  },
];

const STATUS: FleetStatus = {
  generatedAt: new Date(0).toISOString(),
  proxies: [
    {
      acceptedConnections: 2,
      activeSockets: 0,
      codec: { mode: "xcodec", role: "incoming" },
      kind: "tcp",
      listen: { host: "127.0.0.1", port: 1 },
      listening: true,
      name: "proxy0",
      upstream: { host: "127.0.0.1", port: 2 },
    },
  ],
};

const servers: MonitorServer[] = [];

async function startServer(): Promise<number> {
  const server = new MonitorServer(
    { listen: { host: "127.0.0.1", port: 0 } },
    { objects: () => OBJECTS, status: () => STATUS },
  );
  servers.push(server);
  await server.start();
  return server.address.port;
}

afterEach(async () => {
  for (const server of servers.splice(0)) {
    await server.stop();
  }
});

describe("MonitorServer endpoints", () => {
  it("serves the HTML root with config and status", async () => {
    const port = await startServer();
    const response = await fetch(`http://127.0.0.1:${port}/`);
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("text/html");
    const body = await response.text();
    expect(body).toContain("<h1>Configuration</h1>");
    expect(body).toContain("codec0");
    expect(body).toContain("<h1>Status</h1>");
  });

  it("serves a per-object HTML view", async () => {
    const port = await startServer();
    const response = await fetch(`http://127.0.0.1:${port}/object/codec0`);
    expect(response.status).toBe(200);
    const body = await response.text();
    expect(body).toContain("Configuration - codec0");
  });

  it("serves the text export under /:text", async () => {
    const port = await startServer();
    const response = await fetch(`http://127.0.0.1:${port}/:text`);
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("text/plain");
    const body = await response.text();
    expect(body).toContain("config:\r\n");
    expect(body).toContain(" object: codec0; class: codec;\r\n");
    expect(body).toContain("status:\r\n");
  });

  it("serves a filtered text export under /:text/object/<name>", async () => {
    const port = await startServer();
    const response = await fetch(
      `http://127.0.0.1:${port}/:text/object/codec0`,
    );
    expect(response.status).toBe(200);
    const body = await response.text();
    expect(body).toContain("config codec0:\r\n");
    expect(body).not.toContain("status:\r\n");
  });

  it("still serves JSON at /status and 404s unknown paths", async () => {
    const port = await startServer();
    const status = await fetch(`http://127.0.0.1:${port}/status`);
    expect(status.status).toBe(200);
    expect(status.headers.get("content-type")).toContain("application/json");
    const parsed = (await status.json()) as FleetStatus;
    expect(parsed.proxies[0]?.name).toBe("proxy0");

    const missing = await fetch(`http://127.0.0.1:${port}/nope`);
    expect(missing.status).toBe(404);
  });
});
