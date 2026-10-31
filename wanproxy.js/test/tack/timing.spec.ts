import {
  closeSync,
  mkdtempSync,
  openSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  parseTackArgs,
  TackUsageError,
  validateTackOptions,
} from "@/tack/options.js";
import { processFile, TackTimer, timeSamples, timeStats } from "@/tack/tack.js";
import { MemoryCache, XCODEC_SEGMENT_LENGTH } from "@/xcodec/index.js";

function repetitive(bytes: number): Uint8Array {
  const data = new Uint8Array(bytes).fill(0x5a);
  for (let i = 0; i < bytes; i += 4096) {
    data[i] = (i / 4096) & 0xff;
  }
  return data;
}

describe("TackTimer", () => {
  it("records a sample per begin/stop pair", () => {
    const timer = new TackTimer();
    timer.begin();
    timer.stop();
    timer.begin();
    timer.stop();
    expect(timer.samples).toHaveLength(2);
    for (const sample of timer.samples) {
      expect(sample).toBeGreaterThanOrEqual(0);
    }
  });

  it("ignores a stop without a matching begin", () => {
    const timer = new TackTimer();
    timer.stop();
    expect(timer.samples).toHaveLength(0);
  });
});

describe("timeStats / timeSamples", () => {
  it("prints the aggregate statistics lines", () => {
    const timer = new TackTimer();
    timer.begin();
    timer.stop();
    const lines: string[] = [];
    timeStats("<total>", timer, (m) => lines.push(m));
    expect(lines[0]).toBe("<total>: 1 timer samples.");
    expect(lines[1]).toMatch(/^<total>: \d+ total runtime\.$/);
    expect(lines[2]).toMatch(/^<total>: \d+ mean microseconds\/call\.$/);
  });

  it("prints bare samples for the empty name and prefixed otherwise", () => {
    const timer = new TackTimer();
    timer.begin();
    timer.stop();
    timer.begin();
    timer.stop();
    const bare: string[] = [];
    timeSamples("", timer, (m) => bare.push(m));
    expect(bare).toHaveLength(2);
    expect(bare[0]).toMatch(/^\d+$/);
    const named: string[] = [];
    timeSamples("file.bin", timer, (m) => named.push(m));
    expect(named[0]).toMatch(/^file\.bin,\d+$/);
  });
});

describe("tack timing flags", () => {
  it("parses -T, -E and -S", () => {
    const { options } = parseTackArgs(["-TES", "-c", "f"]);
    expect(options.codecTiming).toBe(true);
    expect(options.timingEach).toBe(true);
    expect(options.timingSamples).toBe(true);
  });

  it("rejects -E or -S without -T", () => {
    expect(() =>
      validateTackOptions(parseTackArgs(["-E", "-c", "f"]).options),
    ).toThrow(TackUsageError);
    expect(() =>
      validateTackOptions(parseTackArgs(["-S", "-c", "f"]).options),
    ).toThrow(TackUsageError);
    expect(() =>
      validateTackOptions(parseTackArgs(["-T", "-c", "f"]).options),
    ).not.toThrow();
  });

  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "wanproxy-tack-timing-"));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("emits per-file stats when -T -E are set", () => {
    const input = repetitive(XCODEC_SEGMENT_LENGTH * 8);
    const inPath = join(dir, "in");
    const outPath = join(dir, "out");
    writeFileSync(inPath, input);
    const ifd = openSync(inPath, "r");
    const ofd = openSync(outPath, "w");
    const lines: string[] = [];
    try {
      processFile(inPath, ifd, ofd, {
        action: "compress",
        cache: new MemoryCache(),
        codecTiming: true,
        log: (m) => lines.push(m),
        stats: false,
        timingEach: true,
      });
    } finally {
      closeSync(ifd);
      closeSync(ofd);
    }
    expect(lines.some((l) => l.startsWith(`${inPath}: `))).toBe(true);
    expect(lines.some((l) => l.includes("timer samples."))).toBe(true);
  });
});
