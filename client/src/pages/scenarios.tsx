import { useEffect, useState } from "react";
import { CheckCircle2, Play, XCircle } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { RatioBar } from "@/components/ratio-bar";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
import { Separator } from "@/components/ui/separator";
import { Spinner } from "@/components/ui/spinner";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api, type BenchResult, type Scenario } from "@/lib/api";
import { formatBytes, formatMs, formatRatio } from "@/lib/format";

const DIRECTIONS = [
  { id: "roundtrip", label: "Roundtrip" },
  { id: "upload", label: "Upload" },
  { id: "download", label: "Download" },
] as const;

const PATTERNS: Record<string, string> = {
  mixed: "mixed (VM image)",
  repeat: "repeat (max duplication)",
  text: "text (log stream)",
  random: "random (incompressible)",
};

const SIZES = [
  { id: 4 << 20, label: "4 MB" },
  { id: 16 << 20, label: "16 MB" },
  { id: 64 << 20, label: "64 MB" },
  { id: 256 << 20, label: "256 MB" },
] as const;

const KIND_VARIANT: Record<Scenario["kind"], "info" | "violet" | "success"> = {
  http: "info",
  socks: "violet",
  ssh: "success",
};

export function Scenarios() {
  const [scenarios, setScenarios] = useState<Scenario[]>([]);
  const [direction, setDirection] = useState<string>("roundtrip");
  const [pattern, setPattern] = useState<string>("mixed");
  const [size, setSize] = useState<number>(16 << 20);
  const [seed, setSeed] = useState(7);
  const [results, setResults] = useState<BenchResult[]>([]);
  const [running, setRunning] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .scenarios()
      .then(setScenarios)
      .catch((e) => setError(String(e)));
  }, []);

  const run = async (id: string) => {
    setRunning(id);
    setError(null);
    try {
      const res = await api.runBench({
        scenario: id,
        direction,
        pattern,
        size,
        seed,
      });
      setResults((prev) =>
        [
          res,
          ...prev.filter(
            (r) => r.scenario.id !== id || r.direction !== direction,
          ),
        ].slice(0, 30),
      );
      if (!res.ok) setError(`${id}: ${res.error}`);
    } catch (err) {
      setError(String(err));
    } finally {
      setRunning(null);
    }
  };

  const runAll = async () => {
    for (const s of scenarios) {
      await run(s.id);
    }
  };

  const maxWire = Math.max(1, ...results.map((r) => r.stats?.wireBytes ?? 0));

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Protocol Benchmarks" />

      <Card>
        <CardHeader>
          <CardTitle>Run Parameters</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <FieldGroup className="flex-row flex-wrap items-end gap-4">
            <Field className="w-fit">
              <FieldLabel>Direction</FieldLabel>
              <ToggleGroup
                aria-label="Direction"
                value={[direction]}
                onValueChange={(v) => v[0] && setDirection(v[0])}
                variant="outline"
                spacing={0}
              >
                {DIRECTIONS.map((d) => (
                  <ToggleGroupItem key={d.id} value={d.id}>
                    {d.label}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </Field>
            <Field className="w-fit">
              <FieldLabel htmlFor="bench-pattern">Payload pattern</FieldLabel>
              <Select
                value={pattern}
                onValueChange={(value) => setPattern(value as string)}
                items={PATTERNS}
              >
                <SelectTrigger id="bench-pattern" className="w-56">
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
              <FieldLabel>Size</FieldLabel>
              <ToggleGroup
                aria-label="Size"
                value={[String(size)]}
                onValueChange={(v) => v[0] && setSize(Number(v[0]))}
                variant="outline"
                spacing={0}
              >
                {SIZES.map((s) => (
                  <ToggleGroupItem key={s.id} value={String(s.id)}>
                    {s.label}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </Field>
            <Field className="w-fit">
              <FieldLabel htmlFor="bench-seed">Seed</FieldLabel>
              <Input
                id="bench-seed"
                type="number"
                value={seed}
                onChange={(e) => setSeed(Number(e.target.value))}
                className="w-24 font-mono"
              />
            </Field>
            <Button
              onClick={runAll}
              disabled={running !== null || scenarios.length === 0}
              className="ml-auto"
            >
              {running ? (
                <>
                  <Spinner data-icon="inline-start" />
                  Running…
                </>
              ) : (
                <>
                  <Play data-icon="inline-start" />
                  Run All
                </>
              )}
            </Button>
          </FieldGroup>
        </CardContent>
      </Card>

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Benchmark failed</AlertTitle>
          <AlertDescription className="font-mono text-xs">
            {error}
          </AlertDescription>
        </Alert>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {scenarios.map((s) => {
          const r = results.find(
            (x) => x.scenario.id === s.id && x.direction === direction,
          );
          return (
            <Card key={s.id}>
              <CardHeader>
                <CardTitle>{s.label}</CardTitle>
                <CardAction>
                  <Button
                    size="sm"
                    variant={r ? "outline" : "default"}
                    onClick={() => run(s.id)}
                    disabled={running !== null}
                  >
                    {running === s.id ? (
                      <Spinner data-icon="inline-start" />
                    ) : (
                      <Play data-icon="inline-start" />
                    )}
                    Run
                  </Button>
                </CardAction>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="flex flex-wrap gap-1.5">
                  <Badge variant="secondary">{s.protocol}</Badge>
                  <Badge variant={KIND_VARIANT[s.kind]}>{s.kind}</Badge>
                  {s.base && (
                    <Badge variant="outline" className="font-mono">
                      :{new URL(s.base).port}
                    </Badge>
                  )}
                  {s.proxy && (
                    <Badge variant="outline" className="font-mono">
                      {s.proxy}
                    </Badge>
                  )}
                </div>

                {r && (
                  <>
                    <Separator />
                    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-xs">
                      <span
                        className={
                          r.ok
                            ? "text-success flex items-center gap-1"
                            : "text-destructive flex items-center gap-1"
                        }
                      >
                        {r.ok ? (
                          <>
                            <CheckCircle2 className="size-3.5" /> checksum
                            verified
                          </>
                        ) : (
                          <>
                            <XCircle className="size-3.5" /> {r.error}
                          </>
                        )}
                      </span>
                      <span className="text-foreground tabular-nums">
                        {formatMs(r.durationMs)}
                      </span>
                      <span className="text-muted-foreground tabular-nums">
                        {r.throughputMBps.toFixed(1)} MB/s (app layer)
                      </span>
                    </div>
                    {r.stats && (
                      <div className="flex flex-col gap-1.5">
                        <div className="flex items-center justify-between gap-2 text-xs">
                          <span className="text-muted-foreground">
                            wire{" "}
                            <b className="text-chart-1 font-mono">
                              {formatBytes(r.stats.wireBytes)}
                            </b>{" "}
                            ÷ app{" "}
                            <b className="text-foreground font-mono">
                              {formatBytes(r.stats.appBytes)}
                            </b>
                          </span>
                          <Badge
                            variant={
                              r.stats.ratio !== null && r.stats.ratio < 0.5
                                ? "success"
                                : "warning"
                            }
                          >
                            ratio {formatRatio(r.stats.ratio)}
                          </Badge>
                        </div>
                        <RatioBar
                          appBytes={r.stats.appBytes}
                          wireBytes={r.stats.wireBytes}
                        />
                      </div>
                    )}
                  </>
                )}
              </CardContent>
            </Card>
          );
        })}
      </div>

      {results.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Wire Bytes Comparison</CardTitle>
            <CardDescription>
              Current parameters: {direction} · {PATTERNS[pattern]} ·{" "}
              {formatBytes(size)} · seed={seed}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {[...results]
              .filter((r) => r.stats && r.direction === direction)
              .sort(
                (a, b) => (b.stats?.wireBytes ?? 0) - (a.stats?.wireBytes ?? 0),
              )
              .map((r) => (
                <div
                  key={r.scenario.id + r.direction}
                  className="flex items-center gap-3 text-xs"
                >
                  <span className="w-40 shrink-0 truncate font-mono">
                    {r.scenario.id}
                  </span>
                  <div className="bg-muted h-3.5 flex-1 overflow-hidden rounded-full">
                    <div
                      className="from-chart-1 to-chart-2 h-full rounded-full bg-linear-to-r transition-all duration-300"
                      style={{
                        width: `${Math.max(((r.stats?.wireBytes ?? 0) / maxWire) * 100, 1)}%`,
                      }}
                    />
                  </div>
                  <span className="text-muted-foreground w-24 shrink-0 text-right font-mono tabular-nums">
                    {formatBytes(r.stats?.wireBytes ?? 0)}
                  </span>
                  <span className="text-success w-16 shrink-0 text-right font-mono tabular-nums">
                    {formatRatio(r.stats?.ratio ?? null)}
                  </span>
                </div>
              ))}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
