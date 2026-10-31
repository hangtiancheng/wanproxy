/**
 * The demo benchmark topologies, copied from srv/internal/monitor/monitor.go
 * (and matching web/src/lib/api.ts).  The SSH chain is omitted because the
 * wanproxy.js runtime does not yet splice a codec pipeline through an
 * SSH-SSH proxy; every other scenario is driven by wanproxy.js.
 */

import { srvPort } from '../common/paths.js';

/** Scenario describes one benchmark topology. */
export interface Scenario {
  id: string;
  label: string;
  description: string;
  kind: 'http' | 'socks' | 'ssh';
  base?: string;
  proxy?: string;
  target?: string;
  statsProxy?: string;
  statsCodec?: string;
  protocol: string;
}

/** scenarios returns the demo scenarios. */
export function scenarios(): Scenario[] {
  const port = srvPort();
  const srvBase = `http://127.0.0.1:${port}`;
  const srvTarget = `127.0.0.1:${port}`;
  return [
    {
      id: 'direct',
      label: 'Direct HTTP',
      description: 'Browser/benchmark client hits the SRV directly, no proxy',
      kind: 'http',
      base: srvBase,
      protocol: 'TCP + HTTP/1.1',
    },
    {
      id: 'passthrough',
      label: 'TCP passthrough proxy',
      description: 'wanproxy TCP-to-TCP proxy with no codec (control baseline)',
      kind: 'http',
      base: 'http://127.0.0.1:8310',
      statsProxy: 'proxy-passthrough',
      protocol: 'TCP + HTTP/1.1',
    },
    {
      id: 'zlib-chain',
      label: 'zlib compression chain (dual proxy)',
      description:
        'A-side deflate → B-side inflate, pure compression control (no dedup)',
      kind: 'http',
      base: 'http://127.0.0.1:8311',
      statsProxy: 'proxy-zlib',
      statsCodec: 'peerCodec',
      protocol: 'TCP + HTTP/1.1 + zlib',
    },
    {
      id: 'xcodec-chain',
      label: 'XCodec dedup chain (dual proxy)',
      description:
        'A-side XCodec encode → B-side decode, pure segment-level dedup (2048 B segments, rolling hash, no compression)',
      kind: 'http',
      base: 'http://127.0.0.1:8312',
      statsProxy: 'proxy-xcodec',
      statsCodec: 'peerCodec',
      protocol: 'TCP + HTTP/1.1 + XCodec',
    },
    {
      id: 'wan-chain',
      label: 'WAN optimization chain (dual proxy)',
      description:
        'Client-side proxy encodes XCodec+zlib → server-side proxy decodes; the classic WANProxy deployment',
      kind: 'http',
      base: 'http://127.0.0.1:8320',
      statsProxy: 'proxy-wan-a',
      statsCodec: 'peerCodec',
      protocol: 'TCP + HTTP/1.1 + XCodec + zlib',
    },
    {
      id: 'socks',
      label: 'SOCKS5 CONNECT proxy',
      description:
        'SOCKS5 handshake then CONNECT to the SRV (C++ semantics: reply before dial)',
      kind: 'socks',
      proxy: '127.0.0.1:8330',
      target: srvTarget,
      statsProxy: 'proxy-socks',
      protocol: 'SOCKS5 + TCP + HTTP/1.1',
    },
  ];
}

/** findScenario returns the scenario with the given id, if any. */
export function findScenario(id: string): Scenario | undefined {
  return scenarios().find((s) => s.id === id);
}
