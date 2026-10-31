import {
  createServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";

import type { MonitorConfig } from "../config/monitor-config.js";
import type { ConfigObject } from "../config/model.js";
import type { BoundAddress, FleetStatus } from "../proxy/proxy-status.js";
import { closeServer } from "../proxy/socket-utils.js";
import { exportHtml, exportText, wrapHtml } from "./export.js";
import type { MonitorStatusProvider } from "./monitor-status.js";

/** MonitorSource provides configuration objects and live fleet status. */
export interface MonitorSource {
  objects(): readonly ConfigObject[];
  status(): FleetStatus;
}

/**
 * MonitorSourceInput accepts either a bare status provider (the original
 * single-endpoint form) or a full source that also exports config objects.
 */
export type MonitorSourceInput = MonitorStatusProvider | MonitorSource;

export class MonitorServer {
  private readonly server: Server;
  private readonly source: MonitorSource;

  public constructor(
    private readonly config: MonitorConfig,
    source: MonitorSourceInput,
  ) {
    this.source =
      typeof source === "function"
        ? { objects: () => [], status: source }
        : source;
    this.server = createServer((request, response) => {
      this.handle(request, response);
    });
  }

  private handle(request: IncomingMessage, response: ServerResponse): void {
    if (request.method !== "GET") {
      this.notFound(response);
      return;
    }
    const path = (request.url ?? "/").split("?")[0] ?? "/";
    if (path === "/status") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify(this.source.status()));
      return;
    }

    // The C++ monitor serves a text rendering under the /:text prefix.
    let text = false;
    let trimmed = path.replace(/^\//, "");
    if (trimmed === ":text" || trimmed.startsWith(":text/")) {
      text = true;
      trimmed = trimmed.replace(/^:text/, "").replace(/^\//, "");
    }

    let selection = "";
    if (trimmed !== "") {
      const components = trimmed.split("/");
      if (components.length === 2 && components[0] === "object") {
        selection = components[1] ?? "";
      } else {
        this.notFound(response);
        return;
      }
    }

    const objects = this.source.objects();
    const status = this.source.status();
    if (text) {
      response.writeHead(200, {
        "content-type": "text/plain; charset=utf-8",
      });
      response.end(exportText(objects, status, selection));
      return;
    }
    response.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    response.end(wrapHtml(exportHtml(objects, status, selection)));
  }

  private notFound(response: ServerResponse): void {
    response.writeHead(404, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: "not found" }));
  }

  public get address(): BoundAddress {
    const address = this.server.address();
    if (address === null || typeof address === "string") {
      throw new Error("monitor server is not listening on a TCP address");
    }
    return { address: address.address, port: address.port };
  }

  public async start(): Promise<void> {
    await new Promise<void>((resolve) => {
      this.server.listen(
        this.config.listen.port,
        this.config.listen.host,
        resolve,
      );
    });
  }

  public async stop(): Promise<void> {
    await closeServer(this.server);
  }
}
