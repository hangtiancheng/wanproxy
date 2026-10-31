import { concatByteArrays } from "../xcodec/byte-array.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";
import { XCodecDecoder } from "../xcodec/decoder.js";
import { XCodecEncoder } from "../xcodec/encoder.js";
import { MemoryCache } from "../xcodec/memory-cache.js";
import { PIPE_MAX_PAYLOAD_FRAME } from "./constants.js";
import { encodePipeFrame, type PipeControlFrame } from "./control-frame.js";
import { collectUnknownReferences } from "./frame-scanner.js";
import { ReferenceRetention } from "./reference-retention.js";
import {
  answerAsk,
  applyAdvance,
  applyLearnSegments,
} from "./session-actions.js";
import {
  type CodecSessionReceiveResult,
  invalid,
  ok,
} from "./session-result.js";
import { PipeSessionState } from "./session-state.js";

export interface CodecSessionOptions {
  readonly id?: Uint8Array;
  readonly cache?: XCodecCache;
}

export class CodecSession {
  private readonly cache: XCodecCache;
  private readonly encoder: XCodecEncoder;
  private readonly pendingFrames: Uint8Array[] = [];
  private readonly retention = new ReferenceRetention();
  private readonly state: PipeSessionState;
  private readonly unknownHashes = new Set<bigint>();
  /**
   * The decoder and its per-peer cache view are connected lazily when the
   * peer's <HELLO> arrives, mirroring the C++ XCodecPipePair which calls
   * XCodecCache::connect(peer_uuid) before constructing its decoder.  The
   * encoder keeps using the shared local cache.
   */
  private decoder: XCodecDecoder | undefined;
  private decodeCache: XCodecCache | undefined;

  public constructor(options: CodecSessionOptions = {}) {
    this.cache = options.cache ?? new MemoryCache();
    this.encoder = new XCodecEncoder({ cache: this.cache });
    // The identity announced in <HELLO> is the cache's stable UUID, not a
    // per-connection random id, so a peer reconnecting to the same cache
    // reuses its decoder cache and references resolve without new
    // <ASK>/<LEARN> exchanges.
    this.state = new PipeSessionState(options.id ?? this.cache.uuid);
  }

  public encodeData(input: Uint8Array): Uint8Array {
    const output: Uint8Array[] = [this.state.createHello()];
    let offset = 0;
    while (offset < input.length) {
      const chunk = input.subarray(offset, offset + PIPE_MAX_PAYLOAD_FRAME);
      const result = this.encoder.encodeWithReferences(chunk);
      this.retention.push(result.references);
      output.push(encodePipeFrame({ kind: "frame", payload: result.encoded }));
      offset += chunk.length;
    }
    return concatByteArrays(output);
  }

  public closeWrite(): Uint8Array {
    return concatByteArrays([this.state.createHello(), this.state.createEos()]);
  }

  public receive(input: Uint8Array): CodecSessionReceiveResult {
    const parsed = this.state.receive(input);
    if (parsed.status === "invalid") {
      return invalid(parsed.reason);
    }

    const output: Uint8Array[] = [];
    const responses: Uint8Array[] = [];
    for (const frame of parsed.frames) {
      const handled = this.handleFrame(frame);
      if (handled.status === "invalid") {
        return handled;
      }
      output.push(handled.output);
      responses.push(handled.responses);
    }
    return {
      output: concatByteArrays(output),
      responses: concatByteArrays(responses),
      status: "ok",
    };
  }

  private handleFrame(frame: PipeControlFrame): CodecSessionReceiveResult {
    switch (frame.kind) {
      case "hello":
        this.connectDecoder(frame.id);
        return ok();
      case "frame":
        this.pendingFrames.push(frame.payload);
        return this.drainPendingFrames();
      case "ask":
        return answerAsk(this.retention, frame.hashes);
      case "learn":
        return this.applyLearn(frame.segments);
      case "advance":
        return applyAdvance(this.retention, frame.count);
      case "eos":
        this.state.markEosAckSent();
        return {
          output: new Uint8Array(),
          responses: encodePipeFrame({ kind: "eos-ack" }),
          status: "ok",
        };
      case "eos-ack":
        return ok();
    }
  }

  /**
   * connectDecoder attaches the decode half to a per-peer cache view keyed
   * by the identity carried in <HELLO>, exactly like the C++
   * XCodecCache::connect(uuid, codec_->cache()).
   */
  private connectDecoder(peerId: Uint8Array): void {
    if (this.decoder !== undefined) {
      return;
    }
    this.decodeCache = this.cache.connect(peerId);
    this.decoder = new XCodecDecoder({ cache: this.decodeCache });
  }

  private drainPendingFrames(): CodecSessionReceiveResult {
    const decoder = this.decoder;
    const decodeCache = this.decodeCache;
    if (decoder === undefined || decodeCache === undefined) {
      return invalid("got FRAME before HELLO");
    }
    const output: Uint8Array[] = [];
    const responses: Uint8Array[] = [];
    let advanced = 0;

    while (this.pendingFrames.length > 0) {
      const frame = this.pendingFrames[0];
      if (frame === undefined) {
        break;
      }
      const unknown = collectUnknownReferences(frame, decodeCache);
      if (unknown.length > 0) {
        for (const hash of unknown) {
          this.unknownHashes.add(hash);
        }
        responses.push(encodePipeFrame({ hashes: unknown, kind: "ask" }));
        break;
      }
      const decoded = decoder.decode(frame);
      if (decoded.status === "invalid") {
        return invalid(decoded.reason);
      }
      if (decoded.status === "unknown-hash") {
        return invalid("unexpected unresolved hash after scan");
      }
      output.push(decoded.output);
      this.pendingFrames.shift();
      advanced += 1;
    }

    if (advanced > 0) {
      responses.unshift(encodePipeFrame({ count: advanced, kind: "advance" }));
    }
    return {
      output: concatByteArrays(output),
      responses: concatByteArrays(responses),
      status: "ok",
    };
  }

  private applyLearn(
    segments: readonly Uint8Array[],
  ): CodecSessionReceiveResult {
    const decodeCache = this.decodeCache;
    if (decodeCache === undefined) {
      return invalid("got LEARN before HELLO");
    }
    const learned = applyLearnSegments(
      decodeCache,
      this.unknownHashes,
      segments,
    );
    return learned.status === "invalid" ? learned : this.drainPendingFrames();
  }
}
