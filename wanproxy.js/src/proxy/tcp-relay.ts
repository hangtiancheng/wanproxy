import type { Socket } from "node:net";
import { Transform } from "node:stream";

import type { CodecConfig } from "../config/codec-config.js";
import type { CodecStats } from "../pipeline/codec-stats.js";
import type { PipelineRuntimeDeps } from "../pipeline/pipeline-builder.js";
import { buildCodecPipeline } from "../pipeline/pipeline-builder.js";
import { proxyPipelineOptions } from "./pipeline-options.js";
import { createRelayCloseController } from "./relay-close.js";
import type { RelayResult } from "./relay-result.js";

export function relayTcpSockets(
  local: Socket,
  upstream: Socket,
  codec: CodecConfig,
  deps: PipelineRuntimeDeps = {},
  stats?: CodecStats,
): RelayResult {
  const send = buildCodecPipeline(
    proxyPipelineOptions(codec, "send"),
    deps,
  ).transforms;
  const receive = buildCodecPipeline(
    proxyPipelineOptions(codec, "receive"),
    deps,
  ).transforms;
  // When statistics tracking is enabled, wrap each direction with counting
  // pass-throughs at the socket boundaries, mirroring the Go relay's count
  // callbacks: bytes read from the local socket enter the codec
  // (incoming_to_codec), bytes written to the upstream socket leave it
  // (codec_to_outgoing), and the receive direction is symmetric
  // (outgoing_to_codec / codec_to_incoming).  The positions hold for both
  // chain sides because a proxy's single codec always sits between its
  // local and upstream sockets.
  const sendChain =
    stats === undefined
      ? send
      : [
          countingStream((n) => stats.addIncomingToCodec(n)),
          ...send,
          countingStream((n) => stats.addCodecToOutgoing(n)),
        ];
  const receiveChain =
    stats === undefined
      ? receive
      : [
          countingStream((n) => stats.addOutgoingToCodec(n)),
          ...receive,
          countingStream((n) => stats.addCodecToIncoming(n)),
        ];
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
  source: Socket,
  transforms: readonly Transform[],
  destination: Socket,
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
 * Each socket's write side is half-closed and its read side is kept
 * draining, so a peer's remaining bytes — including a trailing <EOS> — are
 * consumed and the socket reaches EOF instead of being abortively reset
 * with unread data outstanding.  This mirrors the Go relay's post-EOS
 * drain phase, which keeps reading the source until TCP EOF precisely so
 * the final close cannot discard data that is still in flight.
 */
function teardownGracefully(local: Socket, upstream: Socket): void {
  halfCloseAndDrain(local);
  halfCloseAndDrain(upstream);
}

function halfCloseAndDrain(socket: Socket): void {
  if (socket.destroyed) {
    return;
  }
  if (socket.writable) {
    socket.end();
  }
  // Keep consuming incoming data so the socket reaches EOF and closes
  // gracefully rather than being reset with unread data outstanding.
  socket.resume();
}

function destroyAll(
  local: Socket,
  upstream: Socket,
  transforms: readonly Transform[],
): void {
  local.destroy();
  upstream.destroy();
  for (const transform of transforms) {
    transform.destroy();
  }
}
