import { Readable } from "node:stream";
import { createInflate } from "node:zlib";
import { describe, expect, it } from "vitest";
import { createZlibTransform } from "@/pipeline/zlib-transform.js";
import { concatByteArrays } from "@/xcodec/index.js";

async function collect(stream: NodeJS.ReadableStream): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  for await (const chunk of stream) {
    if (!(chunk instanceof Uint8Array)) {
      throw new TypeError("expected bytes");
    }
    chunks.push(chunk);
  }
  return concatByteArrays(chunks);
}

describe("zlib sync-flush deflate", () => {
  it("emits compressed bytes after every small write (Z_SYNC_FLUSH)", async () => {
    const deflate = createZlibTransform("deflate", { level: 6 });
    const emissions: number[] = [];
    deflate.on("data", (chunk: Uint8Array) => emissions.push(chunk.length));

    const chunks = [
      Buffer.from("hello, "),
      Buffer.from("wanproxy "),
      Buffer.from("sync flush"),
    ];
    const source = Readable.from(chunks);
    source.pipe(deflate);
    const compressed = await collect(deflate);

    // Stock createDeflate buffers all three chunks and emits them in a
    // single data event at finish (plus the 2-byte zlib header), i.e. two
    // emissions.  The sync-flush transform emits after every chunk, so we
    // see at least one emission per input chunk, proving data reaches the
    // wire incrementally instead of being held until the stream ends.
    expect(emissions.length).toBeGreaterThanOrEqual(chunks.length);
    expect(compressed.length).toBeGreaterThan(0);
  });

  it("round-trips small incremental chunks through inflate", async () => {
    const deflate = createZlibTransform("deflate", { level: 6 });
    const input = [
      Buffer.from("The quick brown fox "),
      Buffer.from("jumps over the lazy dog. "),
      Buffer.from("The quick brown fox jumps again."),
    ];
    Readable.from(input).pipe(deflate);
    const compressed = await collect(deflate);

    const inflated = await collect(
      Readable.from([Buffer.from(compressed)]).pipe(createInflate()),
    );
    expect(Buffer.from(inflated).toString()).toBe(
      "The quick brown fox jumps over the lazy dog. The quick brown fox jumps again.",
    );
  });

  it("produces a stream the stock zlib inflate can decode", async () => {
    const deflate = createZlibTransform("deflate");
    const payload = new Uint8Array(100_000).fill(0x41);
    // Feed in uneven small pieces to exercise repeated sync flushes.
    const pieces: Uint8Array[] = [];
    for (let offset = 0; offset < payload.length; offset += 4099) {
      pieces.push(payload.subarray(offset, offset + 4099));
    }
    Readable.from(pieces).pipe(deflate);
    const compressed = await collect(deflate);

    const inflated = await collect(
      Readable.from([Buffer.from(compressed)]).pipe(createInflate()),
    );
    expect(inflated).toEqual(payload);
    expect(compressed.length).toBeLessThan(payload.length);
  });
});
