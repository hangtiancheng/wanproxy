/**
 * ToolsService analyzes a stored file with the wanproxy.js tack (in-band +
 * dictionary modes) and xcdump, summarizing the resulting XCodec frame
 * stream.  It is the TypeScript port of srv/internal/tools/tools.go,
 * invoking the wanproxy.js CLIs instead of the Go binaries.
 */

import { spawn } from 'node:child_process';
import { createWriteStream } from 'node:fs';
import { mkdirSync, rmSync, statSync } from 'node:fs';
import { join } from 'node:path';

import { Injectable } from '@nestjs/common';

import { getPaths } from '../common/paths.js';
import { FilesService } from '../files/files.service.js';

/** HashCount is one rolling-hash value and how often it appeared. */
export interface HashCount {
  hash: string;
  count: number;
}

/** Frame is a summarized dump line. */
export interface Frame {
  kind: string;
  detail?: string;
}

/** FrameSummary aggregates the xcdump frame statistics. */
export interface FrameSummary {
  extract: number;
  ref: number;
  escape: number;
  data: number;
  dataBytes: number;
  topHashes: HashCount[];
  firstFrames: Frame[];
}

/** AnalyzeResult is the JSON document returned by POST /api/tools/analyze. */
export interface AnalyzeResult {
  file: { id: string; name: string; size: number };
  inband: { compressedSize: number; ratio: number; stats: string };
  dictionary: { firstPassSize: number; secondPassSize: number; ratio: number };
  frames: FrameSummary;
}

const HASH_RE = /hash="0x([0-9a-f]+)"/;
const DATA_RE = /data="([0-9a-f]*)"/;
const KIND_RE = /^<([a-z-]+)/;

@Injectable()
export class ToolsService {
  public constructor(private readonly files: FilesService) {}

  /** analyze compresses the file with tack and dumps it with xcdump. */
  public async analyze(fileId: string): Promise<AnalyzeResult> {
    const meta = this.files.get(fileId);
    const src = this.files.filePath(fileId);
    const p = getPaths();
    const workDir = join(p.toolsDir, fileId);
    rmSync(workDir, { force: true, recursive: true });
    mkdirSync(workDir, { recursive: true });

    const inbandPath = join(workDir, 'inband.tack');
    const dictPath = join(workDir, 'dict.cache');
    const dict1Path = join(workDir, 'dict1.tack');
    const dict2Path = join(workDir, 'dict2.tack');

    try {
      // In-band compression (EXTRACT declarations travel in the stream).
      const inbandStats = await this.runTack(['-c', '-s', src], inbandPath);
      const inbandSize = fileSize(inbandPath);

      // Dictionary mode: the first pass builds an out-of-band dictionary,
      // the next two reference it (bare REFs, no EXTRACTs).
      await this.runTack(['-c', '-Q', '-p', dictPath, src], null);
      await this.runTack(['-c', '-p', dictPath, src], dict1Path);
      await this.runTack(['-c', '-p', dictPath, src], dict2Path);
      const dict1Size = fileSize(dict1Path);
      const dict2Size = fileSize(dict2Path);

      const dump = await this.runXcdump(inbandPath);
      const frames = parseDump(dump);

      let inbandRatio = 0;
      let dictRatio = 0;
      if (meta.size > 0) {
        inbandRatio = inbandSize / meta.size;
        dictRatio = dict2Size / meta.size;
      }
      return {
        dictionary: {
          firstPassSize: dict1Size,
          ratio: dictRatio,
          secondPassSize: dict2Size,
        },
        file: { id: meta.id, name: meta.name, size: meta.size },
        frames,
        inband: {
          compressedSize: inbandSize,
          ratio: inbandRatio,
          stats: inbandStats,
        },
      };
    } finally {
      rmSync(workDir, { force: true, recursive: true });
    }
  }

  private runTack(args: string[], outFile: string | null): Promise<string> {
    return runNode(getPaths().tackCli, args, outFile, 'tack ' + args.join(' '));
  }

