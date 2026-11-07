import { useCallback, useEffect, useRef, useState } from "react";
import axios from "axios";
import {
  ArrowDown,
  ArrowUp,
  Download,
  Eye,
  FileVideo,
  FolderOpen,
  Trash2,
  Wand2,
} from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
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
import { cn } from "@/lib/utils";
import { api, PATTERNS, UPLOAD_ROUTES, type FileMeta } from "@/lib/api";
import { formatBytes, formatMs, shortSha } from "@/lib/format";

const VIDEO_EXT = /\.(mp4|webm|mov|m4v)$/i;
const IMAGE_EXT = /\.(png|jpe?g|gif|webp)$/i;

function createTimer() {
  const started = performance.now();
  return () => performance.now() - started;
}

const ROUTE_ITEMS: Record<string, string> = Object.fromEntries(
  UPLOAD_ROUTES.map((r) => [r.base, `${r.label} (:${new URL(r.base).port})`]),
);

interface TransferState {
  id: number;
  kind: "upload" | "download";
  name: string;
  route: string;
  progress: number;
  done?: { ms: number; bytes: number };
  error?: string;
}

export function Files() {
  const [files, setFiles] = useState<FileMeta[]>([]);
  const [route, setRoute] = useState(UPLOAD_ROUTES[4]!.base);
  const [transfers, setTransfers] = useState<TransferState[]>([]);
  const [dragOver, setDragOver] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [genForm, setGenForm] = useState({
    name: "vm-image-sim.bin",
    sizeMb: 64,
    pattern: "mixed",
    seed: 99,
  });
  const [preview, setPreview] = useState<FileMeta | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const transferId = useRef(0);

  const refresh = useCallback(async () => {
    try {
      const fs = await api.files();
      setFiles(fs);
    } catch {
    }
  }, []);
  useEffect(() => {
    let alive = true;
    api
      .files()
      .then((fs) => {
        if (alive) setFiles(fs);
      })
      .catch(() => {
      });
    return () => {
      alive = false;
    };
  }, []);

  const patchTransfer = (id: number, patch: Partial<TransferState>) => {
    setTransfers((prev) =>
      prev.map((t) => (t.id === id ? { ...t, ...patch } : t)),
    );
  };

  const uploadFile = async (file: File) => {
    const id = ++transferId.current;
    const elapsed = createTimer();
    const entry: TransferState = {
      id,
      kind: "upload",
      name: file.name,
      route,
      progress: 0,
    };
    setTransfers((prev) => [...prev, entry]);
    try {
      await axios.post(api.uploadUrl(route, file.name), file, {
        headers: { "Content-Type": "application/octet-stream" },
        onUploadProgress: (e) => {
          if (e.total) patchTransfer(id, { progress: e.loaded / e.total });
        },
      });
      patchTransfer(id, {
        progress: 1,
        done: { ms: elapsed(), bytes: file.size },
      });
      refresh();
    } catch (err) {
      patchTransfer(id, { error: String(err) });
    }
  };

  const downloadFile = async (meta: FileMeta) => {
    const id = ++transferId.current;
    const elapsed = createTimer();
    const entry: TransferState = {
      id,
      kind: "download",
      name: meta.name,
      route,
      progress: 0,
    };
    setTransfers((prev) => [...prev, entry]);
    try {
      const res = await axios.get(api.fileUrlVia(meta.id, route), {
        responseType: "blob",
        onDownloadProgress: (e) => {
          if (e.total) patchTransfer(id, { progress: e.loaded / e.total });
        },
      });
      patchTransfer(id, {
        progress: 1,
        done: { ms: elapsed(), bytes: (res.data as Blob).size },
      });
      const url = URL.createObjectURL(res.data as Blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = meta.name;
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      patchTransfer(id, { error: String(err) });
    }
  };

  const generate = async () => {
    setGenerating(true);
    try {
      await api.generateFile(
        genForm.name,
        Math.round(genForm.sizeMb * 1024 * 1024),
        genForm.pattern,
        genForm.seed,
      );
      await refresh();
    } finally {
      setGenerating(false);
    }
  };

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="File Transfers">
        <Field orientation="horizontal" className="w-fit gap-2">
          <FieldLabel htmlFor="route" className="text-muted-foreground">
            Transfer route
          </FieldLabel>
          <Select
            value={route}
            onValueChange={(value) => setRoute(value as string)}
            items={ROUTE_ITEMS}
          >
            <SelectTrigger id="route" className="w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {UPLOAD_ROUTES.map((r) => (
                  <SelectItem key={r.id} value={r.base}>
                    {r.label} (:{new URL(r.base).port})
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
      </PageHeader>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Upload</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div
              role="button"
              tabIndex={0}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ")
                  fileInput.current?.click();
              }}
              onDragOver={(e) => {
                e.preventDefault();
                setDragOver(true);
              }}
              onDragLeave={() => setDragOver(false)}
              onDrop={(e) => {
                e.preventDefault();
                setDragOver(false);
                for (const f of Array.from(e.dataTransfer.files)) {
                  void uploadFile(f);
                }
              }}
              onClick={() => fileInput.current?.click()}
              className={cn(
                "flex cursor-pointer flex-col items-center justify-center gap-1 rounded-xl border-2 border-dashed px-6 py-10 text-center transition-colors",
                dragOver
                  ? "border-primary bg-primary/5"
                  : "border-input bg-muted/40 hover:border-primary/40 hover:bg-muted/70",
              )}
            >
              <FileVideo
                className={cn(
                  "size-8 transition-colors",
                  dragOver ? "text-primary" : "text-muted-foreground",
                )}
              />
              <p className="mt-1 text-sm font-medium">
                Drag & drop videos or large files here, or click to browse
              </p>
              <input
                ref={fileInput}
                type="file"
                className="hidden"
                multiple
                onChange={(e) => {
                  for (const f of Array.from(e.target.files ?? [])) {
                    void uploadFile(f);
                  }
                  e.target.value = "";
                }}
              />
            </div>

            {transfers.length > 0 && (
              <div className="flex flex-col gap-3">
                {transfers.map((t) => (
                  <div
                    key={t.id}
                    className="bg-muted/30 flex flex-col gap-2 rounded-lg border p-3"
                  >
                    <div className="flex items-center justify-between gap-2 text-xs">
                      <span className="flex min-w-0 items-center gap-1.5 font-mono">
                        {t.kind === "upload" ? (
                          <ArrowUp className="text-chart-1 size-3.5 shrink-0" />
                        ) : (
                          <ArrowDown className="text-chart-3 size-3.5 shrink-0" />
                        )}
                        <span className="truncate">
                          {t.kind === "upload" ? "Upload" : "Download"} {t.name}
                        </span>
                        <span className="text-muted-foreground shrink-0">
                          via :{new URL(t.route).port}
                        </span>
                      </span>
                      {t.done ? (
                        <Badge variant="success" className="shrink-0">
                          {formatBytes(t.done.bytes)} · {formatMs(t.done.ms)} ·{" "}
                          {(
                            t.done.bytes /
                            1024 /
                            1024 /
                            (t.done.ms / 1000)
                          ).toFixed(1)}{" "}
                          MB/s
                        </Badge>
                      ) : t.error ? (
                        <Badge variant="destructive" className="shrink-0">
                          failed
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground flex shrink-0 items-center gap-1.5 tabular-nums">
                          <Spinner className="size-3.5" />
                          {(t.progress * 100).toFixed(0)}%
                        </span>
                      )}
                    </div>
                    <Progress value={t.progress * 100} />
                    {t.error && (
                      <p className="text-destructive font-mono text-[11px]">
                        {t.error}
                      </p>
                    )}
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Generate Test File</CardTitle>
          </CardHeader>
          <CardContent>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="gen-name">File name</FieldLabel>
                <Input
                  id="gen-name"
                  value={genForm.name}
                  onChange={(e) =>
                    setGenForm({ ...genForm, name: e.target.value })
                  }
                  className="font-mono"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="gen-size">Size (MB)</FieldLabel>
                <Input
                  id="gen-size"
                  type="number"
                  min={1}
                  value={genForm.sizeMb}
                  onChange={(e) =>
                    setGenForm({ ...genForm, sizeMb: Number(e.target.value) })
                  }
                  className="font-mono"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="gen-pattern">Data pattern</FieldLabel>
                <Select
                  value={genForm.pattern}
                  onValueChange={(value) =>
                    setGenForm({ ...genForm, pattern: value as string })
                  }
                  items={PATTERNS}
                >
                  <SelectTrigger id="gen-pattern" className="w-full">
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
              <Field>
                <FieldLabel htmlFor="gen-seed">Seed</FieldLabel>
                <Input
                  id="gen-seed"
                  type="number"
                  value={genForm.seed}
                  onChange={(e) =>
                    setGenForm({ ...genForm, seed: Number(e.target.value) })
                  }
                  className="font-mono"
                />
              </Field>
              <Button
                onClick={generate}
                disabled={generating}
                className="w-full justify-center"
              >
                {generating ? (
                  <>
                    <Spinner data-icon="inline-start" />
                    Generating…
                  </>
                ) : (
                  <>
                    <Wand2 data-icon="inline-start" />
                    Generate File
                  </>
                )}
              </Button>
            </FieldGroup>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Files</CardTitle>
        </CardHeader>
        <CardContent>
          {files.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <FolderOpen />
                </EmptyMedia>
                <EmptyTitle>No files yet</EmptyTitle>
              </EmptyHeader>
            </Empty>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>Name</TableHead>
                  <TableHead>Size</TableHead>
                  <TableHead>Pattern</TableHead>
                  <TableHead>SHA-256</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {files.map((f) => (
                  <TableRow key={f.id}>
                    <TableCell className="font-mono text-xs font-medium">
                      {f.name}
                    </TableCell>
                    <TableCell className="text-muted-foreground font-mono text-xs tabular-nums">
                      {formatBytes(f.size)}
                    </TableCell>
                    <TableCell>
                      {f.pattern ? (
                        <Badge variant="info">{f.pattern}</Badge>
                      ) : (
                        <span className="text-muted-foreground text-xs">
                          uploaded
                        </span>
                      )}
                    </TableCell>
                    <TableCell
                      className="text-muted-foreground font-mono text-xs"
                      title={f.sha256}
                    >
                      {shortSha(f.sha256)}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs tabular-nums">
                      {f.createdAt.slice(5, 19).replace("T", " ")}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1.5">
                        {(VIDEO_EXT.test(f.name) || IMAGE_EXT.test(f.name)) && (
                          <Button
                            variant="outline"
                            size="xs"
                            onClick={() => setPreview(f)}
                          >
                            <Eye data-icon="inline-start" />
                            Preview
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="xs"
                          onClick={() => {
                            void downloadFile(f);
                          }}
                        >
                          <Download data-icon="inline-start" />
                          Download
                        </Button>
                        <Button
                          variant="destructive"
                          size="xs"
                          onClick={async () => {
                            await api.deleteFile(f.id);
                            refresh();
                          }}
                        >
                          <Trash2 data-icon="inline-start" />
                          Delete
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog
        open={preview !== null}
        onOpenChange={(open) => {
          if (!open) setPreview(null);
        }}
      >
        <DialogContent className="sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle className="font-mono">{preview?.name}</DialogTitle>
          </DialogHeader>
          {preview &&
            (VIDEO_EXT.test(preview.name) ? (
              <video
                src={api.fileUrl(preview.id)}
                controls
                className="max-h-[70vh] w-full rounded-lg"
              />
            ) : (
              <img
                src={api.fileUrl(preview.id)}
                className="mx-auto max-h-[70vh] max-w-full rounded-lg"
                alt={preview.name}
              />
            ))}
        </DialogContent>
      </Dialog>
    </div>
  );
}
