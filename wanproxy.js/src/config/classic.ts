/**
 * ClassicParser parses the C++ wanproxy.conf directive format:
 *
 *   create <class> <name>
 *   set <name>.<member> <value>
 *   activate <name>
 *
 * with # comments, quoted strings and $object.member references.  It is a
 * port of the Go config/classic.go parser; the C++ original is the source
 * of truth for the accepted classes, members and error messages.
 */

import { readFileSync } from "node:fs";

import type { LogLevel } from "../common/log.js";
import { CodecStats } from "../pipeline/codec-stats.js";
import { RsaHostKey } from "../ssh/crypto.js";
import { CACHE_ID_LENGTH } from "../xcodec/cache-interface.js";
import type { XCodecCache } from "../xcodec/cache-interface.js";
import { XCODEC_SEGMENT_LENGTH } from "../xcodec/constants.js";
import { XCodecDisk } from "../xcodec/disk-cache.js";
import {
  MemoryCache,
  type MemoryCacheOptions,
} from "../xcodec/memory-cache.js";
import { PairCache } from "../xcodec/pair-cache.js";
import type {
  CodecMode,
  CodecSpec,
  ConfigObject,
  DiskHandle,
  Endpoint,
  ObjectField,
  RuntimeConfig,
  RuntimeMonitorConfig,
  RuntimeProxyConfig,
  RuntimeSocksConfig,
  SSHProxySettings,
} from "./model.js";
import { parseAddressFamily } from "./model.js";
import { parseSize } from "./size.js";

/** The config classes the C++ parser recognizes. */
const CLASSIC_CLASSES = new Set([
  "log-mask",
  "cache",
  "codec",
  "interface",
  "peer",
  "proxy",
  "proxy-socks",
  "monitor",
]);

const LEVEL_ORDER: readonly LogLevel[] = ["debug", "info", "warn", "error"];

/** classicObject is one created-but-not-yet-activated config object. */
interface ClassicObject {
  readonly name: string;
  readonly class: string;
  readonly fields: Map<string, string>;
  readonly order: string[];
  kind: string;
  cache: XCodecCache | undefined;
  codec: CodecSpec | undefined;
  address: Endpoint | undefined;
}

export interface ClassicParserOptions {
  /**
   * Override how a server host key file is read (used by tests to avoid
   * touching the filesystem).  Defaults to a synchronous file read.
   */
  readonly readFile?: (path: string) => Uint8Array;
}

export class ClassicParser {
  private readonly objects = new Map<string, ClassicObject>();
  /** fieldStrings mirrors the C++ field_strings_map_ used to resolve $refs. */
  private readonly fieldStrings = new Map<string, string>();
  private readonly proxies: RuntimeProxyConfig[] = [];
  private readonly socksProxies: RuntimeSocksConfig[] = [];
  private readonly exportedObjects: ConfigObject[] = [];
  private readonly caches: XCodecCache[] = [];
  private readonly disks: DiskHandle[] = [];
  private readonly readFile: (path: string) => Uint8Array;
  private monitor: RuntimeMonitorConfig | undefined;
  private logLevel: LogLevel = "info";

  public constructor(options: ClassicParserOptions = {}) {
    this.readFile =
      options.readFile ?? ((path) => new Uint8Array(readFileSync(path)));
  }

  /** parse parses a whole wanproxy.conf document. */
  public parse(text: string): RuntimeConfig {
    const lines = text.split("\n");
    for (let index = 0; index < lines.length; index++) {
      const line = lines[index] ?? "";
      const tokens = tokenizeLine(line);
      if (tokens.length === 0) {
        continue;
      }
      try {
        this.parseDirective(tokens);
      } catch (error) {
        throw new Error(
          `line ${index + 1}: ${JSON.stringify(line)}: ${errorMessage(error)}`,
        );
      }
    }
    if (this.proxies.length === 0 && this.socksProxies.length === 0) {
      throw new Error("configuration defines no proxies");
    }
    return {
      caches: this.caches,
      disks: this.disks,
      logLevel: this.logLevel,
      monitor: this.monitor,
      objects: this.exportedObjects,
      proxies: this.proxies,
      socksProxies: this.socksProxies,
    };
  }

