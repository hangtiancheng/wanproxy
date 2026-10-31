#!/usr/bin/env node
import { closeSync, openSync, readSync } from "node:fs";
import { argv, exit, stderr, stdin, stdout } from "node:process";

import { XCodecStreamDumper } from "../xcodec/stream-dump.js";

const READ_SIZE = 65536;
const USAGE = "usage: xcdump [-v] [file ...]\n";

/**
 * xcdump is the XCodec stream decoder and pretty-printer, mirroring the C++
 * programs/xcdump: `xcdump [-v] [file ...]`.  With no files it reads
 * standard input.  Each -v increases verbosity (hash/offset attributes,
 * then hexdumps).
 */
function main(): void {
  // Writing to a closed pipe (e.g. `xcdump | head`) must exit cleanly
  // rather than crash with an unhandled EPIPE on the stdout socket.
  stdout.on("error", (error: NodeJS.ErrnoException) => {
    if (error.code === "EPIPE") {
      exit(0);
    }
    stderr.write(`xcdump: ${error.message}\n`);
    exit(1);
  });

  let verbosity = 0;
  const files: string[] = [];
  const args = argv.slice(2);

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i] ?? "";
    if (arg === "--") {
      files.push(...args.slice(i + 1));
      break;
    }
    if (arg === "-?" || arg === "-h" || arg === "--help") {
      stderr.write(USAGE);
      exit(arg === "-?" ? 1 : 0);
      return;
    }
    if (arg.length >= 2 && arg.startsWith("-")) {
      for (const flag of arg.slice(1)) {
        if (flag !== "v") {
          stderr.write(USAGE);
          exit(1);
          return;
        }
        verbosity += 1;
      }
      continue;
    }
    files.push(arg);
  }

  const sink = (line: string): void => {
    stdout.write(`${line}\n`);
  };

  if (files.length === 0) {
    dumpFd(stdin.fd, verbosity, sink);
    return;
  }
  for (const file of files) {
    let fd: number;
    try {
      fd = openSync(file, "r");
    } catch (error) {
      stderr.write(`could not open ${file}: ${errorMessage(error)}\n`);
      exit(1);
      return;
    }
    try {
      dumpFd(fd, verbosity, sink);
    } finally {
      closeSync(fd);
    }
  }
}

function dumpFd(
  fd: number,
  verbosity: number,
  sink: (line: string) => void,
): void {
  const dumper = new XCodecStreamDumper(verbosity, sink);
  const buffer = Buffer.allocUnsafe(READ_SIZE);
  for (;;) {
    const bytesRead = readSync(fd, buffer, 0, READ_SIZE, null);
    if (bytesRead <= 0) {
      break;
    }
    dumper.write(buffer.subarray(0, bytesRead));
  }
  dumper.end();
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

main();
