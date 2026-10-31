import { describe, expect, it } from "vitest";

import type { ConfigObject } from "@/config/model.js";
import {
  escapeHtml,
  exportHtml,
  exportText,
  wrapHtml,
} from "@/monitor/export.js";
import type { FleetStatus } from "@/proxy/proxy-status.js";

const OBJECTS: readonly ConfigObject[] = [
  {
    class: "codec",
    fields: [
      { name: "codec", type: "string", value: "XCodec" },
      { name: "cache", type: "string", value: "cache0" },
    ],
    name: "codec0",
  },
  {
    class: "cache",
    fields: [{ name: "type", type: "string", value: "Pair" }],
    name: "cache0",
  },
];

const STATUS: FleetStatus = {
  generatedAt: new Date(0).toISOString(),
  proxies: [
    {
      acceptedConnections: 3,
      activeSockets: 1,
      codec: { mode: "zlib-xcodec", role: "incoming" },
      kind: "tcp",
      listen: { host: "127.0.0.1", port: 3300 },
      listening: true,
      name: "proxy0",
      upstream: { host: "127.0.0.1", port: 3301 },
    },
    {
      acceptedConnections: 0,
      activeSockets: 0,
      kind: "socks",
      listen: { host: "127.0.0.1", port: 1080 },
      listening: true,
      name: "socks0",
    },
  ],
};

describe("escapeHtml", () => {
  it("escapes the significant characters", () => {
    expect(escapeHtml('<a href="x">&\'')).toBe(
      "&lt;a href=&quot;x&quot;&gt;&amp;&#39;",
    );
  });
});

describe("exportHtml", () => {
  it("renders the full configuration and status at the root", () => {
    const html = exportHtml(OBJECTS, STATUS, "");
    expect(html).toContain("<h1>Configuration</h1>");
    expect(html).toContain('<a href="/object/codec0">codec0</a>');
    // The pointer field "cache" renders as a link to cache0.
    expect(html).toContain('<a href="/object/cache0">cache0</a>');
    expect(html).toContain("<h1>Status</h1>");
    expect(html).toContain("<td>proxy0</td>");
    expect(html).toContain("<td>socks0</td>");
    expect(html).toContain("<td>zlib-xcodec</td>");
  });

  it("filters to one object and omits the status table", () => {
    const html = exportHtml(OBJECTS, STATUS, "codec0");
    expect(html).toContain("Configuration - codec0");
    expect(html).toContain('href="/"');
    expect(html).toContain("codec0");
    // The excluded cache0 object's own row (with its "Pair" value) is gone,
    // though codec0's pointer field still links to it.
    expect(html).not.toContain("<td>Pair</td>");
    expect(html).not.toContain("<h1>Status</h1>");
  });
});

describe("exportText", () => {
  it("renders the config and status blocks at the root", () => {
    const text = exportText(OBJECTS, STATUS, "");
    expect(text.startsWith("config:\r\n")).toBe(true);
    expect(text).toContain(" object: codec0; class: codec;\r\n");
    expect(text).toContain("  field: codec; type: string;\r\n");
    expect(text).toContain("   value: XCodec;\r\n");
    expect(text).toContain("status:\r\n");
    expect(text).toContain("proxy0; tcp; true; 3; 1; zlib-xcodec\r\n");
  });

  it("filters to one object and omits the status block", () => {
    const text = exportText(OBJECTS, STATUS, "cache0");
    expect(text.startsWith("config cache0:\r\n")).toBe(true);
    expect(text).toContain(" object: cache0; class: cache;\r\n");
    expect(text).not.toContain("codec0");
    expect(text).not.toContain("status:\r\n");
  });
});

describe("wrapHtml", () => {
  it("wraps the body in the monitor document shell", () => {
    const doc = wrapHtml("<h1>x</h1>");
    expect(doc.startsWith("<html><head><title>WANProxy Monitor</title>")).toBe(
      true,
    );
    expect(doc.endsWith("</body></html>")).toBe(true);
    expect(doc).toContain("<h1>x</h1>");
  });
});
