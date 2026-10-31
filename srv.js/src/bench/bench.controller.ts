import { createHash } from 'node:crypto';

import {
  BadRequestException,
  Body,
  ConflictException,
  Controller,
  Get,
  HttpCode,
  NotFoundException,
  Post,
  Query,
  Req,
  Res,
} from '@nestjs/common';
import type { Request, Response } from 'express';

import {
  createPayloadStream,
  parsePattern,
  type Pattern,
} from '../common/payload.js';
import { scenarios } from '../monitor/scenarios.js';
import {
  AlreadyRunningError,
  BenchService,
  UnknownScenarioError,
  type BenchRequest,
  type ExperimentRequest,
} from './bench.service.js';

const MAX_TRANSFER_SIZE = 4 * 1024 * 1024 * 1024;

@Controller('api/bench')
export class BenchController {
  public constructor(private readonly bench: BenchService) {}

  @Get('scenarios')
  public listScenarios() {
    return scenarios();
  }

  @Post('run')
  public async run(@Body() body: BenchRequest) {
    try {
      return await this.bench.run(body);
    } catch (error) {
      throw this.mapError(error);
    }
  }

  @Post('experiment')
  public async experiment(@Body() body: ExperimentRequest) {
    try {
      return await this.bench.runExperiment(body);
    } catch (error) {
      throw this.mapError(error);
    }
  }

  @Post('sink')
  @HttpCode(200)
  public async sink(@Req() req: Request) {
    const hash = createHash('sha256');
    let bytes = 0;
    for await (const chunk of req) {
      const buf = chunk as Buffer;
      hash.update(buf);
      bytes += buf.length;
    }
    return { bytes, sha256: hash.digest('hex') };
  }

  @Get('source')
  public source(
    @Query('size') sizeStr: string | undefined,
    @Query('pattern') patternStr: string | undefined,
    @Query('seed') seedStr: string | undefined,
    @Res() res: Response,
  ): void {
    const size = Number.parseInt(sizeStr ?? '', 10);
    if (!Number.isFinite(size) || size < 0 || size > MAX_TRANSFER_SIZE) {
      res.status(400).json({ message: 'invalid size' });
      return;
    }
    let pattern: Pattern;
    try {
      pattern = parsePattern(patternStr || 'mixed');
    } catch (error) {
      res.status(400).json({ message: (error as Error).message });
      return;
    }
    const parsedSeed =
      seedStr !== undefined ? Number.parseInt(seedStr, 10) : 42;
    const seed = Number.isFinite(parsedSeed) ? parsedSeed : 42;
    res.setHeader('Content-Type', 'application/octet-stream');
    res.setHeader('Content-Length', String(size));
    res.status(200);
    createPayloadStream(pattern, size, seed).pipe(res);
  }

  private mapError(error: unknown) {
    if (error instanceof UnknownScenarioError) {
      return new NotFoundException(error.message);
    }
    if (error instanceof AlreadyRunningError) {
      return new ConflictException(error.message);
    }
    return new BadRequestException(
      error instanceof Error ? error.message : String(error),
    );
  }
}
