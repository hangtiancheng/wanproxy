import { afterEach, describe, expect, it } from "vitest";

import {
  component,
  createLogger,
  formatLogLine,
  getLogLevel,
  parseLogLevel,
  restoreDefaultLogSink,
  setLogLevel,
  setLogSink,
} from "@/common/log.js";

function capture(): {
  readonly lines: string[];
  readonly sink: (l: string) => void;
} {
  const lines: string[] = [];
  return { lines, sink: (line) => lines.push(line) };
}

describe("parseLogLevel", () => {
  it("maps C++ level names onto levels", () => {
    expect(parseLogLevel("DEBUG")).toBe("debug");
    expect(parseLogLevel("info")).toBe("info");
    expect(parseLogLevel("NOTICE")).toBe("info");
    expect(parseLogLevel("WARNING")).toBe("warn");
    expect(parseLogLevel("err")).toBe("warn");
    expect(parseLogLevel("CRIT")).toBe("warn");
    expect(parseLogLevel("EMERG")).toBe("warn");
  });

  it("rejects unknown names", () => {
    expect(() => parseLogLevel("trace")).toThrow(/unknown log level/);
  });
});

describe("leveled logging", () => {
  afterEach(() => {
    setLogLevel("info");
    restoreDefaultLogSink();
  });

  it("filters records below the global level", () => {
    const { lines, sink } = capture();
    setLogSink(sink);
    setLogLevel("warn");
    const log = createLogger();
    log.debug("d");
    log.info("i");
    log.warn("w");
    log.error("e");
    expect(lines).toHaveLength(2);
    expect(lines[0]).toContain("level=WARN");
    expect(lines[1]).toContain("level=ERROR");
  });

  it("emits debug records at the debug level", () => {
    const { lines, sink } = capture();
    setLogSink(sink);
    setLogLevel("debug");
    createLogger().debug("verbose", { key: "value" });
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain("level=DEBUG");
    expect(lines[0]).toContain("key=value");
  });

  it("tags records with a component path", () => {
    const { lines, sink } = capture();
    setLogSink(sink);
    setLogLevel("info");
    component("/wanproxy/proxy/proxy0").info("hello");
    expect(lines[0]).toContain("component=/wanproxy/proxy/proxy0");
  });

  it("derives child loggers via withComponent", () => {
    const { lines, sink } = capture();
    setLogSink(sink);
    const child = createLogger().withComponent("/wanproxy");
    expect(child.component).toBe("/wanproxy");
    child.info("x");
    expect(lines[0]).toContain("component=/wanproxy");
  });

  it("tracks the current level", () => {
    setLogLevel("error");
    expect(getLogLevel()).toBe("error");
  });
});

describe("formatLogLine", () => {
  it("quotes values containing separators", () => {
    const line = formatLogLine("info", undefined, "a message", {
      path: "has space",
      n: 7,
    });
    expect(line).toBe('level=INFO msg="a message" path="has space" n=7');
  });

  it("renders Error field values as messages", () => {
    const line = formatLogLine("error", "c", "boom", {
      error: new Error("bad thing"),
    });
    expect(line).toContain('error="bad thing"');
  });
});
