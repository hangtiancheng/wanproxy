import { Transform, type TransformCallback } from "node:stream";
import {
  constants as zlibConstants,
  createDeflate,
  createInflate,
  type Deflate,
  type ZlibOptions,
} from "node:zlib";

export type ZlibTransformMode = "deflate" | "inflate";

export interface ZlibTransformOptions {
  readonly level?: number;
}

export function createZlibTransform(
  mode: ZlibTransformMode,
  options: ZlibTransformOptions = {},
): Transform {
  const zlibOptions: ZlibOptions =
    options.level === undefined ? {} : { level: options.level };
  switch (mode) {
    case "deflate":
      return new SyncFlushDeflate(zlibOptions);
    case "inflate":
      return createInflate(zlibOptions);
  }
}

/**
 * SyncFlushDeflate mirrors the C++ DeflatePipe, which ends every consume()
 * batch with Z_SYNC_FLUSH so that compressed bytes reach the wire
 * immediately instead of waiting for the deflate buffer to fill.  The Go
 * port does the same by calling Flush() after every wire write.
 *
 * Node's stock createDeflate() deflates with Z_NO_FLUSH, buffering small
 * writes until enough data accumulates or the stream finishes; on a proxy
 * that stalls a connection waiting for more application data before the
 * peer sees anything.  Each chunk is therefore written to the inner deflate
 * stream and then explicitly sync-flushed, which emits an empty stored
 * block that drains the bit buffer and byte-aligns the output while
 * preserving the compression dictionary (unlike Z_FULL_FLUSH).  The final
 * flush uses Z_FINISH via end(), matching the C++ encoder producing EOS
 * into the deflate pipe.
 */
class SyncFlushDeflate extends Transform {
  private readonly deflate: Deflate;

  public constructor(options: ZlibOptions) {
    super();
    this.deflate = createDeflate(options);
    this.deflate.on("data", (chunk: Buffer) => {
      this.push(chunk);
    });
    this.deflate.once("error", (error: Error) => {
      this.destroy(error);
    });
  }

  public override _transform(
    chunk: unknown,
    _encoding: BufferEncoding,
    callback: TransformCallback,
  ): void {
    this.deflate.write(toBytes(chunk));
    this.deflate.flush(zlibConstants.Z_SYNC_FLUSH, (error?: Error | null) => {
      callback(error ?? undefined);
    });
  }

  public override _flush(callback: TransformCallback): void {
    this.deflate.once("end", () => callback());
    this.deflate.once("error", (error: Error) => callback(error));
    this.deflate.end();
  }
}

function toBytes(chunk: unknown): Uint8Array {
  if (chunk instanceof Uint8Array) {
    return chunk;
  }
  if (typeof chunk === "string") {
    return Buffer.from(chunk);
  }
  throw new TypeError("deflate transform expects bytes");
}
