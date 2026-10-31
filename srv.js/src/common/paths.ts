/**
 * paths resolves the repository layout and demo ports.  It is the
 * TypeScript counterpart of srv/internal/paths/paths.go, but it drives the
 * wanproxy.js checkout (node CLIs) instead of the Go binaries.
 *
 * SRV_ROOT is located by walking up from this module (present for both
 * `nest start` and the compiled dist/) looking for demo/wanproxy.conf.template,
 * with an SRV_ROOT environment override and a cwd fallback.
 */

import { existsSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

function looksLikeRoot(dir: string): boolean {
  return existsSync(join(dir, 'demo', 'wanproxy.conf.template'));
}

function walkUp(start: string): string | undefined {
  let dir = start;
  for (;;) {
    if (looksLikeRoot(dir)) {
      return dir;
    }
    const parent = dirname(dir);
    if (parent === dir) {
      return undefined;
    }
    dir = parent;
  }
}

function resolveRoot(): string {
  const env = process.env.SRV_ROOT;
  if (env !== undefined && env !== '') {
    return resolve(env);
  }
  const here = dirname(fileURLToPath(import.meta.url));
  return walkUp(here) ?? walkUp(process.cwd()) ?? process.cwd();
}

let cachedRoot: string | undefined;
let cachedRootEnv: string | undefined;

/** srvRoot returns the srv.js directory, re-resolving when SRV_ROOT changes. */
export function srvRoot(): string {
  const env = process.env.SRV_ROOT;
  if (cachedRoot === undefined || cachedRootEnv !== env) {
    cachedRootEnv = env;
    cachedRoot = resolveRoot();
  }
  return cachedRoot;
}

/** SrvPaths is the resolved set of runtime paths. */
export interface SrvPaths {
  readonly srvRoot: string;
  readonly repoRoot: string;
  readonly wanproxyJsRoot: string;
  readonly dataDir: string;
  readonly filesDir: string;
  readonly toolsDir: string;
  readonly metaPath: string;
  readonly configPath: string;
  readonly hostKeyPath: string;
  readonly xcachePath: string;
  readonly templatePath: string;
  readonly wanproxyCli: string;
  readonly tackCli: string;
  readonly xcdumpCli: string;
}

/** getPaths resolves every runtime path from the current srvRoot(). */
export function getPaths(): SrvPaths {
  const root = srvRoot();
  const repoRoot = dirname(root);
  const wanproxyJsRoot =
    process.env.WANPROXY_JS_ROOT !== undefined &&
    process.env.WANPROXY_JS_ROOT !== ''
      ? resolve(process.env.WANPROXY_JS_ROOT)
      : join(repoRoot, 'wanproxy.js');
  const dataDir = join(root, 'data');
  return {
    configPath: join(dataDir, 'wanproxy.conf'),
    dataDir,
    filesDir: join(dataDir, 'files'),
    hostKeyPath: join(dataDir, 'ssh-hostkey.pem'),
    metaPath: join(dataDir, 'files.json'),
    repoRoot,
    srvRoot: root,
    tackCli: join(wanproxyJsRoot, 'dist', 'cli', 'tack.js'),
    templatePath: join(root, 'demo', 'wanproxy.conf.template'),
    toolsDir: join(dataDir, 'tools'),
    wanproxyCli: join(wanproxyJsRoot, 'dist', 'cli', 'index.js'),
    wanproxyJsRoot,
    xcachePath: join(dataDir, 'wanproxy.xcache'),
    xcdumpCli: join(wanproxyJsRoot, 'dist', 'cli', 'xcdump.js'),
  };
}

function envInt(key: string, def: number): number {
  const v = process.env[key];
  if (v !== undefined && v !== '') {
    const n = Number.parseInt(v, 10);
    if (Number.isFinite(n)) {
      return n;
    }
  }
  return def;
}

/** srvPort is the HTTP listen port (and the origin the proxies forward to). */
export function srvPort(): number {
  const port = process.env.PORT;
  if (port !== undefined && port !== '') {
    const n = Number.parseInt(port, 10);
    if (Number.isFinite(n)) {
      return n;
    }
  }
  return envInt('SRV_PORT', 3101);
}

/** monitorPort is the wanproxy.js monitor port. */
export function monitorPort(): number {
  return envInt('MONITOR_PORT', 9901);
}

/** sshEchoPort is the in-process SSH echo endpoint port. */
export function sshEchoPort(): number {
  return envInt('SSH_ECHO_PORT', 8349);
}

/** ensureDirs creates the runtime data directories. */
export function ensureDirs(): void {
  const p = getPaths();
  for (const dir of [p.dataDir, p.filesDir, p.toolsDir]) {
    mkdirSync(dir, { recursive: true });
  }
}
