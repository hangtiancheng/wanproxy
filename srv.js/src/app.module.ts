import { Module } from '@nestjs/common';

import { AppController } from './app.controller.js';
import { BenchModule } from './bench/bench.module.js';
import { DaemonModule } from './daemon/daemon.module.js';
import { FilesModule } from './files/files.module.js';
import { MonitorModule } from './monitor/monitor.module.js';
import { ToolsModule } from './tools/tools.module.js';

@Module({
  imports: [FilesModule, MonitorModule, BenchModule, DaemonModule, ToolsModule],
  controllers: [AppController],
})
export class AppModule {}
