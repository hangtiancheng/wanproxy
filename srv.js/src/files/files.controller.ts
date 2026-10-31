import { createReadStream, statSync } from 'node:fs';

import {
  BadRequestException,
  Body,
  Controller,
  Delete,
  Get,
  HttpCode,
  Param,
  Post,
  Query,
  Req,
  Res,
} from '@nestjs/common';
import type { Request, Response } from 'express';

import { parsePattern, type Pattern } from '../common/payload.js';
import { FilesService } from './files.service.js';

const MAX_TRANSFER_SIZE = 4 * 1024 * 1024 * 1024;
const DEFAULT_SIZE = 16 * 1024 * 1024;

interface GenerateBody {
  name?: string;
  size?: number;
  pattern?: string;
  seed?: number;
}

@Controller('api/files')
export class FilesController {
  public constructor(private readonly files: FilesService) {}

  @Get()
  public list() {
    return this.files.list();
  }

  @Post('generate')
  @HttpCode(201)
  public async generate(@Body() body: GenerateBody) {
    let pattern: Pattern;
    try {
      pattern = parsePattern(body.pattern ?? 'mixed');
    } catch (error) {
      throw new BadRequestException((error as Error).message);
    }
    const size = body.size ?? DEFAULT_SIZE;
    if (!Number.isInteger(size) || size < 1 || size > MAX_TRANSFER_SIZE) {
      throw new BadRequestException('invalid size');
    }
    const seed = body.seed ?? 42;
    const name = body.name ?? `test-${pattern}.bin`;
    return this.files.generate(name, size, pattern, seed);
  }

  @Post('upload')
  @HttpCode(201)
  public async upload(
    @Query('name') name: string | undefined,
    @Req() req: Request,
  ) {
    const fileName = name !== undefined && name !== '' ? name : 'upload.bin';
    return this.files.store(fileName, req);
  }

  @Delete(':id')
  @HttpCode(204)
  public remove(@Param('id') id: string): void {
    this.files.remove(id);
  }

  @Get(':id/content')
  public content(
    @Param('id') id: string,
    @Req() req: Request,
    @Res() res: Response,
  ): void {
    let meta;
    try {
      meta = this.files.get(id);
    } catch {
      res.status(404).json({ message: `file ${id} not found` });
      return;
    }
    const path = this.files.filePath(id);
    let total: number;
    try {
      total = statSync(path).size;
    } catch {
      res.status(404).json({ message: `file ${id} not found` });
      return;
    }

    res.setHeader('Accept-Ranges', 'bytes');
    res.setHeader('Content-Type', this.files.contentType(meta.name));
    res.setHeader(
      'Content-Disposition',
      `inline; filename="${encodeURIComponent(meta.name)}"`,
    );

    const rangeHeader = req.headers.range;
    let start = 0;
    let end = total - 1;
    let partial = false;
    if (rangeHeader !== undefined && rangeHeader !== '') {
      const match = /bytes=(\d*)-(\d*)/.exec(rangeHeader);
      if (match) {
        start = match[1] !== '' && match[1] !== undefined ? Number.parseInt(match[1], 10) : 0;
        end = match[2] !== '' && match[2] !== undefined ? Number.parseInt(match[2], 10) : total - 1;
      }
      if (start >= total || end >= total || start > end) {
        res.setHeader('Content-Range', `bytes */${total}`);
        res.status(416).end();
        return;
      }
      partial = true;
    }

    if (partial) {
      res.setHeader('Content-Range', `bytes ${start}-${end}/${total}`);
      res.setHeader('Content-Length', String(end - start + 1));
      res.status(206);
      createReadStream(path, { end, start }).pipe(res);
      return;
    }
    res.setHeader('Content-Length', String(total));
    res.status(200);
    createReadStream(path).pipe(res);
  }
}
