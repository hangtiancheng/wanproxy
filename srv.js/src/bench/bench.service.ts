/**
 * BenchService guards against concurrent runs, resolves a scenario, samples
 * the monitor codec counters before/after, and executes the benchmark
 * in-process.  It is the TypeScript port of srv/internal/benchsvc.
 */

import { Injectable } from '@nestjs/common';

import { parsePattern, type Pattern } from '../common/payload.js';
import {
  codecStats,
  type CodecStats,
  MonitorService,
} from '../monitor/monitor.service.js';
import { findScenario, type Scenario } from '../monitor/scenarios.js';
import { runBench } from './bench.runner.js';

const DEFAULT_SIZE = 16 * 1024 * 1024;
const MAX_ARMS = 8;
const MAX_ITERATIONS = 20;
const DEFAULT_ITERATIONS = 3;
const MAX_EXPERIMENT_SIZE = 1 << 30;
const MAX_EXPERIMENT_SEED = 2 ** 31 - 1;

/** UnknownScenarioError signals an unrecognized scenario id (HTTP 404). */
export class UnknownScenarioError extends Error {
  public constructor(public readonly id: string) {
    super('unknown scenario ' + id);
    this.name = 'UnknownScenarioError';
  }
}

/** AlreadyRunningError signals an in-flight benchmark (HTTP 409). */
export class AlreadyRunningError extends Error {
  public constructor() {
    super('a benchmark is already running');
    this.name = 'AlreadyRunningError';
  }
}

/** BenchRequest is the POST /api/bench/run body. */
export interface BenchRequest {
  scenario: string;
  direction?: string;
  pattern?: string;
  size?: number;
  seed?: number;
}

/** BenchStats carries the monitor counter deltas for the run. */
export interface BenchStats {
  proxy: string;
  codec: string;
  appBytes: number;
  wireBytes: number;
  ratio: number | null;
  raw: CodecStats;
}

/** BenchResult is the POST /api/bench/run response. */
export interface BenchResult {
  scenario: Scenario;
  direction: string;
  pattern: string;
  size: number;
  seed: number;
  durationMs: number;
  throughputMBps: number;
  sha256?: string;
  ok: boolean;
  error?: string;
  stats?: BenchStats;
}

/** ExperimentRequest is the POST /api/bench/experiment body. */
export interface ExperimentRequest {
  arms: string[];
  baseline?: string;
  direction?: string;
  pattern?: string;
  size?: number;
  seed?: number;
  iterations?: number;
  /**
   * isolateArms gives every arm its own payload seed (seed + armIndex*7919)
   * so arms cannot warm each other's dedup cache through the fleet-wide
   * shared cache; defaults to true.  Set it to false to reproduce the
   * real-world effect of one shared cache serving several chains (arm order
   * then matters).
   */
  isolateArms?: boolean;
}

/** IterationResult is one transfer inside an experiment arm. */
export interface IterationResult {
  iteration: number;
  durationMs: number;
  throughputMBps: number;
  appBytes: number;
  wireBytes: number;
  ratio: number | null;
  sha256?: string;
  ok: boolean;
  error?: string;
}

/** ArmTotal aggregates one arm's iterations. */
export interface ArmTotal {
  appBytes: number;
  wireBytes: number;
  ratio: number | null;
  meanDurationMs: number;
  medianDurationMs: number;
  totalDurationMs: number;
  meanThroughputMBps: number;
  okCount: number;
}

/** ArmReport is one experiment arm. */
export interface ArmReport {
  scenario: Scenario;
  hasStats: boolean;
  seed: number;
  iterations: IterationResult[];
  total: ArmTotal;
}

/** ComparisonVerdict compares one arm against the baseline arm. */
export interface ComparisonVerdict {
  arm: string;
  wireSavedBytes: number;
  wireReductionPct: number | null;
  throughputSpeedup: number | null;
  timeSavedMs: number;
  timeSavedPct: number | null;
}

/** ExperimentReport is the POST /api/bench/experiment response. */
export interface ExperimentReport {
  pattern: string;
  size: number;
  seed: number;
  direction: string;
  iterations: number;
  baseline: string;
  isolateArms: boolean;
  arms: ArmReport[];
  comparison: ComparisonVerdict[];
  startedAt: string;
  durationMs: number;
}

@Injectable()
export class BenchService {
  private running = false;

  public constructor(private readonly monitor: MonitorService) {}

  /** busy reports whether a benchmark or experiment is in flight. */
  public busy(): boolean {
    return this.running;
  }

  /** run executes one benchmark. */
  public async run(req: BenchRequest): Promise<BenchResult> {
    const scenario = findScenario(req.scenario);
    if (scenario === undefined) {
      throw new UnknownScenarioError(req.scenario);
    }
    const direction = req.direction || 'roundtrip';
    const patternStr = req.pattern || 'mixed';
    const size = req.size ?? DEFAULT_SIZE;
    const seed = req.seed ?? 42;
    const pattern = parsePattern(patternStr);
    if (!this.begin()) {
      throw new AlreadyRunningError();
    }
    try {
      return await this.runOnce(scenario, direction, patternStr, pattern, size, seed);
    } finally {
      this.end();
    }
  }

