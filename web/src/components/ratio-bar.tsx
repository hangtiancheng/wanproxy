import { formatBytes } from "@/lib/format";

export function RatioBar({
  appBytes,
  wireBytes,
}: {
  appBytes: number;
  wireBytes: number;
}) {
  const pct = appBytes > 0 ? Math.min(100, (wireBytes / appBytes) * 100) : 0;
  return (
    <div className="flex flex-col gap-1">
      <div className="bg-muted relative h-2.5 w-full overflow-hidden rounded-full">
        <div
          className="from-chart-1 to-chart-2 absolute inset-y-0 left-0 rounded-full bg-linear-to-r transition-all duration-300"
          style={{ width: `${Math.max(pct, pct > 0 ? 1.5 : 0)}%` }}
        />
      </div>
      <div className="text-muted-foreground flex justify-between font-mono text-[11px]">
        <span>
          wire{" "}
          <b className="text-chart-1 font-medium">{formatBytes(wireBytes)}</b>
        </span>
        <span>app layer {formatBytes(appBytes)}</span>
      </div>
    </div>
  );
}
