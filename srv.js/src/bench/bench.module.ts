import { Module } from '@nestjs/common';

import { MonitorModule } from '../monitor/monitor.module.js';
import { BenchController } from './bench.controller.js';
import { BenchService } from './bench.service.js';

@Module({
  imports: [MonitorModule],
  controllers: [BenchController],
  exports: [BenchService],
  providers: [BenchService],
})
export class BenchModule {}
