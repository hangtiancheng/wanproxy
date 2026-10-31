import { describe, expect, it } from "vitest";

import { CodecStats } from "@/pipeline/codec-stats.js";

describe("CodecStats", () => {
  it("starts at zero", () => {
    expect(new CodecStats().snapshot()).toEqual({
      codecToIncomingBytes: 0,
      codecToOutgoingBytes: 0,
      incomingToCodecBytes: 0,
      outgoingToCodecBytes: 0,
    });
  });

  it("accumulates each of the four boundaries independently", () => {
    const stats = new CodecStats();
    stats.addIncomingToCodec(10);
    stats.addIncomingToCodec(5);
    stats.addCodecToOutgoing(7);
    stats.addOutgoingToCodec(3);
    stats.addCodecToIncoming(100);
    stats.addCodecToIncoming(1);
    expect(stats.snapshot()).toEqual({
      codecToIncomingBytes: 101,
      codecToOutgoingBytes: 7,
      incomingToCodecBytes: 15,
      outgoingToCodecBytes: 3,
    });
  });
});
