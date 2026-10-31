import type { Duplex } from "node:stream";
import { Transform } from "node:stream";

import type { CodecConfig } from "../config/codec-config.js";
import type { CodecStats } from "../pipeline/codec-stats.js";
import type { PipelineRuntimeDeps } from "../pipeline/pipeline-builder.js";
import type { PipelineMode } from "../pipeline/pipeline-options.js";
import { buildCodecPipeline } from "../pipeline/pipeline-builder.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";
import { createRelayCloseController } from "./relay-close.js";
import type { RelayResult } from "./relay-result.js";

/**
 * RelayCodecSpec is one codec slot of a relay (interface or peer), carrying
 * the pipeline mode, the optional zlib level, and the XCodec cache backing
 * the session.  It mirrors the Go pipeline.CodecSpec fields the relay needs.
 */
export interface RelayCodecSpec {
  readonly mode: PipelineMode;
  readonly compressorLevel?: number;
  readonly cache?: XCodecCache;
}

/**
 * RelayStats holds the per-codec byte counters, mirroring the Go relay's
 * ifaceCodec.Stats / peerCodec.Stats.  Either may be absent when the codec
 * was not activated with track_statistics.
 */
export interface RelayStats {
  readonly iface?: CodecStats;
  readonly peer?: CodecStats;
}

/**
 * relayTcpSockets relays a single-codec TCP proxy, mirroring the C++
 * splice and the Go relay.  It is a thin wrapper over relayStreams that
 * places the codec on the interface or peer slot according to its role.
 */
export function relayTcpSockets(
  local: Duplex,
  upstream: Duplex,
  codec: CodecConfig,
  deps: PipelineRuntimeDeps = {},
  stats?: CodecStats,
): RelayResult {
  const spec: RelayCodecSpec = {
    ...(deps.cache === undefined ? {} : { cache: deps.cache }),
    ...(codec.compressorLevel === undefined
      ? {}
      : { compressorLevel: codec.compressorLevel }),
    mode: codec.mode,
  };
  const relayStats: RelayStats | undefined =
    stats === undefined
      ? undefined
      : codec.role === "outgoing"
        ? { iface: stats }
        : { peer: stats };
  return codec.role === "outgoing"
    ? relayStreams(local, upstream, spec, undefined, relayStats)
    : relayStreams(local, upstream, undefined, spec, relayStats);
}

/**
 * relayStreams moves data between a local (interface side) and an upstream
 * (peer side) duplex stream through the codec pipeline composed from the
 * interface and peer codec specs, mirroring the Go Relay:
 *
 *	local → upstream: [interface decode] then [peer encode]
 *	upstream → local: [peer decode] then [interface encode]
 *
 * where the first bracket applies when an interface codec is configured and
 * the second when a peer codec is configured.  A single-codec proxy is the
 * common case (one of the two specs is undefined); a proxy may also carry
 * both, transcoding between the interface and peer codecs.
 *
 * When statistics are provided, counting pass-throughs are inserted at the
 * four stream boundaries and — for dual-codec proxies — at the two internal
 * boundaries between the codecs, mirroring the Go relay's count callbacks:
 *
 *	local read     → iface.incoming_to_codec  (or peer's when no iface)
 *	upstream write → peer.codec_to_outgoing   (or iface's when no peer)
 *	upstream read  → peer.outgoing_to_codec   (or iface's when no peer)
 *	local write    → iface.codec_to_incoming  (or peer's when no iface)
 *	send internal  → iface.codec_to_outgoing + peer.incoming_to_codec
 *	recv internal  → peer.codec_to_incoming + iface.outgoing_to_codec
 *
 * Half-close is propagated by ending the destination write side when the
 * source read side ends, while the destination's read side stays open
 * (allowHalfOpen) so the reverse direction can still deliver crossed
 * control frames.
 */
