import type { Transform } from "node:stream";

import { CodecSession } from "../pipe-protocol/codec-session.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";
import { createPassThroughTransform } from "./pass-through-transform.js";
import {
  type PipelineOptions,
  type PipelineDirection,
  type PipelineRole,
  PipelineOptionsSchema,
} from "./pipeline-options.js";
import {
  createSessionDecodeTransform,
  createSessionEncodeTransform,
} from "./session-transform.js";
import { createZlibTransform } from "./zlib-transform.js";

export interface PipelineRuntimeDeps {
  readonly cache?: XCodecCache;
}

export interface PipelineBuildResult {
  readonly steps: readonly string[];
  readonly transforms: readonly Transform[];
}

export function buildCodecPipeline(
  input: unknown,
  deps: PipelineRuntimeDeps = {},
): PipelineBuildResult {
  const options = PipelineOptionsSchema.parse(input);
  const steps = resolveSteps(options);
  const transforms = steps.map((step) => createStep(step, options, deps));
  return { steps, transforms };
}

export function resolvePipelineSteps(input: unknown): readonly string[] {
  return resolveSteps(PipelineOptionsSchema.parse(input));
}

function resolveSteps(options: PipelineOptions): readonly string[] {
  const role = options.role ?? "incoming";
  const towardWan = towardWAN(role, options.direction);
  switch (options.mode) {
    case "none":
      return ["pass-through"];
    case "zlib":
      return towardWan ? ["deflate"] : ["inflate"];
    case "xcodec":
      return towardWan ? ["xcodec-encode"] : ["xcodec-decode"];
    case "zlib-xcodec":
      return towardWan
        ? ["xcodec-encode", "deflate"]
        : ["inflate", "xcodec-decode"];
  }
}

/**
 * towardWAN reports whether data in the given direction flows toward the
 * WAN side for a codec with the given role, mirroring the C++
 * WANProxyCodecPipePair ordering: data flowing toward the WAN is encoded
 * then deflated, data flowing in from the WAN is inflated then decoded.
 *
 * - role "incoming" is the peer codec (C++ peer_codec,
 *   XCodecPipePairTypeClient); the WAN side is the upstream side, so the
 *   "send" direction (listen->upstream) flows toward the WAN.
 * - role "outgoing" is the interface codec (C++ interface_codec,
 *   XCodecPipePairTypeServer); the WAN side is the listen side, so the
 *   "receive" direction (upstream->listen) flows toward the WAN.
 */
function towardWAN(role: PipelineRole, direction: PipelineDirection): boolean {
  if (role === "incoming") {
    return direction === "send";
  }
  return direction === "receive";
}

function createStep(
  step: string,
  options: PipelineOptions,
  deps: PipelineRuntimeDeps,
): Transform {
  switch (step) {
    case "pass-through":
      return createPassThroughTransform();
    case "inflate":
      return createZlibTransform("inflate", zlibOptions(options));
    case "deflate":
      return createZlibTransform("deflate", zlibOptions(options));
    case "xcodec-encode":
      return createSessionEncodeTransform(
        new CodecSession(deps.cache ? { cache: deps.cache } : {}),
      );
    case "xcodec-decode":
      return createSessionDecodeTransform(
        new CodecSession(deps.cache ? { cache: deps.cache } : {}),
      );
    default:
      throw new Error(`unsupported pipeline step ${step}`);
  }
}

function zlibOptions(options: PipelineOptions): { readonly level?: number } {
  return options.compressorLevel === undefined
    ? {}
    : { level: options.compressorLevel };
}