  private runXcdump(inbandPath: string): Promise<string> {
    return runNodeCapture(getPaths().xcdumpCli, ['-v', inbandPath], 'xcdump');
  }
}

function fileSize(path: string): number {
  return statSync(path).size;
}

/** runNode runs a wanproxy.js CLI, streaming stdout to a file (or null). */
function runNode(
  cli: string,
  args: string[],
  outFile: string | null,
  label: string,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...args], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stderr = '';
    child.stderr.on('data', (d: Buffer) => {
      stderr += d.toString();
    });
    const closed = new Promise<number>((res, rej) => {
      child.on('error', rej);
      child.on('close', (code) => res(code ?? -1));
    });

    const finish = (code: number, extra?: Promise<void>): void => {
      const complete = async (): Promise<void> => {
        if (extra !== undefined) {
          await extra;
        }
        if (code !== 0) {
          reject(new Error(label + ' failed: ' + stderr));
        } else {
          resolve(stderr);
        }
      };
      void complete();
    };

    if (outFile !== null) {
      const out = createWriteStream(outFile);
      const flushed = new Promise<void>((res, rej) => {
        out.on('close', () => res());
        out.on('error', rej);
      });
      child.stdout.pipe(out);
      void closed.then((code) => finish(code, flushed));
    } else {
      child.stdout.resume();
      void closed.then((code) => finish(code));
    }
  });
}

/** runNodeCapture runs a wanproxy.js CLI, capturing stdout as a string. */
function runNodeCapture(
  cli: string,
  args: string[],
  label: string,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...args], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d: Buffer) => {
      stdout += d.toString();
    });
    child.stderr.on('data', (d: Buffer) => {
      stderr += d.toString();
    });
    child.on('error', reject);
    child.on('close', (code) => {
      if ((code ?? -1) !== 0) {
        reject(new Error(label + ' failed: ' + stderr));
      } else {
        resolve(stdout);
      }
    });
  });
}

/** parseDump summarizes an xcdump -v frame listing. */
export function parseDump(dump: string): FrameSummary {
  const summary: FrameSummary = {
    data: 0,
    dataBytes: 0,
    escape: 0,
    extract: 0,
    firstFrames: [],
    ref: 0,
    topHashes: [],
  };
  const counts = new Map<string, number>();
  const order: string[] = [];
  const bump = (h: string): void => {
    if (!counts.has(h)) {
      order.push(h);
    }
    counts.set(h, (counts.get(h) ?? 0) + 1);
  };

  for (const rawLine of dump.split('\n')) {
    const line = rawLine.trim();
    if (line === '') {
      continue;
    }
    if (line.startsWith('<escape')) {
      summary.escape++;
    } else if (line.startsWith('<hash-declare')) {
      summary.extract++;
      const m = HASH_RE.exec(line);
      if (m) {
        bump(m[1]!);
      }
    } else if (line.startsWith('<hash-reference')) {
      summary.ref++;
      const m = HASH_RE.exec(line);
      if (m) {
        bump(m[1]!);
      }
    } else if (line.startsWith('<back-reference')) {
      summary.ref++;
    } else if (line.startsWith('<data')) {
      summary.data++;
      const m = DATA_RE.exec(line);
      if (m) {
        summary.dataBytes += Math.floor((m[1] ?? '').length / 2);
      }
    }
    if (summary.firstFrames.length < 24) {
      const km = KIND_RE.exec(line);
      const frame: Frame = { kind: km ? km[1]! : '?' };
      const hm = HASH_RE.exec(line);
      if (hm) {
        let h = hm[1]!;
        if (h.length > 12) {
          h = h.slice(0, 12);
        }
        frame.detail = h + '\u2026';
      }
      summary.firstFrames.push(frame);
    }
  }

  const list: HashCount[] = order.map((h) => ({ count: counts.get(h)!, hash: h }));
  list.sort((a, b) => b.count - a.count);
  summary.topHashes = list.slice(0, 8);
  return summary;
}