  /** runExperiment executes a full A/B experiment. */
  public async runExperiment(req: ExperimentRequest): Promise<ExperimentReport> {
    if (!req.arms || req.arms.length === 0) {
      throw new Error('experiment requires at least one arm');
    }
    if (req.arms.length > MAX_ARMS) {
      throw new Error('at most ' + MAX_ARMS + ' arms per experiment');
    }
    const direction = req.direction || 'download';
    if (!['upload', 'download', 'roundtrip'].includes(direction)) {
      throw new Error(
        'unknown direction "' + direction + '" (want upload|download|roundtrip)',
      );
    }
    const patternStr = req.pattern || 'mixed';
    const pattern = parsePattern(patternStr);
    const size = req.size ?? DEFAULT_SIZE;
    if (size < 1 || size > MAX_EXPERIMENT_SIZE) {
      throw new Error('size ' + size + ' out of range 1..' + MAX_EXPERIMENT_SIZE);
    }
    const seed = req.seed ?? 42;
    if (seed < 0 || seed > MAX_EXPERIMENT_SEED) {
      throw new Error('seed ' + seed + ' out of range');
    }
    const iterations = req.iterations ?? DEFAULT_ITERATIONS;
    if (iterations < 1 || iterations > MAX_ITERATIONS) {
      throw new Error('iterations ' + iterations + ' out of range 1..' + MAX_ITERATIONS);
    }
    const isolateArms = req.isolateArms ?? true;

    const scenarios: Scenario[] = [];
    for (const id of req.arms) {
      const scenario = findScenario(id);
      if (scenario === undefined) {
        throw new UnknownScenarioError(id);
      }
      scenarios.push(scenario);
    }

    let baseline = req.arms[0]!;
    if (req.baseline) {
      baseline = req.baseline;
      if (!req.arms.includes(baseline)) {
        throw new Error(
          'baseline "' + baseline + '" must be one of the experiment arms',
        );
      }
    }

    if (!this.begin()) {
      throw new AlreadyRunningError();
    }
    try {
      const startedAt = Date.now();
      const startHr = process.hrtime.bigint();
      const report: ExperimentReport = {
        arms: [],
        baseline,
        comparison: [],
        direction,
        durationMs: 0,
        iterations,
        isolateArms,
        pattern: patternStr,
        seed,
        size,
        startedAt: new Date(startedAt).toISOString(),
      };
      const multiplier = direction === 'roundtrip' ? 2 : 1;
      for (let i = 0; i < scenarios.length; i++) {
        const scenario = scenarios[i]!;
        // A distinct payload per arm keeps the shared fleet cache from
        // leaking warm state across arms.
        const armSeed = isolateArms ? seed + i * 7919 : seed;
        const arm: ArmReport = {
          hasStats: !!scenario.statsProxy && !!scenario.statsCodec,
          iterations: [],
          scenario,
          seed: armSeed,
          total: emptyTotal(),
        };
        for (let iter = 1; iter <= iterations; iter++) {
          const res = await this.runOnce(
            scenario,
            direction,
            patternStr,
            pattern,
            size,
            armSeed,
          );
          const it: IterationResult = {
            appBytes: 0,
            durationMs: res.durationMs,
            iteration: iter,
            ok: res.ok,
            ratio: null,
            throughputMBps: res.throughputMBps,
            wireBytes: 0,
          };
          if (res.sha256 !== undefined) {
            it.sha256 = res.sha256;
          }
          if (res.error !== undefined) {
            it.error = res.error;
          }
          if (res.stats !== undefined) {
            it.appBytes = res.stats.appBytes;
            it.wireBytes = res.stats.wireBytes;
            it.ratio = res.stats.ratio;
          } else {
            const app = size * multiplier;
            it.appBytes = app;
            it.wireBytes = app;
            it.ratio = 1;
          }
          arm.iterations.push(it);
        }
        arm.total = aggregateArm(arm.iterations);
        report.arms.push(arm);
      }
      report.comparison = compareArms(report.arms, baseline);
      report.durationMs = Number(process.hrtime.bigint() - startHr) / 1e6;
      return report;
    } finally {
      this.end();
    }
  }

  private begin(): boolean {
    if (this.running) {
      return false;
    }
    this.running = true;
    return true;
  }

  private end(): void {
    this.running = false;
  }

