export function formatBytes(n: number): string {
  if (!Number.isFinite(n)) return "-";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export function formatMs(ms: number): string {
  if (ms < 1000) return `${ms.toFixed(0)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

export function formatRatio(ratio: number | null): string {
  if (ratio === null || !Number.isFinite(ratio)) return "-";
  if (ratio === 0) return "0";
  if (ratio >= 1) return `${ratio.toFixed(2)}×`;
  return `1/${(1 / ratio).toFixed(1)}`;
}

export function shortSha(sha: string): string {
  return `${sha.slice(0, 12)}…`;
}