  private parseDirective(tokens: readonly string[]): void {
    switch (tokens[0]) {
      case "create":
        return this.parseCreate(tokens.slice(1));
      case "set":
        return this.parseSet(tokens.slice(1));
      case "activate":
        return this.parseActivate(tokens.slice(1));
      default:
        throw new Error(`unrecognized configuration command "${tokens[0]}"`);
    }
  }

  private parseCreate(tokens: readonly string[]): void {
    if (tokens.length !== 2) {
      throw new Error(`wrong number of words in create (${tokens.length})`);
    }
    const [klass, name] = tokens as [string, string];
    if (!CLASSIC_CLASSES.has(klass)) {
      throw new Error(`class (${klass}) not present`);
    }
    if (this.objects.has(name)) {
      throw new Error(`refusing to create object (${name}) twice`);
    }
    this.objects.set(name, {
      address: undefined,
      cache: undefined,
      class: klass,
      codec: undefined,
      fields: new Map(),
      kind: "",
      name,
      order: [],
    });
  }

  private parseSet(tokens: readonly string[]): void {
    if (tokens.length !== 2) {
      throw new Error(`wrong number of words in set (${tokens.length})`);
    }
    const identifier = tokens[0] as string;
    let value = tokens[1] as string;
    const dot = identifier.indexOf(".");
    if (dot < 0) {
      throw new Error(
        `identifier (${identifier}) is not an object member identifier`,
      );
    }
    const name = identifier.slice(0, dot);
    const member = identifier.slice(dot + 1);
    if (name === "" || member === "") {
      throw new Error(`object (${name}) or member name (${member}) is empty`);
    }

    if (value.startsWith("$")) {
      value = this.resolveReference(value);
    }

    const obj = this.objects.get(name);
    if (obj === undefined) {
      throw new Error(
        `can not set member value on object (${name}) that does not exist`,
      );
    }
    if (!obj.fields.has(member)) {
      obj.order.push(member);
    }
    obj.fields.set(member, value);
    this.fieldStrings.set(`${name}.${member}`, value);
  }

  private resolveReference(value: string): string {
    const ref = value.slice(1);
    const refDot = ref.indexOf(".");
    if (refDot <= 0 || refDot === ref.length - 1) {
      throw new Error(
        `reference to invalid object or member by name (${value})`,
      );
    }
    const refName = ref.slice(0, refDot);
    const refMember = ref.slice(refDot + 1);
    if (!this.objects.has(refName)) {
      throw new Error(`reference to non-existant object (${refName})`);
    }
    const resolved = this.fieldStrings.get(`${refName}.${refMember}`);
    if (resolved === undefined) {
      throw new Error(
        `reference to unset member (${refMember}) in object (${refName})`,
      );
    }
    return resolved;
  }

  private parseActivate(tokens: readonly string[]): void {
    if (tokens.length !== 1) {
      throw new Error(`wrong number of words in activate (${tokens.length})`);
    }
    const name = tokens[0] as string;
    const obj = this.objects.get(name);
    if (obj === undefined) {
      throw new Error(
        `object (${name}) can not be activated as it does not exist`,
      );
    }
    try {
      this.activate(obj);
    } catch (error) {
      throw new Error(
        `object (${name}) activation failed: ${errorMessage(error)}`,
      );
    }
    this.exportedObjects.push({
      class: obj.class,
      fields: objectFields(obj),
      name: obj.name,
    });
  }

  private activate(obj: ClassicObject): void {
    switch (obj.class) {
      case "log-mask":
        return this.activateLogMask(obj);
      case "cache":
        return this.activateCache(obj);
      case "codec":
        return this.activateCodec(obj);
      case "interface":
      case "peer":
        return this.activateAddress(obj);
      case "proxy":
        return this.activateProxy(obj);
      case "proxy-socks":
        return this.activateProxySocks(obj);
      case "monitor":
        return this.activateMonitor(obj);
      default:
        throw new Error(`unknown class "${obj.class}"`);
    }
  }

