import { describe, expect, it } from "vitest";
import {
  CodecSession,
  encodePipeFrame,
  type PipeControlFrame,
  readPipeFrame,
} from "@/pipe-protocol/index.js";
import {
  ByteQueue,
  MemoryCache,
  XCODEC_SEGMENT_LENGTH,
  XCodecHash,
} from "@/xcodec/index.js";

function segment(byte: number): Uint8Array {
  return new Uint8Array(XCODEC_SEGMENT_LENGTH).fill(byte);
}

function sessionId(byte: number): Uint8Array {
  return new Uint8Array(16).fill(byte);
}

function collectFrames(bytes: Uint8Array): PipeControlFrame[] {
  const queue = new ByteQueue();
  queue.append(bytes);
  const frames: PipeControlFrame[] = [];
  while (queue.length > 0) {
    const result = readPipeFrame(queue);
    if (result.status !== "frame") {
      throw new Error("expected complete pipe frame");
    }
    frames.push(result.frame);
  }
  return frames;
}

describe("CodecSession", () => {
  it("exchanges encoded data and advances processed frames", () => {
    const sender = new CodecSession({ id: sessionId(1) });
    const receiver = new CodecSession({ id: sessionId(2) });
    const input = new Uint8Array(XCODEC_SEGMENT_LENGTH * 3).fill(7);

    const received = receiver.receive(sender.encodeData(input));
    expect(received.status).toBe("ok");
    expect(received.output).toEqual(input);

    const advanced = sender.receive(received.responses);
    expect(advanced.status).toBe("ok");
    expect(advanced.output).toEqual(new Uint8Array());
  });

  it("recovers missing references with ASK and LEARN", () => {
    const sender = new CodecSession({ id: sessionId(3) });
    const receiver = new CodecSession({ id: sessionId(4) });
    sender.encodeData(segment(9));
    const frames = collectFrames(sender.encodeData(segment(9)));
    const refFrame = frames.find((frame) => frame.kind === "frame");
    if (refFrame === undefined) {
      throw new Error("expected frame");
    }

    const ask = receiver.receive(
      new Uint8Array([
        ...encodePipeFrame({ id: sessionId(3), kind: "hello" }),
        ...encodePipeFrame(refFrame),
      ]),
    );
    expect(ask.status).toBe("ok");
    expect(ask.output).toEqual(new Uint8Array());
    expect(collectFrames(ask.responses)[0]?.kind).toBe("ask");

    const learn = sender.receive(ask.responses);
    expect(learn.status).toBe("ok");
    expect(collectFrames(learn.responses)[0]?.kind).toBe("learn");

    const recovered = receiver.receive(learn.responses);
    expect(recovered.status).toBe("ok");
    expect(recovered.output).toEqual(segment(9));
    expect(collectFrames(recovered.responses)[0]?.kind).toBe("advance");
  });

  it("rejects invalid protocol ordering and gratuitous LEARN", () => {
    const session = new CodecSession({ id: sessionId(5) });
    const ordered = session.receive(
      encodePipeFrame({ count: 1, kind: "advance" }),
    );
    expect(ordered).toMatchObject({
      reason: "got ADVANCE before sending HELLO",
      status: "invalid",
    });

    const segmentData = segment(1);
    const gratuitous = new CodecSession({ id: sessionId(6) });
    const learned = gratuitous.receive(
      new Uint8Array([
        ...encodePipeFrame({ id: sessionId(7), kind: "hello" }),
        ...encodePipeFrame({ kind: "learn", segments: [segmentData] }),
      ]),
    );
    expect(learned).toMatchObject({
      reason: "gratuitous LEARN without ASK",
      status: "invalid",
    });
  });

  it("releases retained references after ADVANCE", () => {
    const sender = new CodecSession({ id: sessionId(8) });
    sender.encodeData(segment(2));
    sender.encodeData(segment(2));
    expect(
      sender.receive(encodePipeFrame({ count: 2, kind: "advance" })).status,
    ).toBe("ok");

    const hash = XCodecHash.hashSegment(segment(2));
    const asked = sender.receive(
      encodePipeFrame({ hashes: [hash], kind: "ask" }),
    );
    expect(asked).toMatchObject({
      reason: "hash in ASK could not be found",
      status: "invalid",
    });
  });

  it("sends EOS_ACK after receiving EOS", () => {
    const sender = new CodecSession({ id: sessionId(9) });
    const receiver = new CodecSession({ id: sessionId(10) });
    const closed = receiver.receive(sender.closeWrite());

    expect(closed.status).toBe("ok");
    expect(collectFrames(closed.responses).at(-1)).toEqual({ kind: "eos-ack" });
    expect(sender.receive(closed.responses).status).toBe("ok");
  });

  it("shares cache across sessions for cross-connection dedup", () => {
    const sharedCache = new MemoryCache();
    const input = new Uint8Array(XCODEC_SEGMENT_LENGTH * 2).fill(0x5a);

    const senderA = new CodecSession({ cache: sharedCache, id: sessionId(11) });
    const firstEncoded = senderA.encodeData(input);

    const senderB = new CodecSession({ cache: sharedCache, id: sessionId(12) });
    const secondEncoded = senderB.encodeData(input);

    expect(secondEncoded.length).toBeLessThan(firstEncoded.length);
  });

  it("announces the stable cache uuid in HELLO, not a per-connection id", () => {
    const cache = new MemoryCache();
    const session = new CodecSession({ cache });
    const frames = collectFrames(session.encodeData(segment(0x11)));
    const hello = frames.find((frame) => frame.kind === "hello");
    if (hello === undefined || hello.kind !== "hello") {
      throw new Error("expected HELLO");
    }
    expect(hello.id).toEqual(cache.uuid);
  });

  it("deduplicates across connections without an ASK/LEARN storm", () => {
    // Two proxies, each with one cache that persists across connections.
    const clientCache = new MemoryCache();
    const serverCache = new MemoryCache();
    const input = new Uint8Array(XCODEC_SEGMENT_LENGTH * 4).fill(0x33);

    // Connection 1: the client declares segments in-stream (EXTRACT) and
    // the server learns them into the per-peer decode cache it connects
    // for the client's stable uuid.
    const client1 = new CodecSession({ cache: clientCache });
    const server1 = new CodecSession({ cache: serverCache });
    const first = server1.receive(client1.encodeData(input));
    expect(first.status).toBe("ok");
    expect(first.output).toEqual(input);

    // Connection 2: a fresh session pair, but the same two caches.  The
    // client now emits references (its cache already holds the segments)
    // and the server resolves them from the memoized per-peer decode cache
    // without asking, because the client announced the same stable uuid.
    const client2 = new CodecSession({ cache: clientCache });
    const server2 = new CodecSession({ cache: serverCache });
    const encoded2 = client2.encodeData(input);
    const second = server2.receive(encoded2);

    expect(second.status).toBe("ok");
    expect(second.output).toEqual(input);
    const responses = collectFrames(second.responses);
    expect(responses.some((frame) => frame.kind === "ask")).toBe(false);
  });

  it("asks when a reconnecting peer presents a fresh cache uuid", () => {
    // The server cache persists, but the client uses a brand-new cache (a
    // different stable uuid), so the server connects a fresh, empty decode
    // view and must ASK for the referenced segment.
    const serverCache = new MemoryCache();
    const input = new Uint8Array(XCODEC_SEGMENT_LENGTH * 2).fill(0x44);

    const clientA = new MemoryCache();
    const sessionA = new CodecSession({ cache: clientA });
    const serverA = new CodecSession({ cache: serverCache });
    expect(serverA.receive(sessionA.encodeData(input)).output).toEqual(input);

    // A different client cache emits a reference the new peer view lacks.
    const clientB = new MemoryCache();
    const warm = new CodecSession({ cache: clientB });
    warm.encodeData(input);
    const sessionB = new CodecSession({ cache: clientB });
    const serverB = new CodecSession({ cache: serverCache });
    const result = serverB.receive(sessionB.encodeData(input));

    expect(result.status).toBe("ok");
    expect(collectFrames(result.responses).some((f) => f.kind === "ask")).toBe(
      true,
    );
  });
});
