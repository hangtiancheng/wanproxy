import {
  closeSync,
  mkdtempSync,
  openSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { TackPersistentCache } from "@/tack/persistent-cache.js";
import { processFile } from "@/tack/tack.js";
import { MemoryCache, XCODEC_SEGMENT_LENGTH } from "@/xcodec/index.js";

function makeWorkspace(): string {
  return mkdtempSync(join(tmpdir(), "wanproxy-tack-"));
}

function repetitive(bytes: number, fill = 0x5a): Uint8Array {
  const data = new Uint8Array(bytes).fill(fill);
  // Vary it a little so the rolling hash has real work to do.
  for (let i = 0; i < bytes; i += 4096) {
    data[i] = (i / 4096) & 0xff;
  }
  return data;
}

describe("tack", () => {
  let dir: string;
  beforeEach(() => {
    dir = makeWorkspace();
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  function run(
    action: "compress" | "decompress",
    inputPath: string,
    outputPath: string,
    cache: import("@/xcodec/cache-interface.js").XCodecCache,
  ): void {
    const ifd = openSync(inputPath, "r");
    const ofd = openSync(outputPath, "w");
    try {
      processFile(inputPath, ifd, ofd, { action, cache, stats: false });
    } finally {
      closeSync(ifd);
      closeSync(ofd);
    }
  }

  it("round-trips through an in-band memory cache", () => {
    const input = repetitive(XCODEC_SEGMENT_LENGTH * 16);
    const inPath = join(dir, "in");
    const compPath = join(dir, "comp");
    const outPath = join(dir, "out");
    writeFileSync(inPath, input);

    run("compress", inPath, compPath, new MemoryCache());
    run("decompress", compPath, outPath, new MemoryCache());

    expect(new Uint8Array(readFileSync(outPath))).toEqual(input);
    // Deduplication should shrink repetitive data.
    expect(readFileSync(compPath).length).toBeLessThan(input.length);
  });

  it("round-trips through an out-of-band persistent dictionary", () => {
    const input = repetitive(XCODEC_SEGMENT_LENGTH * 12, 0x33);
    const inPath = join(dir, "in");
    const compPath = join(dir, "comp");
    const outPath = join(dir, "out");
    const dictPath = join(dir, "dict");
    writeFileSync(inPath, input);

    const compressCache = TackPersistentCache.open(dictPath, true);
    run("compress", inPath, compPath, compressCache);
    compressCache.close();

    // The dictionary file now holds the segments; the compressed stream
    // is references only, so it must be far smaller than the input.
    expect(readFileSync(compPath).length).toBeLessThan(input.length / 2);

    const decompressCache = TackPersistentCache.open(dictPath, false);
    run("decompress", compPath, outPath, decompressCache);
    decompressCache.close();

    expect(new Uint8Array(readFileSync(outPath))).toEqual(input);
  });

  it("fails to decompress an out-of-band stream without the dictionary", () => {
    const input = repetitive(XCODEC_SEGMENT_LENGTH * 4, 0x77);
    const inPath = join(dir, "in");
    const compPath = join(dir, "comp");
    const outPath = join(dir, "out");
    const dictPath = join(dir, "dict");
    writeFileSync(inPath, input);

    const compressCache = TackPersistentCache.open(dictPath, true);
    run("compress", inPath, compPath, compressCache);
    compressCache.close();

    // A fresh empty memory cache cannot resolve the out-of-band references.
    expect(() =>
      run("decompress", compPath, outPath, new MemoryCache()),
    ).toThrow(/unknown hashes/);
  });

  it("emits big-endian segment hashes for -h", () => {
    const input = new Uint8Array(XCODEC_SEGMENT_LENGTH * 3).fill(0x41);
    const inPath = join(dir, "in");
    const outPath = join(dir, "hashes");
    writeFileSync(inPath, input);

    const ifd = openSync(inPath, "r");
    const ofd = openSync(outPath, "w");
    try {
      processFile(inPath, ifd, ofd, {
        action: "hashes",
        cache: new MemoryCache(),
        stats: false,
      });
    } finally {
      closeSync(ifd);
      closeSync(ofd);
    }

    const hashes = readFileSync(outPath);
    expect(hashes.length).toBe(3 * 8);
  });
});