  private activateLogMask(obj: ClassicObject): void {
    const raw = this.str(obj, "mask");
    const level = maskToLogLevel(raw);
    // The most detailed mask wins, like applying Log::mask per regex.
    if (levelRank(level) < levelRank(this.logLevel)) {
      this.logLevel = level;
    }
    obj.kind = "log-mask";
  }

  private activateCache(obj: ClassicObject): void {
    if (obj.kind !== "") {
      throw new Error("already activated");
    }
    const cacheType = this.str(obj, "type") || "Memory";
    const uuid = parseCacheUuid(unquote(this.str(obj, "uuid")));

    switch (cacheType) {
      case "Memory": {
        if (this.str(obj, "path") !== "") {
          throw new Error("no path parameter for memory caches");
        }
        if (this.pointer(obj, "primary") !== undefined) {
          throw new Error("specified cache hierarchy for memory cache");
        }
        if (this.pointer(obj, "secondary") !== undefined) {
          throw new Error("specified cache hierarchy for memory cache");
        }
        obj.cache = memoryCacheFromBytes(uuid, this.optionalSize(obj, "size"));
        break;
      }
      case "Disk": {
        if (unquote(this.str(obj, "uuid")) !== "") {
          throw new Error(
            "cannot specify UUID for disk cache; a persistent UUID will be allocated",
          );
        }
        const path = unquote(this.str(obj, "path"));
        if (path === "") {
          throw new Error("disk cache requires a path");
        }
        const disk = XCodecDisk.open(path, this.optionalSize(obj, "size"));
        this.disks.push(disk);
        obj.cache = disk.local();
        break;
      }
      case "Pair": {
        if (unquote(this.str(obj, "uuid")) !== "") {
          throw new Error(
            "cannot specify UUID for cache pair; secondary UUID will be used",
          );
        }
        if (this.str(obj, "size") !== "") {
          throw new Error("no size parameter for cache pair");
        }
        const primary = this.pointer(obj, "primary");
        const secondary = this.pointer(obj, "secondary");
        if (primary === undefined || secondary === undefined) {
          throw new Error(
            "cache pair requires both primary and secondary cache",
          );
        }
        if (primary.cache === undefined) {
          throw new Error(
            `cache (${primary.name}) must be activated prior to use as a primary cache`,
          );
        }
        if (secondary.cache === undefined) {
          throw new Error(
            `cache (${secondary.name}) must be activated prior to use as a secondary cache`,
          );
        }
        obj.cache = new PairCache(primary.cache, secondary.cache);
        break;
      }
      case "None":
        obj.cache = undefined;
        break;
      default:
        throw new Error(`unknown cache type "${cacheType}"`);
    }

    obj.kind = "cache";
    if (obj.cache !== undefined) {
      this.caches.push(obj.cache);
    }
  }

  private activateCodec(obj: ClassicObject): void {
    if (obj.kind !== "") {
      throw new Error("already activated");
    }
    let mode: CodecMode;
    switch (this.str(obj, "codec")) {
      case "":
      case "None":
        mode = "none";
        break;
      case "XCodec":
        mode = "xcodec";
        break;
      default:
        throw new Error(`unknown codec "${this.str(obj, "codec")}"`);
    }

    const compressor = this.str(obj, "compressor");
    switch (compressor) {
      case "":
      case "None":
        break;
      case "zlib":
        mode = mode === "xcodec" ? "zlib-xcodec" : "zlib";
        break;
      default:
        throw new Error(`unknown compressor "${compressor}"`);
    }

    let compressorLevel = -1;
    const levelRaw = this.str(obj, "compressor_level");
    if (levelRaw !== "") {
      const trimmed = levelRaw.trim();
      if (!/^[+-]?[0-9]+$/.test(trimmed)) {
        throw new Error(`invalid compressor_level "${levelRaw}"`);
      }
      compressorLevel = Number.parseInt(trimmed, 10);
    }

    let cache: XCodecCache | undefined;
    if (mode === "xcodec" || mode === "zlib-xcodec") {
      const cacheObject = this.pointer(obj, "cache");
      if (cacheObject === undefined || cacheObject.cache === undefined) {
        throw new Error("codec requires an activated cache");
      }
      cache = cacheObject.cache;
    }

    let trackStatistics = false;
    let stats: CodecStats | undefined;
    switch (this.str(obj, "track_statistics").toLowerCase()) {
      case "true":
      case "yes":
        trackStatistics = true;
        stats = new CodecStats();
        break;
      case "":
      case "false":
      case "no":
        break;
      default:
        throw new Error(
          `invalid track_statistics "${this.str(obj, "track_statistics")}"`,
        );
    }

    obj.codec = { cache, compressorLevel, mode, stats, trackStatistics };
    obj.kind = "codec";
  }

