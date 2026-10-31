import { Body, ConflictException, Controller, Post } from '@nestjs/common';

import { BenchService } from '../bench/bench.service.js';
import { DaemonService } from './daemon.service.js';

@Controller('api/daemon')
export class DaemonController {
  public constructor(
    private readonly daemon: DaemonService,
    private readonly bench: BenchService,
  ) {}

  @Post('restart')
  public async restart(@Body() body: { clearCache?: boolean } | undefined) {
    if (this.bench.busy()) {
      throw new ConflictException('a benchmark or experiment is running');
    }
    const clearCache = body?.clearCache === true;
    await this.daemon.restart(clearCache);
    return { clearedCache: clearCache, ok: true };
  }
}
