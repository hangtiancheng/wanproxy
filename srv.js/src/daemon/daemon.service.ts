/**
 * DaemonService orchestrates the demo infrastructure: it ensures wanproxy.js
 * is built, renders the demo config, spawns the wanproxy.js daemon as a
 * child process, and guarantees the child never outlives this process.  It
 * is the TypeScript port of srv/internal/daemon/daemon.go, driving
 * wanproxy.js instead of the Go binaries.
 */

import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs';

import {
  Injectable,
  Logger,
  type OnApplicationBootstrap,
  type OnApplicationShutdown,
} from '@nestjs/common';

import { ensureDirs, getPaths, monitorPort, srvPort } from '../common/paths.js';

@Injectable()
export class DaemonService implements OnApplicationBootstrap, OnApplicationShutdown {
  private readonly logger = new Logger('daemon');
  private child: ChildProcess | undefined;
  private stopped = false;
  private restartLock: Promise<void> = Promise.resolve();

  public async onApplicationBootstrap(): Promise<void> {
    if (process.env.SRV_NO_DAEMON === '1') {
      this.logger.warn('SRV_NO_DAEMON=1; skipping wanproxy.js daemon start');
      return;
    }
    try {
      await this.start();
    } catch (error) {
      this.logger.error(`startup failed: ${String(error)}`);
      await this.stop();
      throw error;
    }
  }

  public async onApplicationShutdown(): Promise<void> {
    await this.stop();
  }

  /** start brings up the wanproxy.js fleet and waits for its monitor. */
  public async start(): Promise<void> {
    ensureDirs();
    this.ensureBuilt();
    this.renderConfig();
    this.stopped = false;
    this.spawnWanproxy();
    await this.waitForHttp(monitorPort(), 15000);
    this.logger.log(`wanproxy.js daemon up (monitor on :${monitorPort()})`);
  }

  /**
   * restart stops the fleet and brings it up again.  When clearCache is set
   * the XCDFS volume is deleted first so the next run starts with a cold
   * dedup cache.  Restarts are serialized against each other.
   */
  public restart(clearCache: boolean): Promise<void> {
    const run = this.restartLock.then(async () => {
      await this.stop();
      this.child = undefined;
      if (clearCache) {
        rmSync(getPaths().xcachePath, { force: true });
        this.logger.log(`cleared XCDFS volume ${getPaths().xcachePath}`);
      }
      this.logger.log(`restarting wanproxy.js fleet (clearCache=${clearCache})`);
      await this.start();
    });
    this.restartLock = run.catch(() => undefined);
    return run;
  }

  /** stop tears down the wanproxy.js child (SIGTERM, then SIGKILL). */
  public async stop(): Promise<void> {
    if (this.stopped) {
      return;
    }
    this.stopped = true;
    const child = this.child;
    if (child === undefined || child.pid === undefined || child.exitCode !== null) {
      return;
    }
    this.logger.log(`stopping wanproxy.js daemon (pid ${child.pid})`);
    const exited = new Promise<void>((resolve) => {
      child.once('exit', () => resolve());
    });
    child.kill('SIGTERM');
    const timedOut = await Promise.race([
      exited.then(() => false),
      new Promise<boolean>((resolve) => setTimeout(() => resolve(true), 3000)),
    ]);
    if (timedOut) {
      this.logger.warn('wanproxy.js did not exit in 3s; sending SIGKILL');
      child.kill('SIGKILL');
      await exited;
    }
    this.logger.log('wanproxy.js daemon stopped');
  }

  /** isRunning reports whether the child process is alive. */
  public isRunning(): boolean {
    return this.child !== undefined && this.child.exitCode === null;
  }

  private ensureBuilt(): void {
    const p = getPaths();
    if (existsSync(p.wanproxyCli)) {
      return;
    }
    this.logger.log('wanproxy.js dist missing; building (pnpm build)...');
    const attempt = spawnSync('pnpm', ['build'], {
      cwd: p.wanproxyJsRoot,
      stdio: 'inherit',
    });
    if (attempt.status !== 0 || !existsSync(p.wanproxyCli)) {
      const fallback = spawnSync('npm', ['run', 'build'], {
        cwd: p.wanproxyJsRoot,
        stdio: 'inherit',
      });
      if (fallback.status !== 0 || !existsSync(p.wanproxyCli)) {
        throw new Error(
          `could not build wanproxy.js at ${p.wanproxyJsRoot}; run pnpm build there first`,
        );
      }
    }
  }

  private renderConfig(): void {
    const p = getPaths();
    const rendered = readFileSync(p.templatePath, 'utf8')
      .replaceAll('{{SRV_PORT}}', String(srvPort()))
      .replaceAll('{{MONITOR_PORT}}', String(monitorPort()))
      .replaceAll('{{XCACHE_PATH}}', p.xcachePath);
    writeFileSync(p.configPath, rendered);
    this.logger.log(`rendered daemon config at ${p.configPath}`);
  }

  private spawnWanproxy(): void {
    const p = getPaths();
    const child = spawn(process.execPath, [p.wanproxyCli, p.configPath], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    this.child = child;
    this.pipeLines(child.stdout, 'wanproxy');
    this.pipeLines(child.stderr, 'wanproxy');
    child.on('exit', (code, signal) => {
      if (!this.stopped) {
        this.logger.warn(`wanproxy.js exited (code=${code} signal=${signal})`);
      }
    });
    child.on('error', (error) => {
      this.logger.error(`wanproxy.js spawn error: ${String(error)}`);
    });
  }

  private pipeLines(
    stream: NodeJS.ReadableStream | null,
    label: string,
  ): void {
    if (stream === null) {
      return;
    }
    let buffer = '';
    stream.setEncoding('utf8');
    stream.on('data', (chunk: string) => {
      buffer += chunk;
      let idx = buffer.indexOf('\n');
      while (idx >= 0) {
        const line = buffer.slice(0, idx).replace(/\r$/, '');
        buffer = buffer.slice(idx + 1);
        if (line.trim() !== '') {
          this.logger.log(`[${label}] ${line}`);
        }
        idx = buffer.indexOf('\n');
      }
    });
  }

  private async waitForHttp(port: number, timeoutMs: number): Promise<void> {
    const deadline = Date.now() + timeoutMs;
    const url = `http://127.0.0.1:${port}/status`;
    while (Date.now() < deadline) {
      try {
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), 500);
        const res = await fetch(url, { signal: controller.signal });
        clearTimeout(timer);
        if ((res.status >= 200 && res.status < 300) || res.status === 404) {
          return;
        }
      } catch {
        // not up yet; retry
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error(
      `wanproxy.js monitor did not come up on :${port} within ${timeoutMs}ms`,
    );
  }
}
