import { useEffect, useState } from "react";
import { Binary, ScanSearch } from "lucide-react";
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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { api, type AnalyzeResult, type FileMeta } from "@/lib/api";
import { formatBytes, formatRatio } from "@/lib/format";
import { cn } from "@/lib/utils";

const FRAME_COLORS: Record<string, string> = {
  "hash-declare": "bg-chart-1",
  "hash-reference": "bg-chart-3",
  escape: "bg-chart-4",
  data: "bg-muted-foreground/40",
  "back-reference": "bg-chart-5",
};

export function Inspector() {
  const [files, setFiles] = useState<FileMeta[]>([]);
  const [fileId, setFileId] = useState<string>("");
  const [result, setResult] = useState<AnalyzeResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .files()
      .then((fs) => {
        setFiles(fs);
        if (fs.length > 0) setFileId(fs[0]!.id);
      })
      .catch((e) => setError(String(e)));
  }, []);

  const analyze = async () => {
    if (!fileId) return;
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setResult(await api.analyze(fileId));
    } catch (err) {
      setError(String(err));
    } finally {
      setBusy(false);
    }
  };

  const totalFrames = result
    ? result.frames.extract +
      result.frames.ref +
      result.frames.escape +
      result.frames.data
    : 0;

  const fileItems: Record<string, string> = Object.fromEntries(
    files.map((f) => [f.id, `${f.name} (${formatBytes(f.size)})`]),
  );

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="XCodec Stream Inspector" />

      <Card>
        <CardContent>
          <FieldGroup className="flex-row flex-wrap items-end gap-3">
            <Field className="w-fit min-w-72">
              <FieldLabel htmlFor="inspect-file">File</FieldLabel>
              <Select
                value={fileId}
                onValueChange={(value) => setFileId(value as string)}
                items={fileItems}
                disabled={files.length === 0}
              >
                <SelectTrigger id="inspect-file" className="w-full">
                  <SelectValue placeholder="Select a file" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {files.map((f) => (
                      <SelectItem key={f.id} value={f.id}>
                        {f.name} ({formatBytes(f.size)})
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Button onClick={analyze} disabled={busy || !fileId}>
              {busy ? (
                <>
                  <Spinner data-icon="inline-start" />
                  Analyzing (tack × 3 + xcdump)…
                </>
              ) : (
                <>
                  <ScanSearch data-icon="inline-start" />
                  Analyze
                </>
              )}
            </Button>
          </FieldGroup>
        </CardContent>
      </Card>

      {error && (
        <Alert variant="destructive">
          <Binary />
          <AlertTitle>Analysis failed</AlertTitle>
          <AlertDescription className="font-mono text-xs">
            {error}
          </AlertDescription>
        </Alert>
      )}

      {result && (
        <>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <Card>
              <CardHeader>
                <CardTitle>In-band Compression</CardTitle>
                <CardDescription className="font-mono">tack -c</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <Big
                  label="Compressed"
                  value={formatBytes(result.inband.compressedSize)}
                />
                <div className="text-muted-foreground flex items-center justify-between text-xs">
                  <span>Original {formatBytes(result.file.size)}</span>
                  <Badge
                    variant={result.inband.ratio < 0.5 ? "success" : "warning"}
                  >
                    {formatRatio(result.inband.ratio)}
                  </Badge>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Dictionary Mode</CardTitle>
                <CardDescription className="font-mono">tack -p</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <Big
                  label="Second pass"
                  value={formatBytes(result.dictionary.secondPassSize)}
                />
                <div className="text-muted-foreground flex items-center justify-between text-xs">
                  <span>
                    First pass {formatBytes(result.dictionary.firstPassSize)}
                  </span>
                  <Badge
                    variant={
                      result.dictionary.ratio < 0.1 ? "success" : "warning"
                    }
                  >
                    {formatRatio(result.dictionary.ratio)}
                  </Badge>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Frame Composition</CardTitle>
                <CardDescription className="font-mono">xcdump</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="flex h-4 w-full overflow-hidden rounded-md">
                  {(
                    [
                      "hash-declare",
                      "hash-reference",
                      "escape",
                      "data",
                    ] as const
                  ).map((kind) => {
                    const count =
                      kind === "hash-declare"
                        ? result.frames.extract
                        : kind === "hash-reference"
                          ? result.frames.ref
                          : kind === "escape"
                            ? result.frames.escape
                            : result.frames.data;
                    const pct =
                      totalFrames > 0 ? (count / totalFrames) * 100 : 0;
                    return (
                      <div
                        key={kind}
                        className={FRAME_COLORS[kind]}
                        style={{ width: `${pct}%` }}
                        title={`${kind}: ${count}`}
                      />
                    );
                  })}
                </div>
                <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 font-mono text-xs">
                  <Legend
                    color={FRAME_COLORS["hash-declare"]!}
                    label="EXTRACT declares"
                    n={result.frames.extract}
                  />
                  <Legend
                    color={FRAME_COLORS["hash-reference"]!}
                    label="REF references"
                    n={result.frames.ref}
                  />
                  <Legend
                    color={FRAME_COLORS["escape"]!}
                    label="ESCAPE"
                    n={result.frames.escape}
                  />
                  <Legend
                    color={FRAME_COLORS["data"]!}
                    label="raw data blocks"
                    n={result.frames.data}
                  />
                </div>
              </CardContent>
            </Card>
          </div>

          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>Leading Frame Sequence</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-wrap gap-1.5">
                {result.frames.firstFrames.map((f, i) => (
                  <span
                    key={i}
                    className={cn(
                      "rounded-md px-2 py-1 font-mono text-[10px] text-white",
                      FRAME_COLORS[f.kind] ?? "bg-muted-foreground/60",
                    )}
                    title={f.detail}
                  >
                    {f.kind.replace("hash-", "").replace("back-", "")}
                    {f.detail ? ` ${f.detail}` : ""}
                  </span>
                ))}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Hot Segment Hashes</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-1.5">
                {result.frames.topHashes.map((h) => {
                  const max = result.frames.topHashes[0]?.count ?? 1;
                  return (
                    <div
                      key={h.hash}
                      className="flex items-center gap-3 font-mono text-xs"
                    >
                      <span className="text-muted-foreground w-32 shrink-0">
                        0x{h.hash.slice(0, 12)}…
                      </span>
                      <div className="bg-muted h-2.5 flex-1 overflow-hidden rounded-full">
                        <div
                          className="bg-chart-3/80 h-full rounded-full transition-all duration-300"
                          style={{ width: `${(h.count / max) * 100}%` }}
                        />
                      </div>
                      <span className="w-14 shrink-0 text-right tabular-nums">
                        ×{h.count}
                      </span>
                    </div>
                  );
                })}
                {result.frames.topHashes.length === 0 && (
                  <p className="text-muted-foreground text-xs">
                    No reference frames.
                  </p>
                )}
              </CardContent>
            </Card>
          </div>
        </>
      )}
    </div>
  );
}

function Big({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-muted-foreground text-[11px] font-medium tracking-wider uppercase">
        {label}
      </span>
      <span className="font-mono text-2xl font-bold tabular-nums">{value}</span>
    </div>
  );
}

function Legend({
  color,
  label,
  n,
}: {
  color: string;
  label: string;
  n: number;
}) {
  return (
    <span className="text-muted-foreground flex items-center gap-1.5">
      <span className={cn("size-2.5 shrink-0 rounded-sm", color)} />
      <span className="truncate">{label}</span>{" "}
      <b className="text-foreground ml-auto tabular-nums">{n}</b>
    </span>
  );
}
