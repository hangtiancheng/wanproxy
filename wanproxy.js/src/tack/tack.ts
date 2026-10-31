import { readSync, writeSync } from "node:fs";
import { hrtime } from "node:process";

import type { XCodecCache } from "../xcodec/cache-interface.js";
import { XCODEC_SEGMENT_LENGTH } from "../xcodec/constants.js";
import { type DecodeResult, XCodecDecoder } from "../xcodec/decoder.js";
import { XCodecEncoder } from "../xcodec/encoder.js";
import { XCodecHash } from "../xcodec/hash.js";
import { encodeUint64BE } from "../xcodec/uint64.js";

const READ_SIZE = 65536;

export type TackAction = "compress" | "decompress" | "hashes";

export interface TackFileOptions {
  readonly action: TackAction;
  readonly cache: XCodecCache;
  readonly stats: boolean;
  readonly log?: (message: string) => void;
  /** -T: time codec calls. */
  readonly codecTiming?: boolean;
  /** -E: time each file separately (a fresh timer per file). */
  readonly timingEach?: boolean;
  /** -S: print individual samples instead of aggregate stats. */
  readonly timingSamples?: boolean;
  /** A shared timer, used when -T is set without -E. */
  readonly timer?: TackTimer | undefined;
}

/**
 * TackTimer collects microsecond samples of codec call durations,
 * mirroring the C++ common/timer Timer used by tack and the Go timer.
 */
export class TackTimer {
  private readonly sampleList: number[] = [];
  private start = 0n;
  private running = false;

  /** begin starts timing one codec call. */
  public begin(): void {
    this.start = hrtime.bigint();
    this.running = true;
  }

  /** stop ends the current sample, recording its duration in microseconds. */
  public stop(): void {
    if (!this.running) {
      return;
    }
    this.running = false;
    const elapsed = (hrtime.bigint() - this.start) / 1000n;
    this.sampleList.push(Number(elapsed));
  }

  /** samples returns the recorded microsecond durations. */
  public get samples(): readonly number[] {
    return this.sampleList;
  }
}

/**
 * timeStats prints the aggregate timer statistics for one name, mirroring
 * the Go timeStats output.
 */
export function timeStats(
  name: string,
  timer: TackTimer,
  log: (message: string) => void,
): void {
  let total = 0;
  for (const sample of timer.samples) {
    total += sample;
  }
  log(`${name}: ${timer.samples.length} timer samples.`);
  log(`${name}: ${total} total runtime.`);
  if (timer.samples.length > 0) {
    log(
      `${name}: ${Math.floor(total / timer.samples.length)} mean microseconds/call.`,
    );
  }
}

/**
 * timeSamples prints each individual timer sample, mirroring the Go
 * timeSamples output (bare numbers for the empty name, name,value
 * otherwise).
 */
export function timeSamples(
  name: string,
  timer: TackTimer,
  log: (message: string) => void,
): void {
  for (const sample of timer.samples) {
    log(name === "" ? `${sample}` : `${name},${sample}`);
  }
}

/**
 * processFile runs the configured tack action over one input/output file
 * descriptor pair, mirroring the C++ process_file dispatch.
 */
export function processFile(
  name: string,
  inputFd: number,
  outputFd: number,
  options: TackFileOptions,
): void {
  const timer = resolveTimer(options);
  switch (options.action) {
    case "compress":
      compress(name, inputFd, outputFd, options, timer);
      break;
    case "decompress":
      decompress(name, inputFd, outputFd, options, timer);
      break;
    case "hashes":
      hashes(inputFd, outputFd, timer);
      break;
  }
  // Per-file timing output when -T -E created a fresh timer for this file.
  if (
    timer !== undefined &&
    timer !== options.timer &&
    options.log !== undefined
  ) {
    const log = options.log;
    if (options.timingSamples === true) {
      timeSamples(name, timer, log);
    } else {
      timeStats(name, timer, log);
    }
  }
}

/**
 * resolveTimer picks the effective timer: a fresh per-file timer when -T -E
 * are set, the shared timer when only -T is set, and none otherwise.
 */
function resolveTimer(options: TackFileOptions): TackTimer | undefined {
  if (options.codecTiming !== true) {
    return undefined;
  }
  if (options.timingEach === true) {
    return new TackTimer();
  }
  return options.timer;
}