  private activateAddress(obj: ClassicObject): void {
    if (obj.kind !== "") {
      throw new Error("already activated");
    }
    const family = parseAddressFamily(this.str(obj, "family"));
    const endpoint: Endpoint = {
      family,
      host: unquote(this.str(obj, "host")),
      path: unquote(this.str(obj, "path")),
      port: unquote(this.str(obj, "port")),
    };
    if (family === "Unix") {
      if (endpoint.host !== "" || endpoint.port !== "") {
        throw new Error(
          "Unix domain socket has host and/or port field set, which is only valid for IP sockets",
        );
      }
    } else if (endpoint.path !== "") {
      throw new Error(
        "IP socket has path field set, which is only valid for Unix domain sockets",
      );
    }
    obj.address = endpoint;
    obj.kind = "address";
  }

  private activateProxy(obj: ClassicObject): void {
    if (obj.kind !== "") {
      throw new Error("already activated");
    }
    const proxyType = this.str(obj, "type") || "TCP-TCP";
    let ssh: SSHProxySettings | undefined;
    switch (proxyType) {
      case "TCP":
      case "TCP-TCP":
        if (unquote(this.str(obj, "server_host_key")) !== "") {
          throw new Error("server_host_key is only valid for SSH proxies");
        }
        break;
      case "SSH":
      case "SSH-SSH":
        ssh = this.loadHostKey(obj);
        break;
      default:
        throw new Error(`unknown proxy type "${proxyType}"`);
    }

    const iface = this.pointer(obj, "interface");
    if (iface === undefined || iface.address === undefined) {
      throw new Error("proxy requires an activated interface");
    }
    const peer = this.pointer(obj, "peer");
    if (peer === undefined || peer.address === undefined) {
      throw new Error("proxy requires an activated peer");
    }
    if (iface.address.host === "" || iface.address.port === "") {
      throw new Error("proxy interface endpoint is incomplete");
    }
    if (peer.address.host === "" || peer.address.port === "") {
      throw new Error("proxy peer endpoint is incomplete");
    }

    const interfaceCodec = this.activatedCodec(obj, "interface_codec");
    const peerCodec = this.activatedCodec(obj, "peer_codec");

    this.proxies.push({
      interface: iface.address,
      interfaceCodec,
      name: obj.name,
      peer: peer.address,
      peerCodec,
      ssh,
    });
    obj.kind = "proxy";
  }

  private activatedCodec(
    obj: ClassicObject,
    member: string,
  ): CodecSpec | undefined {
    const codecObject = this.pointer(obj, member);
    if (codecObject === undefined) {
      return undefined;
    }
    if (codecObject.codec === undefined) {
      throw new Error(`${member} must be an activated codec`);
    }
    return codecObject.codec;
  }

  private loadHostKey(obj: ClassicObject): SSHProxySettings {
    let keyPath = unquote(this.str(obj, "server_host_key"));
    if (keyPath === "") {
      // For historical reasons the C++ version looks for the server host
      // key at this path by default.
      keyPath = "ssh-server1.pem";
    }
    let pemData: Uint8Array;
    try {
      pemData = this.readFile(keyPath);
    } catch (error) {
      throw new Error(
        `could not load server host key "${keyPath}": ${errorMessage(error)}`,
      );
    }
    let hostKey: RsaHostKey;
    try {
      hostKey = RsaHostKey.fromPrivatePem(pemData);
    } catch (error) {
      throw new Error(
        `invalid server host key "${keyPath}": ${errorMessage(error)}`,
      );
    }
    return { hostKey, hostKeyPath: keyPath };
  }

