import { useEffect, useMemo, useState } from "react";
import {
  ArrowDownToLine,
  CheckCircle2,
  FlaskConical,
  Play,
  RotateCcw,
  Snowflake,
  XCircle,
} from "lucide-react";
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Line,
  LineChart,
  XAxis,
  YAxis,
} from "recharts";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/components/page-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api, PATTERNS, type ExperimentReport, type Scenario } from "@/lib/api";
import { formatBytes, formatMs, formatRatio, shortSha } from "@/lib/format";

const DIRECTIONS = [
  { id: "download", label: "Download" },
  { id: "upload", label: "Upload" },
  { id: "roundtrip", label: "Roundtrip" },
] as const;

const SIZES = [
  { id: 4 << 20, label: "4 MB" },
  { id: 16 << 20, label: "16 MB" },
  { id: 64 << 20, label: "64 MB" },
  { id: 256 << 20, label: "256 MB" },
] as const;

const ITERATIONS = [1, 2, 3, 5, 8, 12];

/** Arm series colors drawn from the theme's chart palette. */
const CHART_COLORS = [
  "var(--chart-1)",
  "var(--chart-2)",
  "var(--chart-3)",
  "var(--chart-4)",
  "var(--chart-5)",
];

const WARMUP_METRICS = [
  { id: "wire", label: "Wire bytes" },
  { id: "throughput", label: "Throughput" },
  { id: "duration", label: "Duration" },
] as const;

type WarmupMetric = (typeof WARMUP_METRICS)[number]["id"];

interface Preset {
  id: string;
  label: string;
  arms: string[];
  baseline: string;
  pattern: string;
  size: number;
  iterations: number;
  direction: string;
}

const PRESETS: Preset[] = [
  {
    id: "dedup-warmup",
    label: "Dedup warm-up",
    arms: ["direct", "wan-chain"],
    baseline: "direct",
    pattern: "mixed",
    size: 16 << 20,
    iterations: 5,
    direction: "download",
  },
  {
    id: "optimizer-showdown",
    label: "Optimizer showdown",
    arms: ["direct", "zlib-chain", "xcodec-chain", "wan-chain"],
    baseline: "direct",
    pattern: "text",
    size: 16 << 20,
    iterations: 3,
    direction: "download",
  },
  {
    id: "full-matrix",
    label: "Full scenario matrix",
    arms: [
      "direct",
      "passthrough",
      "zlib-chain",
      "xcodec-chain",
      "wan-chain",
      "socks",
      "ssh-chain",
    ],
    baseline: "direct",
    pattern: "mixed",
    size: 8 << 20,
    iterations: 3,
    direction: "roundtrip",
  },
  {
    id: "honesty-check",
    label: "Incompressible honesty check",
    arms: ["direct", "wan-chain"],
    baseline: "direct",
    pattern: "random",
    size: 16 << 20,
    iterations: 2,
    direction: "download",
  },
];

const chartColor = (i: number) => CHART_COLORS[i % CHART_COLORS.length];

/** Full-row tooltip formatter so byte values render as KB/MB/GB. */
const bytesTooltipFormatter = (value: unknown, name: unknown) => (
  <div className="flex w-full flex-1 items-center justify-between gap-3 leading-none">
    <span className="text-muted-foreground">{String(name)}</span>
    <span className="text-foreground font-mono font-medium tabular-nums">
      {formatBytes(Number(value))}
    </span>
  </div>
);

const percentTooltipFormatter = (value: unknown, name: unknown) => (
  <div className="flex w-full flex-1 items-center justify-between gap-3 leading-none">
    <span className="text-muted-foreground">{String(name)}</span>
    <span className="text-foreground font-mono font-medium tabular-nums">
      {Number(value).toFixed(1)}%
    </span>
  </div>
);

