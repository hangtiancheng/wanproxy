import { ByteQueue } from "./byte-queue.js";
import { XCODEC_MAGIC } from "./constants.js";
import { readRawPrefix } from "./decoder-state.js";
import { readFrame, type XCodecFrame } from "./frame-codec.js";
import { XCodecHash } from "./hash.js";
import { formatUint64Hex } from "./uint64.js";

export type DumpSink = (line: string) => void;

/**
 * XCodecStreamDumper decodes an XCodec wire stream incrementally and emits
 * one XML-style line per element, mirroring the C++ programs/xcdump and the
 * Go cmd/xcdump:
 *
 *   <data/>            raw escaped bytes between frames
 *   <escape />         an escaped magic byte
 *   <hash-declare/>    an in-band segment declaration
 *   <hash-reference/>  a reference to a cached segment
 *   <back-reference/>  a reference into the backref window
 *
 * Verbosity 1 (-v) adds hash/offset attributes; verbosity 2 (-vv)
 * additionally hexdumps the data.
 */
export class XCodecStreamDumper {
  private readonly queue = new ByteQueue();

  public constructor(
    private readonly verbosity: number,
    private readonly sink: DumpSink,
  ) {}

  /** write feeds one chunk of the stream, emitting any complete elements. */
  public write(chunk: Uint8Array): void {
    this.queue.append(chunk);
    this.drain();
  }

  /** end signals end-of-stream, flushing any trailing partial element. */
  public end(): void {
    this.drain();
    if (this.queue.length > 0) {
      // A trailing incomplete frame is reported as raw data, like the C++
      // dump which stops when it cannot parse further.
      this.emitData(this.queue.toUint8Array());
      this.queue.clear();
    }
  }

  private drain(): void {
    for (;;) {
      const raw = readRawPrefix(this.queue);
      if (raw !== undefined) {
        this.emitData(raw);
        continue;
      }
      // The queue now starts with the magic byte (or is empty); readFrame
      // returns need-more until a whole frame is present.
      const result = readFrame(this.queue);
      if (result.status === "need-more") {
        return;
      }
      if (result.status === "invalid") {
        this.sink(`dump error: ${result.reason}`);
        this.queue.clear();
        return;
      }
      this.emitFrame(result.frame);
    }
  }

  private emitData(data: Uint8Array): void {
    if (data.length === 0) {
      return;
    }
    if (this.verbosity > 1) {
      this.sink(`<data data="${toHex(data)}"/>`);
      return;
    }
    this.sink(`<data/>`);
  }

  private emitFrame(frame: XCodecFrame): void {
    switch (frame.kind) {
      case "escape":
        this.sink(`<escape />`);
        break;
      case "extract": {
        const hash = XCodecHash.hashSegment(frame.segment);
        let line = `<hash-declare`;
        if (this.verbosity > 0) {
          line += ` hash="0x${formatUint64Hex(hash)}"`;
          if (this.verbosity > 1) {
            line += ` data="${toHex(frame.segment)}"`;
          }
        }
        this.sink(`${line}/>`);
        break;
      }
      case "ref": {
        let line = `<hash-reference`;
        if (this.verbosity > 0) {
          line += ` hash="0x${formatUint64Hex(frame.hash)}"`;
        }
        this.sink(`${line}/>`);
        break;
      }
      case "backref": {
        let line = `<back-reference`;
        if (this.verbosity > 0) {
          line += ` offset="${frame.index}"`;
        }
        this.sink(`${line}/>`);
        break;
      }
    }
  }
}

/** magicByte exposes the XCodec frame magic for callers that scan raw input. */
export const XCODEC_STREAM_MAGIC = XCODEC_MAGIC;

function toHex(data: Uint8Array): string {
  return Buffer.from(data).toString("hex");
}