  private activateProxySocks(obj: ClassicObject): void {
    if (obj.kind !== "") {
      throw new Error("already activated");
    }
    const iface = this.pointer(obj, "interface");
    if (iface === undefined || iface.address === undefined) {
      throw new Error("proxy-socks requires an activated interface");
    }
    this.socksProxies.push({ interface: iface.address, name: obj.name });
    obj.kind = "proxy-socks";
  }

  private activateMonitor(obj: ClassicObject): void {
    if (obj.kind !== "") {
      throw new Error("already activated");
    }
    const iface = this.pointer(obj, "interface");
    if (iface === undefined || iface.address === undefined) {
      throw new Error("monitor requires an activated interface");
    }
    this.monitor = { interface: iface.address, name: obj.name };
    obj.kind = "monitor";
  }

  private str(obj: ClassicObject, member: string): string {
    return obj.fields.get(member) ?? "";
  }

  private pointer(
    obj: ClassicObject,
    member: string,
  ): ClassicObject | undefined {
    const value = obj.fields.get(member);
    if (value === undefined || value === "None") {
      return undefined;
    }
    const target = this.objects.get(value);
    if (target === undefined) {
      throw new Error(`member ${member} references unknown object "${value}"`);
    }
    return target;
  }

  private optionalSize(obj: ClassicObject, member: string): number {
    const raw = this.str(obj, member);
    if (raw === "") {
      return 0;
    }
    return parseSize(raw);
  }
}

/**
 * tokenizeLine splits a config line into whitespace-separated words,
 * stopping at # comments, mirroring the C++ istringstream tokenizer.
 */
export function tokenizeLine(line: string): string[] {
  const tokens: string[] = [];
  for (const word of line.trim().split(/\s+/)) {
    if (word === "") {
      continue;
    }
    if (word.startsWith("#")) {
      break;
    }
    tokens.push(word);
  }
  return tokens;
}

function objectFields(obj: ClassicObject): ObjectField[] {
  return obj.order.map((member) => ({
    name: member,
    type: "string",
    value: obj.fields.get(member) ?? "",
  }));
}

/** unquote strips the surrounding quotes required by the C++ string type. */
export function unquote(value: string): string {
  if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
    return value.slice(1, -1);
  }
  return value;
}

function maskToLogLevel(mask: string): LogLevel {
  switch (mask.toUpperCase()) {
    case "DEBUG":
      return "debug";
    case "INFO":
    case "NOTICE":
    case "":
      return "info";
    case "WARNING":
    case "ERR":
    case "CRIT":
    case "ALERT":
    case "EMERG":
      return "warn";
    default:
      throw new Error(`unknown log mask "${mask}"`);
  }
}

function levelRank(level: LogLevel): number {
  return LEVEL_ORDER.indexOf(level);
}

/**
 * parseCacheUuid maps the C++ uuid config member (a 16-character string)
 * onto a cache identity.  An empty value yields undefined so the memory
 * cache generates a fresh random identity, matching the C++ activation.
 */
function parseCacheUuid(value: string): Uint8Array | undefined {
  if (value === "") {
    return undefined;
  }
  if (value.length !== CACHE_ID_LENGTH) {
    throw new Error("specified UUID is invalid");
  }
  return new Uint8Array(Buffer.from(value, "latin1"));
}

/**
 * memoryCacheFromBytes mirrors the C++ constructor which takes a byte
 * limit: limitBytes / SegmentLength segments, at least one when a non-zero
 * limit smaller than a segment is requested, and unbounded when zero.
 */
function memoryCacheFromBytes(
  uuid: Uint8Array | undefined,
  limitBytes: number,
): MemoryCache {
  let maxSegments: number | undefined;
  if (limitBytes > 0) {
    maxSegments = Math.max(1, Math.floor(limitBytes / XCODEC_SEGMENT_LENGTH));
  }
  const options: MemoryCacheOptions = {};
  if (maxSegments !== undefined) {
    (options as { maxSegments?: number }).maxSegments = maxSegments;
  }
  if (uuid !== undefined) {
    (options as { uuid?: Uint8Array }).uuid = uuid;
  }
  return new MemoryCache(options);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
