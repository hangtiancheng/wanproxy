#!/usr/bin/env node
import { closeSync, openSync } from "node:fs";
import { devNull } from "node:os";
import { argv, exit, stderr, stdin, stdout } from "node:process";

import { buildTackCache } from "../tack/cache.js";
import {
  parseTackArgs,
  TACK_USAGE,
  TackUsageError,
  validateTackOptions,
} from "../tack/options.js";
import {
  processFile,
  type TackAction,
  TackTimer,
  timeSamples,
  timeStats,
} from "../tack/tack.js";

/**
 * tack is the file-oriented XCodec dictionary compressor, mirroring the
 * C++ programs/tack: `tack [-p cache | -F fifo | -N] [-svQ] {-c|-d|-h}
 * [file ...]`.  With no files it reads stdin and writes stdout.
 */
function main(): void {
  // Writing to a closed pipe (e.g. `tack -c | head`) must exit cleanly
  // rather than surface an EPIPE error.
  stdout.on("error", (error: NodeJS.ErrnoException) => {
    if (error.code === "EPIPE") {
      exit(0);
    }
  });

  let parsed;
  try {
    parsed = parseTackArgs(argv.slice(2));
    validateTackOptions(parsed.options);
  } catch (error) {
    if (error instanceof TackUsageError) {
      stderr.write(TACK_USAGE);
      exit(1);
      return;
    }
    throw error;
  }

  const { options, files } = parsed;
  if (options.action === "none") {
    stderr.write(TACK_USAGE);
    exit(1);
    return;
  }
  const action: TackAction = options.action;
  const log = (message: string): void => {
    stderr.write(`${message}\n`);
  };

  // -T without -E shares one timer across every file and reports a total.
  const sharedTimer =
    options.codecTiming && !options.timingEach ? new TackTimer() : undefined;

  let tackCache;
  try {
    tackCache = buildTackCache(options);
  } catch (error) {
    stderr.write(`tack: ${errorMessage(error)}\n`);
    exit(1);
    return;
  }

  const outFd = options.quietOutput ? openSync(devNull, "w") : stdout.fd;
  let exitCode = 0;
  try {
    if (files.length === 0) {
      try {
        processFile("<stdin>", stdin.fd, outFd, {
          action,
          cache: tackCache.cache,
          codecTiming: options.codecTiming,
          log,
          stats: options.stats,
          timer: sharedTimer,
          timingEach: options.timingEach,
          timingSamples: options.timingSamples,
        });
      } catch (error) {
        if (isEpipe(error)) {
          exit(0);
        }
        throw error;
      }
    } else {
      for (const file of files) {
        let inputFd: number;
        try {
          inputFd = openSync(file, "r");
        } catch (error) {
          stderr.write(`could not open: ${file}: ${errorMessage(error)}\n`);
          exitCode = 1;
          continue;
        }
        try {
          processFile(file, inputFd, outFd, {
            action,
            cache: tackCache.cache,
            codecTiming: options.codecTiming,
            log,
            stats: options.stats,
            timer: sharedTimer,
            timingEach: options.timingEach,
            timingSamples: options.timingSamples,
          });
        } catch (error) {
          if (isEpipe(error)) {
            exit(0);
          }
          stderr.write(`tack: ${file}: ${errorMessage(error)}\n`);
          exitCode = 1;
        } finally {
          closeSync(inputFd);
        }
      }
    }
    if (sharedTimer !== undefined) {
      if (options.timingSamples) {
        timeSamples("", sharedTimer, log);
      } else {
        timeStats("<total>", sharedTimer, log);
      }
    }
  } finally {
    tackCache.close();
    if (options.quietOutput) {
      closeSync(outFd);
    }
  }
  if (exitCode !== 0) {
    exit(exitCode);
  }
}

function isEpipe(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as NodeJS.ErrnoException).code === "EPIPE"
  );
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

main();
