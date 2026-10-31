/**
 * FilesService persists file metadata to data/files.json and streams file
 * bodies to/from data/files/<id>.  It is the TypeScript port of
 * srv/internal/files/files.go.
 */

import { createHash, randomUUID } from 'node:crypto';
import {
  createWriteStream,
  existsSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { extname, join } from 'node:path';
import { Transform, type Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';

import { Injectable, Logger, NotFoundException } from '@nestjs/common';

import { ensureDirs, getPaths } from '../common/paths.js';
import { createPayloadStream, type Pattern } from '../common/payload.js';

/** FileMeta is one stored file's metadata. */
export interface FileMeta {
  id: string;
  name: string;
  size: number;
  sha256: string;
  pattern?: string;
  seed?: number;
  createdAt: string;
}

const CONTENT_TYPES: Record<string, string> = {
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
  '.mov': 'video/quicktime',
  '.mkv': 'video/x-matroska',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.txt': 'text/plain',
  '.log': 'text/plain',
  '.json': 'application/json',
  '.bin': 'application/octet-stream',
};

@Injectable()
export class FilesService {
  private readonly logger = new Logger('files');
  private metas: FileMeta[] = [];

  public constructor() {
    ensureDirs();
    const metaPath = getPaths().metaPath;
    if (existsSync(metaPath)) {
      try {
        const parsed = JSON.parse(readFileSync(metaPath, 'utf8')) as FileMeta[];
        if (Array.isArray(parsed)) {
          this.metas = parsed;
        }
      } catch (error) {
        this.logger.warn(`could not parse ${metaPath}: ${String(error)}`);
      }
    }
  }

  /** list returns metadata sorted by createdAt descending. */
  public list(): FileMeta[] {
    return [...this.metas].sort((a, b) =>
      a.createdAt < b.createdAt ? 1 : a.createdAt > b.createdAt ? -1 : 0,
    );
  }

  /** get returns the metadata for id, or throws NotFoundException. */
  public get(id: string): FileMeta {
    const meta = this.metas.find((m) => m.id === id);
    if (meta === undefined) {
      throw new NotFoundException(`file ${id} not found`);
    }
    return meta;
  }

  /** filePath returns the on-disk path for a stored file id. */
  public filePath(id: string): string {
    return join(getPaths().filesDir, id);
  }

  /** contentType maps a display name's extension to a MIME type. */
  public contentType(name: string): string {
    return CONTENT_TYPES[extname(name).toLowerCase()] ?? 'application/octet-stream';
  }

  /** store streams an upload body to disk, returning the stored metadata. */
  public store(name: string, body: Readable): Promise<FileMeta> {
    return this.write(name, body);
  }

  /** generate streams the deterministic payload generator to disk. */
  public generate(
    name: string,
    size: number,
    pattern: Pattern,
    seed: number,
  ): Promise<FileMeta> {
    return this.write(name, createPayloadStream(pattern, size, seed), pattern, seed);
  }

  private async write(
    name: string,
    src: Readable,
    pattern?: Pattern,
    seed?: number,
  ): Promise<FileMeta> {
    const id = randomUUID();
    ensureDirs();
    const dest = this.filePath(id);
    const hash = createHash('sha256');
    let size = 0;
    const tap = new Transform({
      transform(chunk, _enc, cb) {
        hash.update(chunk as Buffer);
        size += (chunk as Buffer).length;
        cb(null, chunk);
      },
    });
    try {
      await pipeline(src, tap, createWriteStream(dest));
    } catch (error) {
      rmSync(dest, { force: true });
      throw error;
    }

    const meta: FileMeta = {
      createdAt: new Date().toISOString(),
      id,
      name,
      sha256: hash.digest('hex'),
      size,
    };
    if (pattern !== undefined) {
      meta.pattern = pattern;
      meta.seed = seed;
    }
    this.metas.push(meta);
    this.saveMetas();
    this.logger.log(
      pattern !== undefined
        ? `generated ${name} (${size} bytes, ${pattern}) as ${id}`
        : `stored ${name} (${size} bytes) as ${id}`,
    );
    return meta;
  }

  /** remove deletes a stored file and its metadata. */
  public remove(id: string): void {
    const idx = this.metas.findIndex((m) => m.id === id);
    if (idx < 0) {
      throw new NotFoundException(`file ${id} not found`);
    }
    const meta = this.metas[idx]!;
    const dest = this.filePath(id);
    if (existsSync(dest)) {
      rmSync(dest, { force: true });
    }
    this.metas.splice(idx, 1);
    this.saveMetas();
    this.logger.log(`removed ${meta.name} (${id})`);
  }

  private saveMetas(): void {
    try {
      writeFileSync(
        getPaths().metaPath,
        JSON.stringify(this.metas, null, 2),
      );
    } catch (error) {
      this.logger.error(`write metas: ${String(error)}`);
    }
  }
}
