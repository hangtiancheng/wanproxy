/**
 * MonitorService proxies the wanproxy.js daemon /status endpoint and owns
 * the shared scenario definitions.  It is the TypeScript port of
 * srv/internal/monitor/monitor.go.
 *
 * The wanproxy.js monitor emits a single `codec` slot per proxy; this
 * service normalizes it onto the interfaceCodec/peerCodec shape the web UI
 * and the Go SRV use (role "outgoing" → interfaceCodec, "incoming" →
 * peerCodec).  Codecs activated with `track_statistics` carry the four
 * boundary byte counters in `stats`, which are passed through verbatim;
 * codecStats() reports zeros for proxies or slots without counters.
 */

import { Injectable } from '@nestjs/common';

import { monitorPort } from '../common/paths.js';
import { findScenario, scenarios, type Scenario } from './scenarios.js';

/** CodecStats mirrors the four per-codec byte counters. */
export interface CodecStats {
  incomingToCodecBytes: number;
  codecToOutgoingBytes: number;
  outgoingToCodecBytes: number;
  codecToIncomingBytes: number;
}

/** ProxyStatus is the normalized per-proxy status the web UI consumes. */
export interface ProxyStatus {
  kind: string;
  name: string;
  listening: boolean;
  acceptedConnections: number;
  activeSockets: number;
  address?: { address: string; port: number };
  listen?: { host?: string; port?: string };
  upstream?: { host?: string; port?: string };
  interfaceCodec?: CodecSlot | null;
  peerCodec?: CodecSlot | null;
}

/** CodecSlot is one normalized codec entry of a TCP proxy. */
export interface CodecSlot {
  mode: string;
  trackStatistics?: boolean;
  stats?: CodecStats;
}

/** FleetStatus is the normalized daemon status document. */
export interface FleetStatus {
  generatedAt: string;
  proxies: ProxyStatus[];
}

/** zeroStats is the all-zero codec counter set. */
export function zeroStats(): CodecStats {
  return {
    codecToIncomingBytes: 0,
    codecToOutgoingBytes: 0,
    incomingToCodecBytes: 0,
    outgoingToCodecBytes: 0,
  };
}

interface RawEndpoint {
  host?: string;
  port?: number | string;
}

interface RawProxy {
  kind: string;
  name: string;
  listening: boolean;
  acceptedConnections: number;
  activeSockets: number;
  address?: { address: string; port: number };
  listen?: RawEndpoint;
  upstream?: RawEndpoint;
  codec?: {
    mode: string;
    role?: string;
    trackStatistics?: boolean;
    stats?: CodecStats;
  };
}

function normalizeEndpoint(ep?: RawEndpoint): { host?: string; port?: string } | undefined {
  if (ep === undefined) {
    return undefined;
  }
  return {
    host: ep.host,
    port: ep.port === undefined ? undefined : String(ep.port),
  };
}

/** normalizeFleet maps the wanproxy.js status onto the web/API shape. */
export function normalizeFleet(raw: unknown): FleetStatus {
  const doc = raw as { generatedAt?: string; proxies?: RawProxy[] };
  const proxies: ProxyStatus[] = (doc.proxies ?? []).map((p) => {
    const base: ProxyStatus = {
      acceptedConnections: p.acceptedConnections ?? 0,
      activeSockets: p.activeSockets ?? 0,
      kind: p.kind,
      listen: normalizeEndpoint(p.listen),
      listening: !!p.listening,
      name: p.name,
    };
    if (p.address !== undefined) {
      base.address = p.address;
    }
    if (p.kind === 'tcp') {
      base.upstream = normalizeEndpoint(p.upstream);
      const codec = p.codec;
      if (codec !== undefined && codec.mode !== 'none') {
        const slot: CodecSlot = { mode: codec.mode };
        if (codec.trackStatistics !== undefined) {
          slot.trackStatistics = codec.trackStatistics;
        }
        if (codec.stats !== undefined) {
          slot.stats = codec.stats;
        }
        if (codec.role === 'outgoing') {
          base.interfaceCodec = slot;
          base.peerCodec = null;
        } else {
          base.interfaceCodec = null;
          base.peerCodec = slot;
        }
      } else {
        base.interfaceCodec = null;
        base.peerCodec = null;
      }
    }
    return base;
  });
  return { generatedAt: doc.generatedAt ?? new Date().toISOString(), proxies };
}

/**
 * codecStats extracts the codec counters for a proxy/slot pair, returning
 * zeros when the proxy or its stats are absent.
 */
export function codecStats(
  fs: FleetStatus,
  proxyName: string,
  codecSlot: string,
): CodecStats {
  for (const p of fs.proxies) {
    if (p.name !== proxyName) {
      continue;
    }
    const cs = codecSlot === 'interfaceCodec' ? p.interfaceCodec : p.peerCodec;
    return cs?.stats ?? zeroStats();
  }
  return zeroStats();
}

@Injectable()
export class MonitorService {
  /** scenarios returns the demo benchmark topologies. */
  public scenarios(): Scenario[] {
    return scenarios();
  }

  /** findScenario resolves one scenario by id. */
  public findScenario(id: string): Scenario | undefined {
    return findScenario(id);
  }

  /** statusRaw fetches the daemon /status document as text. */
  public async statusRaw(): Promise<string> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 3000);
    try {
      const res = await fetch(
        `http://127.0.0.1:${monitorPort()}/status`,
        { signal: controller.signal },
      );
      const text = await res.text();
      if (res.status < 200 || res.status >= 300) {
        throw new Error(`monitor status ${res.status}`);
      }
      return text;
    } finally {
      clearTimeout(timer);
    }
  }

  /** status fetches and normalizes the daemon /status document. */
  public async status(): Promise<FleetStatus> {
    const raw = await this.statusRaw();
    return normalizeFleet(JSON.parse(raw));
  }
}