  private async runOnce(
    scenario: Scenario,
    direction: string,
    patternStr: string,
    pattern: Pattern,
    size: number,
    seed: number,
  ): Promise<BenchResult> {
    const trackStats = !!scenario.statsProxy && !!scenario.statsCodec;
    let before: CodecStats | undefined;
    if (trackStats) {
      before = await this.sampleStats(scenario);
    }

    const run = await runBench(
      scenario.kind,
      scenario.base,
      scenario.proxy,
      scenario.target,
      direction,
      pattern,
      size,
      seed,
    );

    let after: CodecStats | undefined;
    if (trackStats && before !== undefined) {
      after = await this.sampleStats(scenario);
    }

    const result: BenchResult = {
      direction,
      durationMs: run.durationMs,
      ok: run.ok,
      pattern: patternStr,
      scenario,
      seed,
      size,
      throughputMBps: run.throughputMBps,
    };
    if (run.sha256 !== undefined) {
      result.sha256 = run.sha256;
    }
    if (run.error !== undefined) {
      result.error = run.error;
    }

    if (before !== undefined && after !== undefined) {
      const raw: CodecStats = {
        codecToIncomingBytes: after.codecToIncomingBytes - before.codecToIncomingBytes,
        codecToOutgoingBytes: after.codecToOutgoingBytes - before.codecToOutgoingBytes,
        incomingToCodecBytes: after.incomingToCodecBytes - before.incomingToCodecBytes,
        outgoingToCodecBytes: after.outgoingToCodecBytes - before.outgoingToCodecBytes,
      };
      // Codecs without `track_statistics` (or a monitor that could not be
      // sampled) read as an all-zero delta.  Treat that as "not tracked"
      // and omit stats, letting the experiment fall back to the size
      // estimate rather than reporting a misleading 100% wire reduction.
      const tracked =
        raw.incomingToCodecBytes !== 0 ||
        raw.codecToOutgoingBytes !== 0 ||
        raw.outgoingToCodecBytes !== 0 ||
        raw.codecToIncomingBytes !== 0;
      if (tracked) {
      let appBytes = 0;
      let wireBytes = 0;
      if (direction === 'upload' || direction === 'roundtrip') {
        appBytes += raw.incomingToCodecBytes;
        wireBytes += raw.codecToOutgoingBytes;
      }
      if (direction === 'download' || direction === 'roundtrip') {
        appBytes += raw.codecToIncomingBytes;
        wireBytes += raw.outgoingToCodecBytes;
      }
      result.stats = {
        appBytes,
        codec: scenario.statsCodec!,
        proxy: scenario.statsProxy!,
        ratio: appBytes > 0 ? wireBytes / appBytes : null,
        raw,
        wireBytes,
      };
      }
    }
    return result;
  }

  private async sampleStats(scenario: Scenario): Promise<CodecStats | undefined> {
    try {
      const fs = await this.monitor.status();
      return codecStats(fs, scenario.statsProxy!, scenario.statsCodec!);
    } catch {
      return undefined;
    }
  }
}

function emptyTotal(): ArmTotal {
  return {
    appBytes: 0,
    meanDurationMs: 0,
    meanThroughputMBps: 0,
    medianDurationMs: 0,
    okCount: 0,
    ratio: null,
    totalDurationMs: 0,
    wireBytes: 0,
  };
}

function aggregateArm(iters: IterationResult[]): ArmTotal {
  const total = emptyTotal();
  if (iters.length === 0) {
    return total;
  }
  const durations: number[] = [];
  let throughputSum = 0;
  for (const it of iters) {
    total.appBytes += it.appBytes;
    total.wireBytes += it.wireBytes;
    total.totalDurationMs += it.durationMs;
    throughputSum += it.throughputMBps;
    if (it.ok) {
      total.okCount++;
    }
    durations.push(it.durationMs);
  }
  const n = iters.length;
  total.meanDurationMs = total.totalDurationMs / n;
  total.meanThroughputMBps = throughputSum / n;
  durations.sort((a, b) => a - b);
  const mid = Math.floor(durations.length / 2);
  total.medianDurationMs =
    durations.length % 2 === 1
      ? durations[mid]!
      : (durations[mid - 1]! + durations[mid]!) / 2;
  if (total.appBytes > 0) {
    total.ratio = total.wireBytes / total.appBytes;
  }
  return total;
}

function compareArms(arms: ArmReport[], baselineId: string): ComparisonVerdict[] {
  const baseline = arms.find((a) => a.scenario.id === baselineId);
  if (baseline === undefined) {
    return [];
  }
  const verdicts: ComparisonVerdict[] = [];
  for (const arm of arms) {
    if (arm.scenario.id === baselineId) {
      continue;
    }
    const v: ComparisonVerdict = {
      arm: arm.scenario.id,
      throughputSpeedup: null,
      timeSavedMs: baseline.total.totalDurationMs - arm.total.totalDurationMs,
      timeSavedPct: null,
      wireReductionPct: null,
      wireSavedBytes: baseline.total.wireBytes - arm.total.wireBytes,
    };
    if (baseline.total.wireBytes > 0) {
      v.wireReductionPct =
        (100 * (baseline.total.wireBytes - arm.total.wireBytes)) /
        baseline.total.wireBytes;
    }
    if (arm.total.meanThroughputMBps > 0 && baseline.total.meanThroughputMBps > 0) {
      v.throughputSpeedup =
        arm.total.meanThroughputMBps / baseline.total.meanThroughputMBps;
    }
    if (baseline.total.totalDurationMs > 0) {
      v.timeSavedPct =
        (100 * (baseline.total.totalDurationMs - arm.total.totalDurationMs)) /
        baseline.total.totalDurationMs;
    }
    verdicts.push(v);
  }
  return verdicts;
}
