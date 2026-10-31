import { useEffect, useState } from "react";
import {
  Activity,
  ArrowLeftRight,
  ArrowRight,
  Server,
  TrendingDown,
  Waypoints,
} from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { RatioBar } from "@/components/ratio-bar";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api, type FleetStatus, type ProxyStatus } from "@/lib/api";
import { formatBytes } from "@/lib/format";

function useMonitor(intervalMs = 2000) {
  const [status, setStatus] = useState<FleetStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const s = await api.monitorStatus();
        if (alive) {
          setStatus(s);
          setError(null);
        }
      } catch (err) {
        if (alive) setError(String(err));
      }
    };
    tick();
    const timer = setInterval(tick, intervalMs);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [intervalMs]);
  return { status, error };
}

function codecTotals(p: ProxyStatus) {
  const stats = p.peerCodec?.stats ?? p.interfaceCodec?.stats;
  if (!stats) return null;
  const app = stats.incomingToCodecBytes + stats.codecToIncomingBytes;
  const wire = stats.codecToOutgoingBytes + stats.outgoingToCodecBytes;
  return { app, wire, mode: p.peerCodec?.mode ?? p.interfaceCodec?.mode ?? "" };
}

function StatCard({
  icon: Icon,
  label,
  value,
  loading,
}: {
  icon: typeof Server;
  label: string;
  value: string;
  loading?: boolean;
}) {
  return (
    <Card size="sm">
      <CardContent className="flex items-center gap-3">
        <div className="bg-primary/10 text-primary flex size-9 shrink-0 items-center justify-center rounded-lg">
          <Icon />
        </div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-muted-foreground text-xs">{label}</span>
          {loading ? (
            <Skeleton className="h-6 w-16" />
          ) : (
            <span className="font-mono text-xl font-semibold tabular-nums">
              {value}
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

export function Dashboard() {
  const { status, error } = useMonitor();
  const proxies = status?.proxies ?? [];
  const loading = status === null && error === null;

  const listening = proxies.filter((p) => p.listening).length;
  const accepted = proxies.reduce((sum, p) => sum + p.acceptedConnections, 0);
  const active = proxies.reduce((sum, p) => sum + p.activeSockets, 0);
  const codecRows = proxies
    .map((p) => ({ p, t: codecTotals(p) }))
    .filter((x) => x.t !== null);
  const appTotal = codecRows.reduce((sum, x) => sum + x.t!.app, 0);
  const wireTotal = codecRows.reduce((sum, x) => sum + x.t!.wire, 0);
  const savings =
    appTotal > 0 ? Math.max(0, (1 - wireTotal / appTotal) * 100) : null;

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Overview">
        {status && (
          <Badge variant="success" className="gap-1.5">
            <span className="relative flex size-1.5">
              <span className="absolute inline-flex size-full animate-ping rounded-full bg-current opacity-60" />
              <span className="relative inline-flex size-1.5 rounded-full bg-current" />
            </span>
            live · {status.generatedAt.slice(11, 19)}
          </Badge>
        )}
      </PageHeader>

      {error && (
        <Alert variant="destructive">
          <Waypoints />
          <AlertTitle>Daemon not ready</AlertTitle>
          <AlertDescription>
            <p className="font-mono text-xs">{error}</p>
          </AlertDescription>
        </Alert>
      )}

      <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard
          icon={Server}
          label="Listeners up"
          value={status ? `${listening}/${proxies.length}` : "-"}
          loading={loading}
        />
        <StatCard
          icon={ArrowLeftRight}
          label="Accepted connections"
          value={status ? String(accepted) : "-"}
          loading={loading}
        />
        <StatCard
          icon={Activity}
          label="Active sockets"
          value={status ? String(active) : "-"}
          loading={loading}
        />
        <StatCard
          icon={TrendingDown}
          label="WAN bytes saved"
          value={savings !== null ? `${savings.toFixed(1)}%` : "—"}
          loading={loading}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Demo Topology</CardTitle>
        </CardHeader>
        <CardContent>
          <Topology status={status} />
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Proxy Listeners</CardTitle>
          </CardHeader>
          <CardContent>
            {loading ? (
              <div className="flex flex-col gap-2">
                {Array.from({ length: 6 }).map((_, i) => (
                  <Skeleton key={i} className="h-7 w-full" />
                ))}
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead>Proxy</TableHead>
                    <TableHead>Kind</TableHead>
                    <TableHead>Listen</TableHead>
                    <TableHead className="text-right">Accepted</TableHead>
                    <TableHead className="text-right">Active</TableHead>
                    <TableHead className="text-right">Status</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody className="font-mono text-xs">
                  {proxies.map((p) => (
                    <TableRow key={p.name}>
                      <TableCell className="text-foreground font-medium">
                        {p.name}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {p.kind}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {p.listen?.host}:{p.listen?.port}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {p.acceptedConnections}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {p.activeSockets}
                      </TableCell>
                      <TableCell className="text-right">
                        {p.listening ? (
                          <Badge variant="success">listening</Badge>
                        ) : (
                          <Badge variant="destructive">down</Badge>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Codec Wire/App Byte Ratio</CardTitle>
          </CardHeader>
          <CardContent>
            {loading ? (
              <div className="flex flex-col gap-4">
                {Array.from({ length: 3 }).map((_, i) => (
                  <Skeleton key={i} className="h-10 w-full" />
                ))}
              </div>
            ) : codecRows.length > 0 ? (
              <div className="flex flex-col gap-4">
                {codecRows.map(({ p, t }) => (
                  <div key={p.name} className="flex flex-col gap-1.5">
                    <div className="flex items-center justify-between gap-2 text-xs">
                      <span className="truncate font-mono font-medium">
                        {p.name}
                      </span>
                      <span className="flex items-center gap-2">
                        <Badge variant="info">{t!.mode}</Badge>
                        <span className="text-muted-foreground font-mono tabular-nums">
                          {formatBytes(t!.wire)} / {formatBytes(t!.app)}
                        </span>
                      </span>
                    </div>
                    <RatioBar appBytes={t!.app} wireBytes={t!.wire} />
                  </div>
                ))}
              </div>
            ) : (
              <Empty>
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <TrendingDown />
                  </EmptyMedia>
                  <EmptyTitle>No codec traffic yet</EmptyTitle>
                </EmptyHeader>
              </Empty>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

const CHAIN_ROWS: {
  entry: string;
  label: string;
  codec: string;
  hops: string[];
  tone: "secondary" | "info" | "success" | "violet" | "warning";
}[] = [
  {
    entry: ":8310",
    label: "passthrough",
    codec: "no codec",
    hops: ["SRV :3101"],
    tone: "secondary",
  },
  {
    entry: ":8311",
    label: "zlib-a → zlib-b",
    codec: "zlib",
    hops: [":8351 zlib-b", "SRV :3101"],
    tone: "warning",
  },
  {
    entry: ":8312",
    label: "xcodec-a → xcodec-b",
    codec: "XCodec",
    hops: [":8352 xcodec-b", "SRV :3101"],
    tone: "info",
  },
  {
    entry: ":8320",
    label: "wan-a → wan-b",
    codec: "XCodec + zlib",
    hops: [":8321 wan-b", "SRV :3101"],
    tone: "success",
  },
  {
    entry: ":8330",
    label: "SOCKS5",
    codec: "CONNECT",
    hops: ["SRV :3101"],
    tone: "violet",
  },
  {
    entry: ":8340",
    label: "ssh-a → ssh-b",
    codec: "SSH + XCodec + zlib",
    hops: [":8341 ssh-b", "SSH echo :8349"],
    tone: "success",
  },
];

function Topology({ status }: { status: FleetStatus | null }) {
  const live = (name: string) => status?.proxies.find((p) => p.name === name);
  return (
    <div className="flex flex-col gap-2.5 overflow-x-auto">
      {CHAIN_ROWS.map((row) => {
        const proxyName = `proxy-${row.label.split(" → ")[0]}`;
        const st = live(proxyName) ?? live(`proxy-${row.label}`);
        const conns = st?.acceptedConnections ?? 0;
        return (
          <div
            key={row.entry}
            className="flex min-w-180 items-center gap-2 text-xs"
          >
            <span className="text-muted-foreground w-24 shrink-0 text-right font-mono">
              {row.entry}
            </span>
            <span className="w-44 shrink-0 font-mono font-medium">
              {row.label}
            </span>
            <Badge variant={row.tone}>{row.codec}</Badge>
            <ArrowRight className="text-muted-foreground/50 size-3.5 shrink-0" />
            {row.hops.map((hop) => (
              <span key={hop} className="flex items-center gap-2">
                <span className="bg-muted text-muted-foreground rounded-md px-2 py-1 font-mono">
                  {hop}
                </span>
                <ArrowRight className="text-muted-foreground/50 size-3.5 shrink-0" />
              </span>
            ))}
            <span className="text-muted-foreground ml-auto font-mono tabular-nums">
              {conns} conn
            </span>
          </div>
        );
      })}
    </div>
  );
}
