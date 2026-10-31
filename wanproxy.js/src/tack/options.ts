import type { TackAction } from "./tack.js";

export interface TackOptions {
  readonly action: TackAction | "none";
  /** -p: path to a persistent dictionary cache (concatenated segments). */
  readonly persist: string;
  /** -F: path to an XCDFS FIFO disk cache. */
  readonly fifo: string;
  /** -N: use a null (out-of-band) cache. */
  readonly nullCache: boolean;
  /** -s: print byte statistics. */
  readonly stats: boolean;
  /** -v: enable debug logging. */
  readonly verbose: boolean;
  /** -Q: discard output. */
  readonly quietOutput: boolean;
  /** -T: time codec calls. */
  readonly codecTiming: boolean;
  /** -E: time each file separately (requires -T). */
  readonly timingEach: boolean;
  /** -S: print individual timer samples instead of aggregates (requires -T). */
  readonly timingSamples: boolean;
}

export interface ParsedTackArgs {
  readonly options: TackOptions;
  readonly files: readonly string[];
}

export class TackUsageError extends Error {
  public constructor() {
    super("usage error");
    this.name = "TackUsageError";
  }
}

export const TACK_USAGE =
  "usage: tack [-p cache | -F fifo | -N] [-svQ] [-T [-ES]] -c [file ...]\n" +
  "       tack [-p cache | -F fifo | -N] [-svQ] [-T [-ES]] -d [file ...]\n" +
  "       tack [-vQ] [-T [-ES]] -h [file ...]\n";

/**
 * parseTackArgs mirrors the C++ getopt("?cdhp:svEF:NQST") loop, supporting
 * clustered short flags, `-p`/`-F` with an attached or following argument,
 * and the `--` end-of-options separator.  The timing flags -T (time codec
 * calls), -E (time each file) and -S (print samples) are ported.
 */
export function parseTackArgs(args: readonly string[]): ParsedTackArgs {
  let action: TackAction | "none" = "none";
  let persist = "";
  let fifo = "";
  let nullCache = false;
  let stats = false;
  let verbose = false;
  let quietOutput = false;
  let codecTiming = false;
  let timingEach = false;
  let timingSamples = false;
  const files: string[] = [];

  let i = 0;
  while (i < args.length) {
    const arg = args[i] ?? "";
    i += 1;

    if (arg === "--") {
      files.push(...args.slice(i));
      break;
    }

    if (arg.length >= 2 && arg.startsWith("-")) {
      let j = 1;
      while (j < arg.length) {
        const flag = arg[j];
        j += 1;
        switch (flag) {
          case "c":
            action = "compress";
            break;
          case "d":
            action = "decompress";
            break;
          case "h":
            action = "hashes";
            break;
          case "N":
            nullCache = true;
            break;
          case "s":
            stats = true;
            break;
          case "v":
            verbose = true;
            break;
          case "Q":
            quietOutput = true;
            break;
          case "T":
            codecTiming = true;
            break;
          case "E":
            timingEach = true;
            break;
          case "S":
            timingSamples = true;
            break;
          case "p":
            persist = takeValue(arg, j, args, i);
            if (j >= arg.length) {
              i += 1;
            }
            j = arg.length;
            break;
          case "F":
            fifo = takeValue(arg, j, args, i);
            if (j >= arg.length) {
              i += 1;
            }
            j = arg.length;
            break;
          default:
            throw new TackUsageError();
        }
      }
      continue;
    }

    files.push(arg);
  }

  return {
    files,
    options: {
      action,
      codecTiming,
      fifo,
      nullCache,
      persist,
      quietOutput,
      stats,
      timingEach,
      timingSamples,
      verbose,
    },
  };
}

/**
 * validateTackOptions enforces the C++ mutual-exclusion rules between the
 * action and the cache/flag selections.
 */
export function validateTackOptions(options: TackOptions): void {
  if (options.action === "none") {
    throw new TackUsageError();
  }
  if (
    options.action === "hashes" &&
    (options.stats ||
      options.nullCache ||
      options.persist !== "" ||
      options.fifo !== "")
  ) {
    throw new TackUsageError();
  }
  const cacheSelectors =
    Number(options.persist !== "") +
    Number(options.fifo !== "") +
    Number(options.nullCache);
  if (cacheSelectors > 1) {
    throw new TackUsageError();
  }
  // -E and -S only mean something together with -T, as in the C++ tack.
  if (!options.codecTiming && (options.timingEach || options.timingSamples)) {
    throw new TackUsageError();
  }
}

function takeValue(
  arg: string,
  j: number,
  args: readonly string[],
  nextIndex: number,
): string {
  if (j < arg.length) {
    return arg.slice(j);
  }
  const next = args[nextIndex];
  if (next === undefined) {
    throw new TackUsageError();
  }
  return next;
}
