import { describe, expect, it } from "vitest";

import { parseAddressFamily } from "@/config/model.js";

describe("parseAddressFamily", () => {
  it("maps the C++ family names and defaults the empty string to IP", () => {
    expect(parseAddressFamily("")).toBe("IP");
    expect(parseAddressFamily("IP")).toBe("IP");
    expect(parseAddressFamily("IPv4")).toBe("IPv4");
    expect(parseAddressFamily("IPv6")).toBe("IPv6");
    expect(parseAddressFamily("Unix")).toBe("Unix");
  });

  it("rejects an unknown family", () => {
    expect(() => parseAddressFamily("IPX")).toThrow(/unknown address family/);
  });
});
