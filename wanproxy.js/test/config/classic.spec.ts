import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import { ClassicParser, tokenizeLine, unquote } from "@/config/classic.js";
import type { RuntimeConfig } from "@/config/model.js";

// The exact wanproxy.conf shipped with the C++ implementation, with the
// disk cache path swapped for a per-test temporary file.
const CPP_SAMPLE = `# Set log-mask before doing anything else.
create log-mask catch-all
set catch-all.regex "^/"
set catch-all.mask INFO
activate catch-all

create cache memorycache0
set memorycache0.type Memory
set memorycache0.size 128MB
activate memorycache0

create cache diskcache0
set diskcache0.type Disk
set diskcache0.size 4MB
set diskcache0.path "DISK_PATH"
activate diskcache0

create cache cache0
set cache0.type Pair
set cache0.primary memorycache0
set cache0.secondary diskcache0
activate cache0

create codec codec0
set codec0.codec XCodec
set codec0.cache cache0
set codec0.compressor zlib
set codec0.compressor_level 6
set codec0.track_statistics true
activate codec0

create codec codec1
set codec1.codec XCodec
set codec1.cache cache0
set codec1.compressor zlib
set codec1.compressor_level 6
set codec1.track_statistics true
activate codec1

create interface if0
set if0.family IP
set if0.host "localhost"
set if0.port "3300"
activate if0

create interface if1
set if1.family $if0.family
set if1.host $if0.host
set if1.port "3301"
activate if1

create interface if2
set if2.family $if0.family
set if2.host $if0.host
set if2.port "3302"
activate if2

create peer peer0
set peer0.family $if1.family
set peer0.host $if1.host
set peer0.port $if1.port
activate peer0

create peer peer1
set peer1.family $if2.family
set peer1.host $if2.host
set peer1.port $if2.port
activate peer1

create proxy proxy0
set proxy0.type TCP-TCP
set proxy0.interface if0
set proxy0.interface_codec None
set proxy0.peer peer0
set proxy0.peer_codec codec0
activate proxy0

create proxy proxy1
set proxy1.type TCP-TCP
set proxy1.interface if1
set proxy1.interface_codec codec1
set proxy1.peer peer1
set proxy1.peer_codec None
activate proxy1

create proxy-socks proxy-socks0
set proxy-socks0.interface if2
activate proxy-socks0

create interface if3
set if3.family $if0.family
set if3.host $if0.host
set if3.port "9900"
activate if3

create monitor monitor0
set monitor0.interface if3
activate monitor0
`;

const tempDirs: string[] = [];
const openConfigs: RuntimeConfig[] = [];

function makeTempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "wanproxy-classic-"));
  tempDirs.push(dir);
  return dir;
}

function parseSample(): RuntimeConfig {
  const dir = makeTempDir();
  const text = CPP_SAMPLE.replace("DISK_PATH", join(dir, "wanproxy.xcache"));
  const config = new ClassicParser().parse(text);
  openConfigs.push(config);
  return config;
}

