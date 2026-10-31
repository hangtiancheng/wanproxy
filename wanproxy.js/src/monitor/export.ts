/**
 * Configuration and status exporters for the monitor HTTP interface,
 * mirroring the C++ MonitorClient and the Go monitor.exportHTML/exportText:
 * an HTML rendering at /, per-object views at /object/<name>, and plain
 * text under the /:text prefix.
 */

import type { ConfigObject } from "../config/model.js";
import type { FleetStatus, ProxyStatus } from "../proxy/proxy-status.js";

/** The member names the C++ monitor renders as object links. */
const POINTER_FIELDS = new Set([
  "primary",
  "secondary",
  "cache",
  "interface",
  "interface_codec",
  "peer",
  "peer_codec",
]);

/** escapeHtml escapes the five significant HTML characters. */
export function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

/**
 * exportHtml renders the configuration table (optionally filtered to one
 * object) plus, for the unfiltered view, the status table.
 */
export function exportHtml(
  objects: readonly ConfigObject[],
  status: FleetStatus,
  selection: string,
): string {
  const parts: string[] = [];
  if (selection === "") {
    parts.push("<h1>Configuration</h1>");
  } else {
    parts.push(
      `<h1>Configuration - ${escapeHtml(selection)} [<a href="/">up</a>]</h1>`,
    );
  }
  parts.push("<table>");
  parts.push(
    "<tr><th>object</th><th>class</th><th>field</th><th>type</th><th>value</th></tr>",
  );
  for (const object of objects) {
    if (selection !== "" && object.name !== selection) {
      continue;
    }
    parts.push(
      `<tr><td><a href="/object/${escapeHtml(object.name)}">${escapeHtml(object.name)}</a></td>` +
        `<td>${escapeHtml(object.class)}</td><td colspan="3" /></tr>`,
    );
    for (const field of object.fields) {
      parts.push(
        `<tr><td colspan="2" /><td>${escapeHtml(field.name)}</td>` +
          `<td>${escapeHtml(field.type)}</td><td>${htmlValue(field.name, field.value)}</td></tr>`,
      );
    }
  }
  parts.push("</table>");
  if (selection === "") {
    parts.push("<h1>Status</h1>");
    parts.push(statusTableHtml(status));
  }
  return parts.join("");
}

/**
 * exportText renders the C++ text export: a config block (optionally
 * filtered to one object) plus, for the unfiltered view, a status block.
 */
export function exportText(
  objects: readonly ConfigObject[],
  status: FleetStatus,
  selection: string,
): string {
  const parts: string[] = [];
  parts.push(selection === "" ? "config:\r\n" : `config ${selection}:\r\n`);
  for (const object of objects) {
    if (selection !== "" && object.name !== selection) {
      continue;
    }
    parts.push(` object: ${object.name}; class: ${object.class};\r\n`);
    for (const field of object.fields) {
      parts.push(`  field: ${field.name}; type: ${field.type};\r\n`);
      parts.push(`   value: ${field.value};\r\n`);
    }
  }
  if (selection === "") {
    parts.push("status:\r\n");
    for (const row of statusEntries(status)) {
      parts.push(` ${row.join("; ")}\r\n`);
    }
  }
  return parts.join("");
}

/** wrapHtml wraps an exported body in the monitor's HTML document shell. */
export function wrapHtml(body: string): string {
  return (
    "<html><head><title>WANProxy Monitor</title>" +
    '<style type="text/css">body { font-family: sans-serif; } ' +
    "td, th { vertical-align: text-top; text-align: left; }</style></head><body>" +
    body +
    "</body></html>"
  );
}

function htmlValue(fieldName: string, value: string): string {
  if (POINTER_FIELDS.has(fieldName) && value !== "" && value !== "None") {
    return `<a href="/object/${escapeHtml(value)}">${escapeHtml(value)}</a>`;
  }
  return `<tt>${escapeHtml(value)}</tt>`;
}

function statusTableHtml(status: FleetStatus): string {
  const parts: string[] = [];
  parts.push(
    "<table><tr><th>proxy</th><th>kind</th><th>listening</th><th>accepted</th><th>active</th><th>codec</th></tr>",
  );
  for (const row of statusEntries(status)) {
    parts.push(`<tr>${row.map((cell) => `<td>${cell}</td>`).join("")}</tr>`);
  }
  parts.push("</table>");
  return parts.join("");
}

function statusEntries(status: FleetStatus): string[][] {
  return status.proxies.map((proxy) => proxyRow(proxy));
}

function proxyRow(proxy: ProxyStatus): string[] {
  const codec = proxy.kind === "tcp" ? escapeHtml(proxy.codec.mode) : "";
  return [
    escapeHtml(proxy.name),
    proxy.kind,
    String(proxy.listening),
    String(proxy.acceptedConnections),
    String(proxy.activeSockets),
    codec,
  ];
}
