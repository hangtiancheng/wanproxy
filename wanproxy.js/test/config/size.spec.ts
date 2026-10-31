import { describe, expect, it } from "vitest";

import { parseSize } from "@/config/size.js";

describe("parseSize", () => {
  it("parses the C++ ConfigTypeSize vectors", () => {
    expect(parseSize("0")).toBe(0);
    expect(parseSize("512")).toBe(512);
    expect(parseSize("1kb")).toBe(1024);
    expect(parseSize("128MB")).toBe(128 * 1024 * 1024);
    expect(parseSize("1GB")).toBe(1024 * 1024 * 1024);
    expect(parseSize("2tb")).toBe(2 * 1024 * 1024 * 1024 * 1024);
    expect(parseSize("0x10")).toBe(16);
  });

  it("accepts single-letter suffixes and a bare b", () => {
    expect(parseSize("4k")).toBe(4096);
    expect(parseSize("3m")).toBe(3 * 1024 * 1024);
    expect(parseSize("2g")).toBe(2 * 1024 * 1024 * 1024);
    expect(parseSize("64b")).toBe(64);
  });

  it("parses octal and binary with base 0", () => {
    expect(parseSize("010")).toBe(8);
    expect(parseSize("0b101")).toBe(5);
    expect(parseSize("0o17")).toBe(15);
  });

  it("trims surrounding whitespace", () => {
    expect(parseSize("  128MB  ")).toBe(128 * 1024 * 1024);
  });

  it("rejects invalid sizes", () => {
    expect(() => parseSize("12qb")).toThrow(/invalid size/);
    expect(() => parseSize("")).toThrow(/empty size/);
    expect(() => parseSize("abc")).toThrow(/invalid size/);
  });
});