function rsaPem(): Uint8Array {
  const { privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const pem = privateKey.export({ format: "pem", type: "pkcs1" });
  return new Uint8Array(Buffer.from(pem));
}

afterEach(() => {
  for (const config of openConfigs.splice(0)) {
    for (const disk of config.disks) {
      disk.close();
    }
  }
  for (const dir of tempDirs.splice(0)) {
    rmSync(dir, { force: true, recursive: true });
  }
});

describe("ClassicParser", () => {
  it("parses the C++ sample configuration", () => {
    const config = parseSample();
    expect(config.logLevel).toBe("info");
    expect(config.proxies).toHaveLength(2);
    expect(config.socksProxies).toHaveLength(1);
    expect(config.monitor?.interface.port).toBe("9900");

    const p0 = config.proxies[0]!;
    expect(p0.name).toBe("proxy0");
    expect(p0.interface.host).toBe("localhost");
    expect(p0.interface.port).toBe("3300");
    expect(p0.interfaceCodec).toBeUndefined();
    expect(p0.peer.port).toBe("3301");
    expect(p0.peerCodec).toBeDefined();
    expect(p0.peerCodec?.mode).toBe("zlib-xcodec");
    expect(p0.peerCodec?.compressorLevel).toBe(6);
    expect(p0.peerCodec?.trackStatistics).toBe(true);
    expect(p0.peerCodec?.stats).toBeDefined();

    const p1 = config.proxies[1]!;
    expect(p1.interfaceCodec).toBeDefined();
    expect(p1.peerCodec).toBeUndefined();
    expect(p1.interface.port).toBe("3301");
    expect(p1.peer.port).toBe("3302");

    // Both codecs share the same activated pair cache.
    expect(p0.peerCodec?.cache).toBe(p1.interfaceCodec?.cache);
    expect(config.socksProxies[0]?.interface.port).toBe("3302");
    expect(config.objects.length).toBeGreaterThanOrEqual(15);
  });

  it("retains object fields for monitor export", () => {
    const config = parseSample();
    const codec0 = config.objects.find((o) => o.name === "codec0");
    expect(codec0?.class).toBe("codec");
    const level = codec0?.fields.find((f) => f.name === "compressor_level");
    expect(level?.value).toBe("6");
    expect(level?.type).toBe("string");
  });

  it("resolves $object.member references", () => {
    const config = parseSample();
    // if1.family/host come from $if0; peer0 mirrors if1.
    expect(config.proxies[0]?.peer.host).toBe("localhost");
  });

  it("loads an SSH-SSH proxy host key from the default path", () => {
    const pem = rsaPem();
    const config = new ClassicParser({ readFile: () => pem }).parse(`
create interface if0
set if0.family IP
set if0.host "localhost"
set if0.port "1"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "localhost"
set peer0.port "2"
activate peer0
create proxy p0
set p0.type SSH-SSH
set p0.interface if0
set p0.peer peer0
activate p0
`);
    expect(config.proxies[0]?.ssh?.hostKeyPath).toBe("ssh-server1.pem");
    expect(config.proxies[0]?.ssh?.hostKey).toBeDefined();
  });

  it("honors an explicit server_host_key path", () => {
    const pem = rsaPem();
    const reads: string[] = [];
    const config = new ClassicParser({
      readFile: (path) => {
        reads.push(path);
        return pem;
      },
    }).parse(`
create interface if0
set if0.family IP
set if0.host "h"
set if0.port "1"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "h"
set peer0.port "2"
activate peer0
create proxy p0
set p0.type SSH-SSH
set p0.interface if0
set p0.peer peer0
set p0.server_host_key "custom.pem"
activate p0
`);
    expect(reads).toEqual(["custom.pem"]);
    expect(config.proxies[0]?.ssh?.hostKeyPath).toBe("custom.pem");
  });

  it("rejects the documented error cases", () => {
    const cases: Record<string, string> = {
      "activate missing": "activate nope\n",
      "bad ref": "create interface if0\nset if0.host $nope.host\n",
      "bad set ident": 'create interface if0\nset if0 "x"\n',
      "bad set target": 'set missing.host "x"\n',
      "duplicate create": "create interface if0\ncreate interface if0\n",
      "proxy needs iface": "create proxy p0\nactivate p0\n",
      "unknown class": "create frob x\n",
      "unknown command": "frobnicate foo\ncreate interface if0\nactivate if0",
      "unset ref":
        "create interface if0\ncreate interface if1\nset if1.host $if0.host\n",
    };
    for (const [name, text] of Object.entries(cases)) {
      expect(() => new ClassicParser().parse(text), name).toThrow();
    }
  });

  it("rejects server_host_key on a TCP proxy", () => {
    expect(() =>
      new ClassicParser({ readFile: () => rsaPem() }).parse(`
create interface if0
set if0.family IP
set if0.host "h"
set if0.port "1"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "h"
set peer0.port "2"
activate peer0
create proxy p0
set p0.type TCP-TCP
set p0.interface if0
set p0.peer peer0
set p0.server_host_key "k.pem"
activate p0
`),
    ).toThrow(/server_host_key is only valid for SSH proxies/);
  });

  it("rejects a Unix socket with host/port set", () => {
    expect(() =>
      new ClassicParser().parse(`
create interface if0
set if0.family Unix
set if0.host "localhost"
set if0.path "/tmp/x"
activate if0
create proxy-socks s0
set s0.interface if0
activate s0
`),
    ).toThrow(/Unix domain socket has host/);
  });

  it("requires at least one proxy", () => {
    expect(() =>
      new ClassicParser().parse(`
create interface if0
set if0.family IP
set if0.host "h"
set if0.port "1"
activate if0
`),
    ).toThrow(/defines no proxies/);
  });

  it("maps the most detailed log mask", () => {
    const config = new ClassicParser().parse(`
create log-mask m0
set m0.mask WARNING
activate m0
create log-mask m1
set m1.mask DEBUG
activate m1
create interface if0
set if0.family IP
set if0.host "h"
set if0.port "1"
activate if0
create proxy-socks s0
set s0.interface if0
activate s0
`);
    expect(config.logLevel).toBe("debug");
  });

  it("parses the shipped wanproxy.conf.example", () => {
    const dir = makeTempDir();
    const example = readFileSync(
      join(process.cwd(), "wanproxy.conf.example"),
      "utf8",
    ).replace(
      '"wanproxy.xcache"',
      JSON.stringify(join(dir, "wanproxy.xcache")),
    );
    const config = new ClassicParser().parse(example);
    openConfigs.push(config);
    expect(config.proxies.map((p) => p.name)).toEqual(["proxy0", "proxy1"]);
    expect(config.socksProxies.map((s) => s.name)).toEqual(["proxy-socks0"]);
    expect(config.monitor?.name).toBe("monitor0");
    // proxy0 carries the peer codec; proxy1 the interface codec.
    expect(config.proxies[0]?.peerCodec?.mode).toBe("zlib-xcodec");
    expect(config.proxies[1]?.interfaceCodec?.mode).toBe("zlib-xcodec");
    expect(config.proxies[0]?.peerCodec?.cache).toBe(
      config.proxies[1]?.interfaceCodec?.cache,
    );
  });
});

describe("ClassicParser error branches", () => {
  it("covers the documented activation error branches", () => {
    const cases: ReadonlyArray<readonly [string, string, RegExp]> = [
      [
        "memory cache with path",
        'create cache c\nset c.type Memory\nset c.path "x"\nactivate c\n',
        /no path parameter for memory caches/,
      ],
      [
        "memory cache with hierarchy",
        "create cache p\nset p.type Memory\nactivate p\ncreate cache c\nset c.type Memory\nset c.primary p\nactivate c\n",
        /specified cache hierarchy for memory cache/,
      ],
      [
        "disk cache with uuid",
        'create cache c\nset c.type Disk\nset c.uuid "0123456789abcdef"\nset c.path "x"\nactivate c\n',
        /cannot specify UUID for disk cache/,
      ],
      [
        "disk cache without path",
        "create cache c\nset c.type Disk\nactivate c\n",
        /disk cache requires a path/,
      ],
      [
        "pair cache with size",
        "create cache c\nset c.type Pair\nset c.size 1MB\nactivate c\n",
        /no size parameter for cache pair/,
      ],
      [
        "pair cache missing secondary",
        "create cache p\nset p.type Memory\nactivate p\ncreate cache c\nset c.type Pair\nset c.primary p\nactivate c\n",
        /cache pair requires both primary and secondary cache/,
      ],
      [
        "pair cache with unactivated primary",
        "create cache p\nset p.type Memory\ncreate cache s\nset s.type Memory\ncreate cache c\nset c.type Pair\nset c.primary p\nset c.secondary s\nactivate c\n",
        /must be activated prior to use as a primary cache/,
      ],
      [
        "unknown cache type",
        "create cache c\nset c.type Frobnitz\nactivate c\n",
        /unknown cache type/,
      ],
      [
        "invalid cache uuid",
        'create cache c\nset c.type Memory\nset c.uuid "short"\nactivate c\n',
        /specified UUID is invalid/,
      ],
      [
        "unknown codec",
        "create codec c\nset c.codec Frobnitz\nactivate c\n",
        /unknown codec/,
      ],
      [
        "unknown compressor",
        "create codec c\nset c.compressor bzip2\nactivate c\n",
        /unknown compressor/,
      ],
      [
        "invalid compressor level",
        "create codec c\nset c.compressor_level abc\nactivate c\n",
        /invalid compressor_level/,
      ],
      [
        "xcodec without cache",
        "create codec c\nset c.codec XCodec\nactivate c\n",
        /codec requires an activated cache/,
      ],
      [
        "invalid track_statistics",
        "create codec c\nset c.track_statistics maybe\nactivate c\n",
        /invalid track_statistics/,
      ],
      [
        "IP socket with path",
        'create interface if0\nset if0.family IP\nset if0.path "/tmp/x"\nactivate if0\n',
        /IP socket has path field set/,
      ],
      [
        "unknown log mask",
        "create log-mask m\nset m.mask FATAL\nactivate m\n",
        /unknown log mask/,
      ],
      [
        "proxy-socks without interface",
        "create proxy-socks s\nactivate s\n",
        /proxy-socks requires an activated interface/,
      ],
      [
        "monitor without interface",
        "create monitor m\nactivate m\n",
        /monitor requires an activated interface/,
      ],
      [
        "activate twice",
        'create interface if0\nset if0.family IP\nset if0.host "h"\nset if0.port "1"\nactivate if0\nactivate if0\n',
        /already activated/,
      ],
      [
        "wrong create arity",
        "create interface\n",
        /wrong number of words in create/,
      ],
      [
        "wrong set arity",
        'create interface if0\nset if0.host "a" "b"\n',
        /wrong number of words in set/,
      ],
      [
        "wrong activate arity",
        "activate a b\n",
        /wrong number of words in activate/,
      ],
      [
        "reference without member",
        "create interface if0\nset if0.host $bad\n",
        /reference to invalid object or member/,
      ],
      [
        "pointer to unknown object",
        "create proxy x\nset x.interface nonexistent\nactivate x\n",
        /references unknown object/,
      ],
    ];
    for (const [name, text, pattern] of cases) {
      expect(() => new ClassicParser().parse(text), name).toThrow(pattern);
    }
  });

  it("rejects an unknown proxy type and a missing peer", () => {
    const iface =
      'create interface if0\nset if0.family IP\nset if0.host "h"\nset if0.port "1"\nactivate if0\n';
    const peer =
      'create peer p0\nset p0.family IP\nset p0.host "h"\nset p0.port "2"\nactivate p0\n';
    expect(() =>
      new ClassicParser().parse(
        `${iface}${peer}create proxy x\nset x.type UDP-UDP\nset x.interface if0\nset x.peer p0\nactivate x\n`,
      ),
    ).toThrow(/unknown proxy type/);
    expect(() =>
      new ClassicParser().parse(
        `${iface}create proxy x\nset x.interface if0\nactivate x\n`,
      ),
    ).toThrow(/proxy requires an activated peer/);
  });

  it("rejects a non-activated codec referenced as interface_codec", () => {
    const text =
      'create interface if0\nset if0.family IP\nset if0.host "h"\nset if0.port "1"\nactivate if0\n' +
      'create peer p0\nset p0.family IP\nset p0.host "h"\nset p0.port "2"\nactivate p0\n' +
      "create codec c0\nset c0.codec None\n" +
      "create proxy x\nset x.interface if0\nset x.peer p0\nset x.interface_codec c0\nactivate x\n";
    expect(() => new ClassicParser().parse(text)).toThrow(
      /interface_codec must be an activated codec/,
    );
  });
});

describe("tokenizeLine", () => {
  it("splits on whitespace and stops at comments", () => {
    expect(tokenizeLine("set if0.host x")).toEqual(["set", "if0.host", "x"]);
    expect(tokenizeLine("  create   cache c0  ")).toEqual([
      "create",
      "cache",
      "c0",
    ]);
    expect(tokenizeLine("# a comment")).toEqual([]);
    expect(tokenizeLine("activate x # trailing")).toEqual(["activate", "x"]);
    expect(tokenizeLine("   ")).toEqual([]);
  });
});

describe("unquote", () => {
  it("strips surrounding double quotes only", () => {
    expect(unquote('"localhost"')).toBe("localhost");
    expect(unquote("localhost")).toBe("localhost");
    expect(unquote('"')).toBe('"');
    expect(unquote('""')).toBe("");
  });
});