export function Experiment() {
  const [scenarios, setScenarios] = useState<Scenario[]>([]);
  const [arms, setArms] = useState<string[]>(["direct", "wan-chain"]);
  const [baseline, setBaseline] = useState("direct");
  const [direction, setDirection] = useState("download");
  const [pattern, setPattern] = useState("mixed");
  const [size, setSize] = useState<number>(16 << 20);
  const [seed, setSeed] = useState(42);
  const [iterations, setIterations] = useState(5);
  const [isolateArms, setIsolateArms] = useState(true);
  const [warmupMetric, setWarmupMetric] = useState<WarmupMetric>("wire");
  const [report, setReport] = useState<ExperimentReport | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [restarting, setRestarting] = useState(false);
  const [clearCache, setClearCache] = useState(true);
  const [restartNote, setRestartNote] = useState<string | null>(null);

  useEffect(() => {
    api
      .scenarios()
      .then(setScenarios)
      .catch((e) => setError(String(e)));
  }, []);

  const applyPreset = (p: Preset) => {
    setArms(p.arms);
    setBaseline(p.baseline);
    setPattern(p.pattern);
    setSize(p.size);
    setIterations(p.iterations);
    setDirection(p.direction);
    setError(null);
  };

  const toggleArm = (id: string) => {
    setArms((prev) =>
      prev.includes(id) ? prev.filter((a) => a !== id) : [...prev, id],
    );
  };

  const run = async () => {
    setRunning(true);
    setError(null);
    setRestartNote(null);
    try {
      const res = await api.runExperiment({
        arms,
        baseline,
        direction,
        pattern,
        size,
        seed,
        iterations,
        isolateArms,
      });
      setReport(res);
    } catch (err) {
      setError(String(err));
    } finally {
      setRunning(false);
    }
  };

  const restart = async () => {
    setRestarting(true);
    setError(null);
    setRestartNote(null);
    try {
      const res = await api.restartDaemon(clearCache);
      setRestartNote(
        res.clearedCache
          ? "Fleet restarted; memory + disk caches are cold."
          : "Fleet restarted; the disk cache was kept.",
      );
    } catch (err) {
      setError(String(err));
    } finally {
      setRestarting(false);
    }
  };

  // ---- chart models (derived from the report) ----

  const armConfig = useMemo<ChartConfig>(() => {
    if (!report) return {};
    return Object.fromEntries(
      report.arms.map((arm, i) => [
        arm.scenario.id,
        { label: arm.scenario.id, color: chartColor(i) },
      ]),
    );
  }, [report]);

  const bytesConfig = useMemo<ChartConfig>(
    () => ({
      app: { label: "App bytes", color: "var(--chart-2)" },
      wire: { label: "Wire bytes", color: "var(--chart-1)" },
    }),
    [],
  );

  const savingsConfig = useMemo<ChartConfig>(
    () => ({
      wire: { label: "Wire reduction", color: "var(--chart-3)" },
      time: { label: "Time saved", color: "var(--chart-4)" },
    }),
    [],
  );

  const bytesData = useMemo(
    () =>
      report
        ? report.arms.map((a) => ({
            arm: a.scenario.id,
            app: a.total.appBytes,
            wire: a.total.wireBytes,
          }))
        : [],
    [report],
  );

  const throughputData = useMemo(
    () =>
      report
        ? report.arms.map((a) => ({
            arm: a.scenario.id,
            mbps: Number(a.total.meanThroughputMBps.toFixed(2)),
          }))
        : [],
    [report],
  );

  const savingsData = useMemo(
    () =>
      report
        ? report.comparison.map((v) => ({
            arm: v.arm,
            wire: Number((v.wireReductionPct ?? 0).toFixed(1)),
            time: Number((v.timeSavedPct ?? 0).toFixed(1)),
          }))
        : [],
    [report],
  );

  const warmupData = useMemo(() => {
    if (!report) return [];
    return Array.from({ length: report.iterations }, (_, idx) => {
      const point: Record<string, number> = { iteration: idx + 1 };
      for (const arm of report.arms) {
        const it = arm.iterations[idx];
        if (!it) continue;
        point[arm.scenario.id] =
          warmupMetric === "wire"
            ? it.wireBytes
            : warmupMetric === "throughput"
              ? Number(it.throughputMBps.toFixed(2))
              : Number(it.durationMs.toFixed(1));
      }
      return point;
    });
  }, [report, warmupMetric]);

  const warmupValueFormatter = (v: number) =>
    warmupMetric === "wire"
      ? formatBytes(v)
      : warmupMetric === "throughput"
        ? `${v.toFixed(1)} MB/s`
        : formatMs(v);

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="A/B Experiment" />

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Experiment failed</AlertTitle>
          <AlertDescription className="break-all">{error}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Experiment Design</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-5">
          <div className="flex flex-wrap gap-2">
            {PRESETS.map((p) => (
              <Button
                key={p.id}
                variant="outline"
                size="sm"
                onClick={() => applyPreset(p)}
                disabled={running}
              >
                <FlaskConical />
                {p.label}
              </Button>
            ))}
          </div>

          <FieldGroup className="flex-row flex-wrap items-end gap-4">
            <Field>
              <FieldLabel>Arms</FieldLabel>
              <div className="flex max-w-2xl flex-wrap gap-1.5">
                {scenarios.map((s) => {
                  const active = arms.includes(s.id);
                  return (
                    <Button
                      key={s.id}
                      type="button"
                      variant={active ? "default" : "outline"}
                      size="sm"
                      className="font-mono text-xs"
                      onClick={() => toggleArm(s.id)}
                      disabled={running}
                    >
                      {s.id}
                    </Button>
                  );
                })}
              </div>
            </Field>
          </FieldGroup>

          <FieldGroup className="flex-row flex-wrap items-end gap-4">
            <Field className="w-fit">
              <FieldLabel htmlFor="exp-baseline">Baseline arm</FieldLabel>
              <Select
                value={baseline}
                onValueChange={(v) => v && setBaseline(v)}
                items={Object.fromEntries(arms.map((a) => [a, a]))}
              >
                <SelectTrigger id="exp-baseline" className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {arms.map((a) => (
                      <SelectItem key={a} value={a}>
                        {a}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Field className="w-fit">
              <FieldLabel>Direction</FieldLabel>
              <ToggleGroup
                aria-label="Direction"
                value={[direction]}
                onValueChange={(v) => v[0] && setDirection(v[0])}
                variant="outline"
                spacing={0}
                disabled={running}
              >
                {DIRECTIONS.map((d) => (
                  <ToggleGroupItem key={d.id} value={d.id}>
                    {d.label}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </Field>
            <Field className="w-fit">
              <FieldLabel htmlFor="exp-pattern">Payload</FieldLabel>
              <Select
                value={pattern}
                onValueChange={(v) => setPattern(v as string)}
                items={PATTERNS}
              >
                <SelectTrigger id="exp-pattern" className="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {Object.entries(PATTERNS).map(([value, label]) => (
                      <SelectItem key={value} value={value}>
                        {label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Field className="w-fit">
              <FieldLabel htmlFor="exp-size">Size</FieldLabel>
              <Select
                value={String(size)}
                onValueChange={(v) => setSize(Number(v))}
                items={Object.fromEntries(
                  SIZES.map((s) => [String(s.id), s.label]),
                )}
              >
                <SelectTrigger id="exp-size" className="w-28">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {SIZES.map((s) => (
                      <SelectItem key={s.id} value={String(s.id)}>
                        {s.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Field className="w-fit">
              <FieldLabel htmlFor="exp-iterations">Iterations</FieldLabel>
              <Select
                value={String(iterations)}
                onValueChange={(v) => setIterations(Number(v))}
                items={Object.fromEntries(
                  ITERATIONS.map((n) => [String(n), String(n)]),
                )}
              >
                <SelectTrigger id="exp-iterations" className="w-20">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {ITERATIONS.map((n) => (
                      <SelectItem key={n} value={String(n)}>
                        {n}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Field className="w-fit">
              <FieldLabel htmlFor="exp-seed">Seed</FieldLabel>
              <Input
                id="exp-seed"
                type="number"
                className="w-24 font-mono"
                value={seed}
                disabled={running}
                onChange={(e) => setSeed(Number(e.target.value) || 0)}
              />
            </Field>
            <Field className="w-fit justify-end gap-2">
              <FieldLabel htmlFor="exp-isolate" className="text-xs">
                Isolate arm caches
              </FieldLabel>
              <input
                id="exp-isolate"
                type="checkbox"
                checked={isolateArms}
                disabled={running}
                onChange={(e) => setIsolateArms(e.target.checked)}
              />
            </Field>
            <Button onClick={run} disabled={running || arms.length === 0}>
              {running ? <Spinner /> : <Play />}
              {running
                ? `Running ${arms.length}×${iterations} transfers…`
                : "Run experiment"}
            </Button>
          </FieldGroup>
        </CardContent>
      </Card>

      {report && (
        <>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {report.comparison.map((v) => {
              const good = (v.wireReductionPct ?? 0) > 0;
              return (
                <Card key={v.arm} size="sm">
                  <CardHeader>
                    <CardTitle className="font-mono text-sm">{v.arm}</CardTitle>
                    <CardDescription>
                      vs baseline <b>{report.baseline}</b>
                    </CardDescription>
                  </CardHeader>
                  <CardContent className="flex flex-col gap-2">
                    <div className="flex items-baseline gap-2">
                      <span
                        className={cn(
                          "font-mono text-3xl font-semibold tabular-nums",
                          good ? "text-success" : "text-warning",
                        )}
                      >
                        {v.wireReductionPct === null
                          ? "-"
                          : `${v.wireReductionPct > 0 ? "-" : "+"}${Math.abs(v.wireReductionPct).toFixed(1)}%`}
                      </span>
                      <span className="text-muted-foreground text-xs">
                        wire bytes
                      </span>
                      {good && v.wireSavedBytes > 0 && (
                        <Badge variant="success">
                          {formatBytes(v.wireSavedBytes)} saved
                        </Badge>
                      )}
                    </div>
                    <div className="text-muted-foreground grid grid-cols-2 gap-x-4 gap-y-1 font-mono text-xs tabular-nums">
                      <span>
                        speedup{" "}
                        <b className="text-foreground">
                          {v.throughputSpeedup === null
                            ? "-"
                            : `${v.throughputSpeedup.toFixed(2)}×`}
                        </b>
                      </span>
                      <span>
                        time{" "}
                        <b className="text-foreground">
                          {v.timeSavedMs >= 0 ? "-" : "+"}
                          {formatMs(Math.abs(v.timeSavedMs))}
                        </b>
                      </span>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>Application vs Wire Bytes</CardTitle>
                <CardDescription>
                  Total bytes per arm — the gap between app and wire is what the
                  codec removes
                </CardDescription>
              </CardHeader>
              <CardContent>
                <ChartContainer config={bytesConfig} className="h-72 w-full">
                  <BarChart
                    layout="vertical"
                    data={bytesData}
                    margin={{ left: 8, right: 16 }}
                  >
                    <CartesianGrid horizontal={false} strokeDasharray="3 3" />
                    <XAxis
                      type="number"
                      tickLine={false}
                      axisLine={false}
                      fontSize={11}
                      tickFormatter={(v) => formatBytes(Number(v))}
                    />
                    <YAxis
                      type="category"
                      dataKey="arm"
                      width={110}
                      tickLine={false}
                      axisLine={false}
                      fontSize={11}
                    />
                    <ChartTooltip
                      content={
                        <ChartTooltipContent
                          formatter={bytesTooltipFormatter}
                        />
                      }
                    />
                    <ChartLegend content={<ChartLegendContent />} />
                    <Bar
                      dataKey="app"
                      fill="var(--color-app)"
                      radius={[0, 4, 4, 0]}
                    />
                    <Bar
                      dataKey="wire"
                      fill="var(--color-wire)"
                      radius={[0, 4, 4, 0]}
                    />
                  </BarChart>
                </ChartContainer>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Mean Throughput</CardTitle>
                <CardDescription>
                  MB/s per arm, averaged over all iterations
                </CardDescription>
              </CardHeader>
              <CardContent>
                <ChartContainer config={armConfig} className="h-72 w-full">
                  <BarChart
                    layout="vertical"
                    data={throughputData}
                    margin={{ left: 8, right: 16 }}
                  >
                    <CartesianGrid horizontal={false} strokeDasharray="3 3" />
                    <XAxis
                      type="number"
                      tickLine={false}
                      axisLine={false}
                      fontSize={11}
                    />
                    <YAxis
                      type="category"
                      dataKey="arm"
                      width={110}
                      tickLine={false}
                      axisLine={false}
                      fontSize={11}
                    />
                    <ChartTooltip content={<ChartTooltipContent />} />
                    <Bar dataKey="mbps" name="MB/s" radius={[0, 4, 4, 0]}>
                      {throughputData.map((d) => (
                        <Cell key={d.arm} fill={`var(--color-${d.arm})`} />
                      ))}
                    </Bar>
                  </BarChart>
                </ChartContainer>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>Warm-up Curve</CardTitle>
              <CardDescription>
                {PATTERNS[report.pattern] ?? report.pattern} ·{" "}
                {formatBytes(report.size)} · seed={report.seed}
                {report.isolateArms ? " (+7919 per arm)" : " (shared)"} ·{" "}
                {report.direction} — how each arm behaves across iterations
              </CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-4">
              <ToggleGroup
                aria-label="Warm-up metric"
                value={[warmupMetric]}
                onValueChange={(v) =>
                  v[0] && setWarmupMetric(v[0] as WarmupMetric)
                }
                variant="outline"
                size="sm"
                spacing={0}
                className="w-fit"
              >
                {WARMUP_METRICS.map((m) => (
                  <ToggleGroupItem key={m.id} value={m.id}>
                    {m.label}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
              <ChartContainer config={armConfig} className="h-80 w-full">
                <LineChart
                  data={warmupData}
                  margin={{ top: 8, right: 16, bottom: 8, left: 8 }}
                >
                  <CartesianGrid strokeDasharray="3 3" vertical={false} />
                  <XAxis
                    dataKey="iteration"
                    tickLine={false}
                    axisLine={false}
                    fontSize={11}
                    tickFormatter={(v) => `#${v}`}
                  />
                  <YAxis
                    tickLine={false}
                    axisLine={false}
                    fontSize={11}
                    width={72}
                    tickFormatter={(v) => warmupValueFormatter(Number(v))}
                  />
                  <ChartTooltip
                    content={
                      <ChartTooltipContent
                        labelFormatter={(label) => `Iteration #${label}`}
                        formatter={
                          warmupMetric === "wire"
                            ? bytesTooltipFormatter
                            : undefined
                        }
                      />
                    }
                  />
                  <ChartLegend content={<ChartLegendContent />} />
                  {report.arms.map((arm) => (
                    <Line
                      key={arm.scenario.id}
                      type="monotone"
                      dataKey={arm.scenario.id}
                      stroke={`var(--color-${arm.scenario.id})`}
                      strokeWidth={2}
                      dot={{ r: 3 }}
                      activeDot={{ r: 5 }}
                    />
                  ))}
                </LineChart>
              </ChartContainer>
            </CardContent>
          </Card>

          {report.comparison.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Savings vs Baseline ({report.baseline})</CardTitle>
                <CardDescription>
                  Positive bars mean the arm beats the baseline; negative bars
                  mean it trails
                </CardDescription>
              </CardHeader>
              <CardContent>
                <ChartContainer config={savingsConfig} className="h-64 w-full">
                  <BarChart
                    layout="vertical"
                    data={savingsData}
                    margin={{ left: 8, right: 16 }}
                  >
                    <CartesianGrid horizontal={false} strokeDasharray="3 3" />
                    <XAxis
                      type="number"
                      tickLine={false}
                      axisLine={false}
                      fontSize={11}
                      tickFormatter={(v) => `${v}%`}
                    />
                    <YAxis
                      type="category"
                      dataKey="arm"
                      width={110}
                      tickLine={false}
                      axisLine={false}
                      fontSize={11}
                    />
                    <ChartTooltip
                      content={
                        <ChartTooltipContent
                          formatter={percentTooltipFormatter}
                        />
                      }
                    />
                    <ChartLegend content={<ChartLegendContent />} />
                    <Bar
                      dataKey="wire"
                      name="Wire reduction"
                      fill="var(--color-wire)"
                      radius={[0, 4, 4, 0]}
                    />
                    <Bar
                      dataKey="time"
                      name="Time saved"
                      fill="var(--color-time)"
                      radius={[0, 4, 4, 0]}
                    />
                  </BarChart>
                </ChartContainer>
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle>Arm Totals</CardTitle>
              <CardDescription>
                Aggregated over {report.iterations} iteration
                {report.iterations > 1 ? "s" : ""} · experiment wall time{" "}
                {formatMs(report.durationMs)}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Arm</TableHead>
                    <TableHead className="text-right">App bytes</TableHead>
                    <TableHead className="text-right">Wire bytes</TableHead>
                    <TableHead className="text-right">Ratio</TableHead>
                    <TableHead className="text-right">Mean</TableHead>
                    <TableHead className="text-right">Median</TableHead>
                    <TableHead className="text-right">MB/s (app)</TableHead>
                    <TableHead className="text-right">Integrity</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {report.arms.map((arm) => (
                    <TableRow key={arm.scenario.id}>
                      <TableCell className="font-mono text-xs">
                        {arm.scenario.id}
                        {arm.scenario.id === report.baseline && (
                          <Badge variant="secondary" className="ml-2">
                            baseline
                          </Badge>
                        )}
                        {report.isolateArms && (
                          <span className="text-muted-foreground ml-2">
                            seed={arm.seed}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {formatBytes(arm.total.appBytes)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {formatBytes(arm.total.wireBytes)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {formatRatio(arm.total.ratio)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {formatMs(arm.total.meanDurationMs)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {formatMs(arm.total.medianDurationMs)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs tabular-nums">
                        {arm.total.meanThroughputMBps.toFixed(1)}
                      </TableCell>
                      <TableCell className="text-right">
                        <Badge
                          variant={
                            arm.total.okCount === report.iterations
                              ? "success"
                              : "destructive"
                          }
                        >
                          {arm.total.okCount}/{report.iterations}
                        </Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Iteration Details</CardTitle>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Arm</TableHead>
                    <TableHead className="text-right">#</TableHead>
                    <TableHead className="text-right">Duration</TableHead>
                    <TableHead className="text-right">MB/s</TableHead>
                    <TableHead className="text-right">Wire</TableHead>
                    <TableHead className="text-right">Ratio</TableHead>
                    <TableHead>SHA-256</TableHead>
                    <TableHead className="text-right">Status</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {report.arms.flatMap((arm) =>
                    arm.iterations.map((it) => (
                      <TableRow key={`${arm.scenario.id}-${it.iteration}`}>
                        <TableCell className="font-mono text-xs">
                          {arm.scenario.id}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {it.iteration}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {formatMs(it.durationMs)}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {it.throughputMBps.toFixed(1)}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {formatBytes(it.wireBytes)}
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums">
                          {formatRatio(it.ratio)}
                        </TableCell>
                        <TableCell className="font-mono text-xs">
                          {it.sha256 ? shortSha(it.sha256) : "-"}
                        </TableCell>
                        <TableCell className="text-right">
                          {it.ok ? (
                            <Badge variant="success">
                              <CheckCircle2 /> verified
                            </Badge>
                          ) : (
                            <Badge variant="destructive" title={it.error}>
                              <XCircle /> failed
                            </Badge>
                          )}
                        </TableCell>
                      </TableRow>
                    )),
                  )}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </>
      )}

      <Card>
        <CardHeader>
          <CardTitle>WAN Cache Control</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center gap-3">
          <Button
            variant="outline"
            onClick={restart}
            disabled={restarting || running}
          >
            {restarting ? (
              <Spinner />
            ) : clearCache ? (
              <Snowflake />
            ) : (
              <RotateCcw />
            )}
            {restarting
              ? "Restarting fleet…"
              : clearCache
                ? "Cold restart (clear disk cache)"
                : "Restart wanproxy fleet"}
          </Button>
          <label className="text-muted-foreground flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={clearCache}
              onChange={(e) => setClearCache(e.target.checked)}
              disabled={restarting || running}
            />
            clear XCDFS disk cache on restart
          </label>
          {restartNote && (
            <Badge variant="success">
              <ArrowDownToLine /> {restartNote}
            </Badge>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
