#!/usr/bin/env node
/**
 * boot.js — WANProxy demo stack orchestrator.
 *
 * All startup logic for both backends lives here:
 *   Go stack: srv.go (yukino_http SRV) + wanproxy.go proxy fleet
 *   JS stack: srv.js (NestJS SRV)      + wanproxy.js proxy fleet
 * plus the shared Vite web front-end.
 *
 * The Makefile is a thin wrapper: `make <target>` -> `node boot.js <target>`.
 *
 * Usage:
 *   node boot.js <command> [--stack=go|js] [--srv-port=N] [--web-port=N]
 *                          [--monitor-port=N] [--ssh-echo-port=N]
 *
 * Commands:
 *   dev       backend + web in the foreground (Ctrl-C stops both)
 *   srv       backend only, foreground
 *   web       web front-end only, foreground
 *   start     backend + web backgrounded (logs + pidfiles under <srv>/data)
 *   stop      stop the background stack (pidfiles + port-scan fallback)
 *   restart   stop + start
 *   status    show listeners on the srv/web/monitor ports
 *   logs      tail the background logs
 *   open      open the web front-end in a browser
 *   install   pnpm install + build both backends
 *   build     build the selected backend + web
 *   test      test the selected backend + web
 *   clean     remove build artifacts (runtime data is kept)
 *   help      this message
 */