export function relayStreams(
  local: Duplex,
  upstream: Duplex,
  iface: RelayCodecSpec | undefined,
  peer: RelayCodecSpec | undefined,
  stats?: RelayStats,
): RelayResult {
  const ifaceDecode = stageTransforms(iface, "outgoing", "send");
  const peerEncode = stageTransforms(peer, "incoming", "send");
  const peerDecode = stageTransforms(peer, "incoming", "receive");
  const ifaceEncode = stageTransforms(iface, "outgoing", "receive");

  const ifaceStats = stats?.iface;
  const peerStats = stats?.peer;
  const dual = iface !== undefined && peer !== undefined;

  // local→upstream: [iface decode] then [peer encode], counted at the local
  // read boundary, the (dual) internal boundary, and the upstream write
  // boundary.
  const sendChain: Transform[] = [];
  const sendEnter = ifaceStats ?? peerStats;
  if (sendEnter !== undefined) {
    sendChain.push(countingStream((n) => sendEnter.addIncomingToCodec(n)));
  }
  sendChain.push(...ifaceDecode);
  if (dual && ifaceStats !== undefined && peerStats !== undefined) {
    sendChain.push(
      countingStream((n) => {
        ifaceStats.addCodecToOutgoing(n);
        peerStats.addIncomingToCodec(n);
      }),
    );
  }
  sendChain.push(...peerEncode);
  const sendExit = peerStats ?? ifaceStats;
  if (sendExit !== undefined) {
    sendChain.push(countingStream((n) => sendExit.addCodecToOutgoing(n)));
  }

  // upstream→local: [peer decode] then [iface encode], counted at the
  // upstream read boundary, the (dual) internal boundary, and the local
  // write boundary.
  const receiveChain: Transform[] = [];
  const receiveEnter = peerStats ?? ifaceStats;
  if (receiveEnter !== undefined) {
    receiveChain.push(
      countingStream((n) => receiveEnter.addOutgoingToCodec(n)),
    );
  }
  receiveChain.push(...peerDecode);
  if (dual && ifaceStats !== undefined && peerStats !== undefined) {
    receiveChain.push(
      countingStream((n) => {
        peerStats.addCodecToIncoming(n);
        ifaceStats.addOutgoingToCodec(n);
      }),
    );
  }
  receiveChain.push(...ifaceEncode);
  const receiveExit = ifaceStats ?? peerStats;
  if (receiveExit !== undefined) {
    receiveChain.push(countingStream((n) => receiveExit.addCodecToIncoming(n)));
  }

  const streams = [...sendChain, ...receiveChain];
  const destroy = () => destroyAll(local, upstream, streams);
  const close = createRelayCloseController(local, upstream, streams, destroy);

  wireDirection(local, sendChain, upstream);
  wireDirection(upstream, receiveChain, local);

  return {
    closed: close.closed,
    stop: () => {
      teardownGracefully(local, upstream);
      close.resolve();
    },
  };
}

/**
 * stageTransforms builds the transforms for one codec stage, or an empty
 * array when the codec is absent or in pass-through mode.
 */
function stageTransforms(
  spec: RelayCodecSpec | undefined,
  role: "incoming" | "outgoing",
  direction: "send" | "receive",
): readonly Transform[] {
  if (spec === undefined || spec.mode === "none") {
    return [];
  }
  const deps: PipelineRuntimeDeps =
    spec.cache === undefined ? {} : { cache: spec.cache };
  return buildCodecPipeline(
    {
      compressorLevel: spec.compressorLevel,
      direction,
      mode: spec.mode,
      role,
    },
    deps,
  ).transforms;
}

/**
 * wireDirection pumps one direction of the relay through the codec
 * transforms and propagates half-close, mirroring the C++ splice and the
 * Go relay.  When the source read side ends — after the transforms have
 * flushed any trailing <EOS> — the destination write side is ended, while
 * the destination's read side stays open (allowHalfOpen) so the reverse
 * direction can still deliver crossed control frames (<EOS_ACK>,
 * <ADVANCE>).  This is what keeps the final close from turning into a
 * reset that loses delivered data.
 */
function wireDirection(
  source: Duplex,
  transforms: readonly Transform[],
  destination: Duplex,
): void {
  let current: NodeJS.ReadableStream = source;
  for (const transform of transforms) {
    current = current.pipe(transform);
  }
  current.pipe(destination, { end: false });
  current.once("end", () => {
    if (!destination.destroyed && destination.writable) {
      destination.end();
    }
  });
}

/**
 * teardownGracefully closes the relay without resetting the connections.
 * Each stream's write side is half-closed and its read side is kept
 * draining, so a peer's remaining bytes — including a trailing <EOS> — are
 * consumed and the stream reaches EOF instead of being abortively reset
 * with unread data outstanding.  This mirrors the Go relay's post-EOS
 * drain phase, which keeps reading the source until TCP EOF precisely so
 * the final close cannot discard data that is still in flight.
 */
function teardownGracefully(local: Duplex, upstream: Duplex): void {
  halfCloseAndDrain(local);
  halfCloseAndDrain(upstream);
}

function halfCloseAndDrain(stream: Duplex): void {
  if (stream.destroyed) {
    return;
  }
  if (stream.writable) {
    stream.end();
  }
  // Keep consuming incoming data so the stream reaches EOF and closes
  // gracefully rather than being reset with unread data outstanding.
  stream.resume();
}

function destroyAll(
  local: Duplex,
  upstream: Duplex,
  transforms: readonly Transform[],
): void {
  local.destroy();
  upstream.destroy();
  for (const transform of transforms) {
    transform.destroy();
  }
}

/**
 * countingStream returns a pass-through transform that reports the length
 * of every chunk it forwards.  It adds no buffering, so relay timing and
 * half-close behaviour are unchanged.
 */
function countingStream(count: (bytes: number) => void): Transform {
  return new Transform({
    transform(chunk, _encoding, callback) {
      count(chunk.length);
      callback(null, chunk);
    },
  });
}
