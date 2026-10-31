import { describe, expect, it } from "vitest";

import {
  concatByteArrays,
  encodeEscapedBytes,
  encodeFrame,
  XCODEC_SEGMENT_LENGTH,
  XCodecHash,
  formatUint64Hex,
} from "@/xcodec/index.js";
import { XCodecStreamDumper } from "@/xcodec/stream-dump.js";

function dump(input: Uint8Array, verbosity = 0): string[] {
  const lines: string[] = [];
  const dumper = new XCodecStreamDumper(verbosity, (line) => lines.push(line));
  dumper.write(input);
  dumper.end();
  return lines;
}

function segment(fill: number): Uint8Array {
  return new Uint8Array(XCODEC_SEGMENT_LENGTH).fill(fill);
}

describe("XCodecStreamDumper", () => {
  it("prints one XML-style line per stream element", () => {
    const seg = segment(0x41);
    const hash = XCodecHash.hashSegment(seg);
    const stream = concatByteArrays([
      encodeEscapedBytes(Uint8Array.from([1, 2, 3])),
      encodeFrame({ kind: "extract", segment: seg }),
      encodeFrame({ hash, kind: "ref" }),
      encodeFrame({ kind: "escape" }),
      encodeFrame({ index: 5, kind: "backref" }),
    ]);

    expect(dump(stream)).toEqual([
      "<data/>",
      "<hash-declare/>",
      "<hash-reference/>",
      "<escape />",
      "<back-reference/>",
    ]);
  });

  it("adds hash and offset attributes at verbosity 1", () => {
    const seg = segment(0x42);
    const hash = XCodecHash.hashSegment(seg);
    const stream = concatByteArrays([
      encodeFrame({ kind: "extract", segment: seg }),
      encodeFrame({ hash, kind: "ref" }),
      encodeFrame({ index: 9, kind: "backref" }),
    ]);

    expect(dump(stream, 1)).toEqual([
      `<hash-declare hash="0x${formatUint64Hex(hash)}"/>`,
      `<hash-reference hash="0x${formatUint64Hex(hash)}"/>`,
      `<back-reference offset="9"/>`,
    ]);
  });

  it("hexdumps data at verbosity 2", () => {
    const stream = encodeEscapedBytes(Uint8Array.from([0xde, 0xad]));
    expect(dump(stream, 2)).toEqual([`<data data="dead"/>`]);
  });

  it("dumps incrementally across chunk boundaries", () => {
    const seg = segment(0x43);
    const stream = concatByteArrays([
      encodeFrame({ kind: "extract", segment: seg }),
      encodeFrame({ hash: XCodecHash.hashSegment(seg), kind: "ref" }),
    ]);

    const lines: string[] = [];
    const dumper = new XCodecStreamDumper(0, (line) => lines.push(line));
    // Feed one byte at a time to exercise partial-frame buffering.
    for (const byte of stream) {
      dumper.write(Uint8Array.from([byte]));
    }
    dumper.end();

    expect(lines).toEqual(["<hash-declare/>", "<hash-reference/>"]);
  });

  it("reports a trailing partial frame as data at end of stream", () => {
    // A magic byte with no following opcode is an incomplete frame.
    const lines = dump(Uint8Array.from([0xf1]));
    expect(lines).toEqual(["<data/>"]);
  });
});