import { spawn, spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  openSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = dirname(fileURLToPath(import.meta.url));

// --------------------------------------------------------------------- config

const argv = process.argv.slice(2);
const command = argv[0] ?? "help";

function opt(name, def) {
  const prefix = `--${name}=`;
  const eq = argv.find((a) => a.startsWith(prefix));
  if (eq !== undefined) return eq.slice(prefix.length);
  const i = argv.indexOf(`--${name}`);
  if (i >= 0 && argv[i + 1] !== undefined) return argv[i + 1];
  return def;
}

function envInt(name, def) {
  const v = process.env[name];
  const n = v === undefined ? NaN : Number.parseInt(v, 10);
  return Number.isFinite(n) ? n : def;
}

const STACK = String(opt("stack", process.env.STACK ?? "go")).toLowerCase();
const IS_JS = STACK === "js";
const SRV_PORT = Number(opt("srv-port", envInt("SRV_PORT", 3101)));
const WEB_PORT = Number(opt("web-port", envInt("WEB_PORT", 5173)));
const MONITOR_PORT = Number(opt("monitor-port", envInt("MONITOR_PORT", 9901)));
const SSH_ECHO_PORT = Number(
  opt("ssh-echo-port", envInt("SSH_ECHO_PORT", 8349)),
);
const PNPM = process.env.PNPM ?? "pnpm";
const GO = process.env.GO ?? "go";

const SRV_DIR = IS_JS ? "srv.js" : "srv.go";
const DATA_DIR = join(ROOT, SRV_DIR, "data");
const PID_DIR = join(DATA_DIR, "pids");
const SRV_LOG = join(DATA_DIR, "server.log");
const WEB_LOG = join(DATA_DIR, "web.log");
const SRV_PID = join(PID_DIR, "srv.pid");
const WEB_PID = join(PID_DIR, "web.pid");
const GO_BIN = join(ROOT, "srv.go", "bin", "wanproxy-srv");
const JS_MAIN = join(ROOT, "srv.js", "dist", "main.js");
const WEB_DIR = join(ROOT, "web");
const ALL_DATA_DIRS = [
  join(ROOT, "srv.go", "data"),
  join(ROOT, "srv.js", "data"),
];

const log = (msg) => process.stdout.write(`[boot] ${msg}\n`);
function fail(msg) {
  process.stderr.write(`[boot] ${msg}\n`);
  process.exit(1);
}

// -------------------------------------------------------------------- helpers

function runInherit(cmd, args, opts = {}) {
  const res = spawnSync(cmd, args, { stdio: "inherit", cwd: ROOT, ...opts });
  if (res.error) fail(`${cmd} ${args.join(" ")}: ${res.error.message}`);
  if (res.status !== 0)
    fail(`${cmd} ${args.join(" ")} exited with ${res.status}`);
}

function runCapture(cmd, args) {
  return spawnSync(cmd, args, { cwd: ROOT, encoding: "utf8" });
}

function backendEnv() {
  return {
    ...process.env,
    MONITOR_PORT: String(MONITOR_PORT),
    PORT: String(SRV_PORT),
    SRV_PORT: String(SRV_PORT),
    SSH_ECHO_PORT: String(SSH_ECHO_PORT),
  };
}

function backendCommand() {
  return IS_JS
    ? { args: [JS_MAIN], cmd: process.execPath, cwd: ROOT }
    : { args: [], cmd: GO_BIN, cwd: ROOT };
}

function webCommand() {
  return { args: ["dev", "--port", String(WEB_PORT)], cmd: PNPM, cwd: WEB_DIR };
}

function buildGoBackend() {
  log("building wanproxy.go");
  runInherit("make", ["-C", "wanproxy.go", "build"]);
  log("building srv.go");
  mkdirSync(join(ROOT, "srv.go", "bin"), { recursive: true });
  runInherit(GO, ["build", "-o", "bin/wanproxy-srv", "./cmd/server"], {
    cwd: join(ROOT, "srv.go"),
  });
}

function buildJsBackend() {
  log("building wanproxy.js");
  runInherit(PNPM, ["build"], { cwd: join(ROOT, "wanproxy.js") });
  log("building srv.js");
  runInherit(PNPM, ["build"], { cwd: join(ROOT, "srv.js") });
}

function buildBackend() {
  if (IS_JS) buildJsBackend();
  else buildGoBackend();
}

function buildWeb() {
  log("building web");
  runInherit(PNPM, ["build"], { cwd: WEB_DIR });
}

function isAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function readPid(file) {
  try {
    const pid = Number.parseInt(readFileSync(file, "utf8").trim(), 10);
    return Number.isFinite(pid) ? pid : undefined;
  } catch {
    return undefined;
  }
}

function killPid(pid, signal = "SIGTERM") {
  try {
    process.kill(pid, signal);
    return true;
  } catch {
    return false;
  }
}

function portPids(port) {
  const res = runCapture("lsof", ["-ti", `tcp:${port}`, "-s", "tcp:listen"]);
  if (res.status !== 0 || !res.stdout) return [];
  return res.stdout
    .split("\n")
    .map((s) => Number.parseInt(s.trim(), 10))
    .filter((n) => Number.isFinite(n));
}

function sleepSync(ms) {
  spawnSync("sleep", [String(ms / 1000)]);
}

// ------------------------------------------------------------------- commands

function dev() {
  buildBackend();
  log(`backend (${STACK}) -> http://127.0.0.1:${SRV_PORT}`);
  log(`web -> http://localhost:${WEB_PORT}`);
  log("Ctrl-C stops both");

  const be = backendCommand();
  const web = webCommand();
  const backend = spawn(be.cmd, be.args, {
    cwd: be.cwd,
    env: backendEnv(),
    stdio: "inherit",
  });
  const frontend = spawn(web.cmd, web.args, { cwd: web.cwd, stdio: "inherit" });

  let shuttingDown = false;
  const cleanup = () => {
    if (shuttingDown) return;
    shuttingDown = true;
    killPid(backend.pid);
    killPid(frontend.pid);
  };
  process.on("SIGINT", () => {
    cleanup();
    process.exit(0);
  });
  process.on("SIGTERM", () => {
    cleanup();
    process.exit(0);
  });
  backend.on("exit", (code) => {
    log(`backend exited (${code})`);
    cleanup();
    process.exit(code ?? 0);
  });
  frontend.on("exit", (code) => {
    log(`web exited (${code})`);
    cleanup();
    process.exit(code ?? 0);
  });
}

function srvOnly() {
  buildBackend();
  const be = backendCommand();
  runInherit(be.cmd, be.args, { cwd: be.cwd, env: backendEnv() });
}

function webOnly() {
  const web = webCommand();
  runInherit(web.cmd, web.args, { cwd: web.cwd });
}

function spawnDetached(spec, env, logFile) {
  const out = openSync(logFile, "a");
  const child = spawn(spec.cmd, spec.args, {
    cwd: spec.cwd,
    detached: true,
    env,
    stdio: ["ignore", out, out],
  });
  child.unref();
  return child.pid;
}

function start() {
  buildBackend();
  mkdirSync(PID_DIR, { recursive: true });

  const runningSrv = readPid(SRV_PID);
  if (runningSrv !== undefined && isAlive(runningSrv)) {
    log(`backend already running (pid ${runningSrv})`);
  } else {
    const pid = spawnDetached(backendCommand(), backendEnv(), SRV_LOG);
    writeFileSync(SRV_PID, String(pid));
    log(
      `backend (${STACK}) started (pid ${pid}) -> http://127.0.0.1:${SRV_PORT}  log: ${SRV_LOG}`,
    );
  }

  const runningWeb = readPid(WEB_PID);
  if (runningWeb !== undefined && isAlive(runningWeb)) {
    log(`web already running (pid ${runningWeb})`);
  } else {
    const pid = spawnDetached(webCommand(), process.env, WEB_LOG);
    writeFileSync(WEB_PID, String(pid));
    log(
      `web started (pid ${pid}) -> http://localhost:${WEB_PORT}  log: ${WEB_LOG}`,
    );
  }
}

function stop() {
  for (const dir of ALL_DATA_DIRS) {
    for (const name of ["srv.pid", "web.pid"]) {
      const file = join(dir, "pids", name);
      const pid = readPid(file);
      if (pid !== undefined && isAlive(pid)) {
        killPid(pid);
        log(`stopped pid ${pid} (${file})`);
      }
      rmSync(file, { force: true });
    }
  }
  sleepSync(1500);
  for (const port of [SRV_PORT, WEB_PORT, MONITOR_PORT, SSH_ECHO_PORT]) {
    for (const pid of portPids(port)) {
      killPid(pid);
      log(`killed leftover listener on :${port} (pid ${pid})`);
    }
  }
  log("stack stopped");
}

function status() {
  const rows = [
    ["backend", SRV_PORT],
    ["web", WEB_PORT],
    ["monitor", MONITOR_PORT],
  ];
  for (const [label, port] of rows) {
    const pids = portPids(port);
    log(
      `${label} :${port} -> ${pids.length > 0 ? `pid ${pids.join(",")}` : "not listening"}`,
    );
  }
}

function logs() {
  const files = [SRV_LOG, WEB_LOG].filter((f) => existsSync(f));
  if (files.length === 0) {
    log("no logs yet");
    return;
  }
  runInherit("tail", ["-n", "40", "-f", ...files]);
}

function openBrowser() {
  const url = `http://localhost:${WEB_PORT}`;
  const cmd =
    process.platform === "darwin"
      ? "open"
      : process.platform === "win32"
        ? "start"
        : "xdg-open";
  runInherit(cmd, [url]);
}

function install() {
  log("pnpm install (workspace)");
  runInherit(PNPM, ["install"]);
  buildGoBackend();
  buildJsBackend();
}

function test() {
  if (IS_JS) {
    runInherit(PNPM, ["test"], { cwd: join(ROOT, "wanproxy.js") });
    runInherit(PNPM, ["test"], { cwd: join(ROOT, "srv.js") });
    runInherit(PNPM, ["test:e2e"], { cwd: join(ROOT, "srv.js") });
  } else {
    runInherit("make", ["-C", "wanproxy.go", "test"]);
    runInherit(GO, ["vet", "./..."], { cwd: join(ROOT, "srv.go") });
    runInherit(GO, ["test", "./..."], { cwd: join(ROOT, "srv.go") });
  }
  buildWeb();
  runInherit(PNPM, ["lint"], { cwd: WEB_DIR });
}

function clean() {
  runCapture("make", ["-C", "wanproxy.go", "clean"]);
  for (const dir of [
    join(ROOT, "srv.go", "bin"),
    join(ROOT, "wanproxy.js", "dist"),
    join(ROOT, "srv.js", "dist"),
    join(ROOT, "web", "dist"),
  ]) {
    rmSync(dir, { force: true, recursive: true });
  }
  log("cleaned build artifacts (runtime data kept)");
}

function help() {
  process.stdout.write(
    [
      "WANProxy demo stack orchestrator",
      `  current stack: ${STACK} (${IS_JS ? "srv.js + wanproxy.js" : "srv.go + wanproxy.go"})`,
      "",
      "Usage: node boot.js <command> [--stack=go|js] [--srv-port=N] [--web-port=N]",
      "                                  [--monitor-port=N] [--ssh-echo-port=N]",
      "",
      "  dev       backend + web in the foreground (Ctrl-C stops both)",
      "  srv       backend only, foreground",
      "  web       web front-end only, foreground",
      "  start     backend + web backgrounded (logs + pidfiles under <srv>/data)",
      "  stop      stop the background stack (pidfiles + port-scan fallback)",
      "  restart   stop + start",
      "  status    show listeners on the srv/web/monitor ports",
      "  logs      tail the background logs",
      "  open      open the web front-end in a browser",
      "  install   pnpm install + build both backends",
      "  build     build the selected backend + web",
      "  test      test the selected backend + web",
      "  clean     remove build artifacts (runtime data kept)",
      "",
      `  ports: srv=${SRV_PORT} web=${WEB_PORT} monitor=${MONITOR_PORT} sshEcho=${SSH_ECHO_PORT}`,
      "",
    ].join("\n"),
  );
}

// ------------------------------------------------------------------- dispatch

switch (command) {
  case "dev":
    dev();
    break;
  case "srv":
    srvOnly();
    break;
  case "web":
    webOnly();
    break;
  case "start":
    start();
    break;
  case "stop":
    stop();
    break;
  case "restart":
    stop();
    start();
    break;
  case "status":
    status();
    break;
  case "logs":
    logs();
    break;
  case "open":
    openBrowser();
    break;
  case "install":
    install();
    break;
  case "build":
    buildBackend();
    buildWeb();
    break;
  case "test":
    test();
    break;
  case "clean":
    clean();
    break;
  case "help":
  case "--help":
  case "-h":
    help();
    break;
  default:
    process.stderr.write(`unknown command: ${command}\n\n`);
    help();
    process.exit(2);
}
