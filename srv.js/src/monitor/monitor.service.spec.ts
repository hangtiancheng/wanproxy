import { describe, expect, it } from 'vitest';

import {
  codecStats,
  normalizeFleet,
  zeroStats,
  type FleetStatus,
} from './monitor.service.js';

describe('normalizeFleet', () => {
  it('maps a tcp codec onto the peer/interface slot by role', () => {
    const fs = normalizeFleet({
      generatedAt: 'now',
      proxies: [
        {
          acceptedConnections: 1,
          activeSockets: 0,
          codec: { mode: 'zlib-xcodec', role: 'incoming' },
          kind: 'tcp',
          listen: { host: '127.0.0.1', port: 8320 },
          listening: true,
          name: 'proxy-wan-a',
          upstream: { host: '127.0.0.1', port: 8321 },
        },
        {
          acceptedConnections: 0,
          activeSockets: 0,
          codec: { mode: 'zlib-xcodec', role: 'outgoing' },
          kind: 'tcp',
          listen: { host: '127.0.0.1', port: 8321 },
          listening: true,
          name: 'proxy-wan-b',
          upstream: { host: '127.0.0.1', port: 3101 },
        },
        {
          acceptedConnections: 0,
          activeSockets: 0,
          kind: 'socks',
          listen: { host: '127.0.0.1', port: 8330 },
          listening: true,
          name: 'proxy-socks',
        },
      ],
    });
    expect(fs.proxies[0]!.peerCodec).toEqual({ mode: 'zlib-xcodec' });
    expect(fs.proxies[0]!.interfaceCodec).toBeNull();
    expect(fs.proxies[0]!.listen).toEqual({ host: '127.0.0.1', port: '8320' });
    expect(fs.proxies[1]!.interfaceCodec).toEqual({ mode: 'zlib-xcodec' });
    expect(fs.proxies[1]!.peerCodec).toBeNull();
    expect(fs.proxies[2]!.kind).toBe('socks');
  });

  it('nulls both codec slots for mode none', () => {
    const fs = normalizeFleet({
      proxies: [
        {
          acceptedConnections: 0,
          activeSockets: 0,
          codec: { mode: 'none', role: 'incoming' },
          kind: 'tcp',
          listening: true,
          name: 'p',
        },
      ],
    });
    expect(fs.proxies[0]!.peerCodec).toBeNull();
    expect(fs.proxies[0]!.interfaceCodec).toBeNull();
  });
});

describe('codecStats', () => {
  it('returns zeros when stats are absent or the proxy is unknown', () => {
    const fs = normalizeFleet({
      proxies: [
        {
          acceptedConnections: 0,
          activeSockets: 0,
          codec: { mode: 'xcodec', role: 'incoming' },
          kind: 'tcp',
          listening: true,
          name: 'p',
        },
      ],
    });
    expect(codecStats(fs, 'p', 'peerCodec')).toEqual(zeroStats());
    expect(codecStats(fs, 'missing', 'peerCodec')).toEqual(zeroStats());
  });

  it('returns the counters when present', () => {
    const fs: FleetStatus = {
      generatedAt: 'now',
      proxies: [
        {
          acceptedConnections: 0,
          activeSockets: 0,
          kind: 'tcp',
          listening: true,
          name: 'p',
          peerCodec: {
            mode: 'xcodec',
            stats: {
              codecToIncomingBytes: 0,
              codecToOutgoingBytes: 2,
              incomingToCodecBytes: 10,
              outgoingToCodecBytes: 0,
            },
          },
        },
      ],
    };
    expect(codecStats(fs, 'p', 'peerCodec').incomingToCodecBytes).toBe(10);
    expect(codecStats(fs, 'p', 'interfaceCodec')).toEqual(zeroStats());
  });
});
