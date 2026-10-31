import { Module } from '@nestjs/common';

import { BenchModule } from '../bench/bench.module.js';
import { DaemonController } from './daemon.controller.js';
import { DaemonService } from './daemon.service.js';

@Module({
  imports: [BenchModule],
  controllers: [DaemonController],
  exports: [DaemonService],
  providers: [DaemonService],
})
export class DaemonModule {}
