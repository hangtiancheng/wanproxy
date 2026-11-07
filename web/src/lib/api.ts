const SRV = "http://127.0.0.1:3101";

export interface CodecStats {
  incomingToCodecBytes: number;
  codecToOutgoingBytes: number;
  outgoingToCodecBytes: number;
  codecToIncomingBytes: number;
}

export interface ProxyStatus {
  kind: string;
  name: string;
  listening: boolean;
  acceptedConnections: number;
  activeSockets: number;
  address?: { address: string; port: number };
  listen?: { host?: string; port?: string };
  upstream?: { host?: string; port?: string };
  interfaceCodec?: { mode: string; stats?: CodecStats } | null;
  peerCodec?: { mode: string; stats?: CodecStats } | null;
}

export interface FleetStatus {
  generatedAt: string;
  proxies: ProxyStatus[];
}

export interface Scenario {
  id: string;
  label: string;
  description: string;
  kind: "http" | "socks" | "ssh";
  base?: string;
  proxy?: string;
  target?: string;
  statsProxy?: string;
  statsCodec?: string;
  protocol: string;
}

export interface FileMeta {
  id: string;
  name: string;
  size: number;
  sha256: string;
  pattern?: string;
  seed?: number;
  createdAt: string;
}

export interface BenchStats {
  proxy: string;
  codec: string;
  appBytes: number;
  wireBytes: number;
  ratio: number | null;
  raw: CodecStats;
}

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

export interface ArmReport {
  scenario: Scenario;
  hasStats: boolean;
  seed: number;
  iterations: IterationResult[];
  total: ArmTotal;
}

export interface ComparisonVerdict {
  arm: string;
  wireSavedBytes: number;
  wireReductionPct: number | null;
  throughputSpeedup: number | null;
  timeSavedMs: number;
  timeSavedPct: number | null;
}

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

export interface AnalyzeResult {
  file: { id: string; name: string; size: number };
  inband: { compressedSize: number; ratio: number; stats: string };
  dictionary: { firstPassSize: number; secondPassSize: number; ratio: number };
  frames: {
    extract: number;
    ref: number;
    escape: number;
    data: number;
    dataBytes: number;
    topHashes: { hash: string; count: number }[];
    firstFrames: { kind: string; detail?: string }[];
  };
}

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`HTTP ${res.status}: ${text.slice(0, 200)}`);
  }
  return res.json() as Promise<T>;
}

export const api = {
  monitorStatus: () =>
    fetch(`${SRV}/api/monitor/status`).then((r) => json<FleetStatus>(r)),
  scenarios: () =>
    fetch(`${SRV}/api/monitor/scenarios`).then((r) => json<Scenario[]>(r)),
  files: () => fetch(`${SRV}/api/files`).then((r) => json<FileMeta[]>(r)),
  generateFile: (name: string, size: number, pattern: string, seed: number) =>
    fetch(`${SRV}/api/files/generate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, size, pattern, seed }),
    }).then((r) => json<FileMeta>(r)),
  deleteFile: (id: string) =>
    fetch(`${SRV}/api/files/${id}`, { method: "DELETE" }),
  fileUrl: (id: string) => `${SRV}/api/files/${id}/content`,
  fileUrlVia: (id: string, base: string) => `${base}/api/files/${id}/content`,
  uploadUrl: (base: string, name: string) =>
    `${base}/api/files/upload?name=${encodeURIComponent(name)}`,
  runBench: (body: {
    scenario: string;
    direction: string;
    pattern: string;
    size: number;
    seed: number;
  }) =>
    fetch(`${SRV}/api/bench/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then((r) => json<BenchResult>(r)),
  analyze: (fileId: string) =>
    fetch(`${SRV}/api/tools/analyze`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ fileId }),
    }).then((r) => json<AnalyzeResult>(r)),
  runExperiment: (body: {
    arms: string[];
    baseline?: string;
    direction?: string;
    pattern?: string;
    size?: number;
    seed?: number;
    iterations?: number;
    isolateArms?: boolean;
  }) =>
    fetch(`${SRV}/api/bench/experiment`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then((r) => json<ExperimentReport>(r)),
  restartDaemon: (clearCache: boolean) =>
    fetch(`${SRV}/api/daemon/restart`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ clearCache }),
    }).then((r) => json<{ ok: boolean; clearedCache: boolean }>(r)),
};

export const PATTERNS: Record<string, string> = {
  mixed: "mixed (VM image)",
  repeat: "repeat (max duplication)",
  text: "text (log stream)",
  random: "random (incompressible)",
};

export const UPLOAD_ROUTES: { id: string; label: string; base: string }[] = [
  { id: "direct", label: "Direct to SRV", base: SRV },
  {
    id: "passthrough",
    label: "TCP passthrough proxy",
    base: "http://127.0.0.1:8310",
  },
  {
    id: "zlib-chain",
    label: "zlib compression chain",
    base: "http://127.0.0.1:8311",
  },
  {
    id: "xcodec-chain",
    label: "XCodec dedup chain",
    base: "http://127.0.0.1:8312",
  },
  {
    id: "wan-chain",
    label: "WAN optimization chain (XCodec+zlib)",
    base: "http://127.0.0.1:8320",
  },
];