function compress(
  name: string,
  inputFd: number,
  outputFd: number,
  options: TackFileOptions,
  timer: TackTimer | undefined,
): void {
  const encoder = new XCodecEncoder({ cache: options.cache });
  const input = Buffer.allocUnsafe(READ_SIZE);
  let inbytes = 0;
  let outbytes = 0;

  for (;;) {
    const bytesRead = readSync(inputFd, input, 0, READ_SIZE, null);
    if (bytesRead <= 0) {
      break;
    }
    if (options.stats) {
      inbytes += bytesRead;
    }
    timer?.begin();
    const encoded = encoder.encode(input.subarray(0, bytesRead));
    timer?.stop();
    if (options.stats) {
      outbytes += encoded.length;
    }
    writeFull(outputFd, encoded);
  }

  if (options.stats) {
    printRatio(name, inbytes, outbytes, options.log);
  }
}

function decompress(
  name: string,
  inputFd: number,
  outputFd: number,
  options: TackFileOptions,
  timer: TackTimer | undefined,
): void {
  const decoder = new XCodecDecoder({ cache: options.cache });
  const input = Buffer.allocUnsafe(READ_SIZE);
  let inbytes = 0;
  let outbytes = 0;

  for (;;) {
    const bytesRead = readSync(inputFd, input, 0, READ_SIZE, null);
    if (bytesRead <= 0) {
      break;
    }
    if (options.stats) {
      inbytes += bytesRead;
    }
    timer?.begin();
    const result = decoder.decode(input.subarray(0, bytesRead));
    timer?.stop();
    assertDecodable(result);
    if (result.output.length > 0) {
      if (options.stats) {
        outbytes += result.output.length;
      }
      writeFull(outputFd, result.output);
    }
  }

  // Flush the decoder and ensure the stream is complete.
  timer?.begin();
  const final = decoder.decode(new Uint8Array());
  timer?.stop();
  if (final.status === "need-more") {
    throw new Error("incomplete XCodec stream");
  }
  assertDecodable(final);
  if (final.output.length > 0) {
    if (options.stats) {
      outbytes += final.output.length;
    }
    writeFull(outputFd, final.output);
  }

  if (options.stats) {
    // Reverse order of compress(): decompressed bytes first.
    printRatio(name, outbytes, inbytes, options.log);
  }
}

function hashes(
  inputFd: number,
  outputFd: number,
  timer: TackTimer | undefined,
): void {
  const hash = new XCodecHash();
  const input = Buffer.allocUnsafe(READ_SIZE);
  let fed = 0;

  for (;;) {
    const bytesRead = readSync(inputFd, input, 0, READ_SIZE, null);
    if (bytesRead <= 0) {
      break;
    }
    timer?.begin();
    for (let index = 0; index < bytesRead; index += 1) {
      if (fed >= XCODEC_SEGMENT_LENGTH) {
        continue;
      }
      hash.add(input[index] ?? 0);
      fed += 1;
      if (fed === XCODEC_SEGMENT_LENGTH) {
        writeFull(outputFd, encodeUint64BE(hash.mix()));
        hash.reset();
        fed = 0;
      }
    }
    timer?.stop();
  }
}

function assertDecodable(result: DecodeResult): void {
  if (result.status === "invalid") {
    throw new Error(`decoder error: ${result.reason}`);
  }
  if (result.status === "unknown-hash") {
    throw new Error(
      `decoder needs ${result.hashes.length} unknown hashes; is the cache correct?`,
    );
  }
}

function writeFull(fd: number, data: Uint8Array): void {
  let offset = 0;
  while (offset < data.length) {
    const written = writeSync(fd, data, offset, data.length - offset, null);
    if (written <= 0) {
      throw new Error("write failed");
    }
    offset += written;
  }
}

function printRatio(
  name: string,
  inbytes: number,
  outbytes: number,
  log: ((message: string) => void) | undefined,
): void {
  if (log === undefined) {
    return;
  }
  log(`${name}: ${inbytes} uncompressed bytes, ${outbytes} compressed bytes.`);
  if (inbytes <= outbytes || outbytes === 0) {
    if (outbytes === 0) {
      log(`${name}: no compressed bytes.`);
      return;
    }
    log(
      `${name}: bloat ratio 1:${(outbytes / inbytes).toFixed(6)} (${(
        inbytes / outbytes
      ).toFixed(6)}:1)`,
    );
    return;
  }
  log(
    `${name}: compression ratio 1:${(outbytes / inbytes).toFixed(6)} (${(
      inbytes / outbytes
    ).toFixed(6)}:1)`,
  );
}
