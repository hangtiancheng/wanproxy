import { describe, expect, it } from "vitest";

import { parseFwdProxyConfig } from "@/fwdproxy/config.js";

describe("parseFwdProxyConfig", () => {
  it("parses the columnar C++ fwdproxy format", () => {
    const text = [
      "# Simple columnar format.",
      "",
      "0.0.0.0 2200 10.0.1.9 22",
      "  127.0.0.1  4433  127.0.0.1  443  ",
    ].join("\n");

    expect(parseFwdProxyConfig(text)).toEqual([
      {
        codec: { mode: "none", role: "incoming" },
        listen: { host: "0.0.0.0", port: 2200 },
        name: "configured-3",
        upstream: { host: "10.0.1.9", port: 22 },
      },
      {
        codec: { mode: "none", role: "incoming" },
        listen: { host: "127.0.0.1", port: 4433 },
        name: "configured-4",
        upstream: { host: "127.0.0.1", port: 443 },
      },
    ]);
  });

  it("skips comments and blank lines", () => {
    expect(parseFwdProxyConfig("# only a comment\n\n")).toEqual([]);
  });

  it("rejects a line with the wrong number of tokens", () => {
    expect(() => parseFwdProxyConfig("0.0.0.0 2200 10.0.1.9")).toThrow(
      /expected 4, got 3/,
    );
  });

  it("rejects symbolic ports", () => {
    expect(() =>
      parseFwdProxyConfig("0.0.0.0 4433 ipv6.google.com https"),
    ).toThrow(/must be numeric/);
  });

  it("rejects out-of-range ports", () => {
    expect(() => parseFwdProxyConfig("0.0.0.0 70000 10.0.0.1 22")).toThrow(
      /out of range/,
    );
  });
});
