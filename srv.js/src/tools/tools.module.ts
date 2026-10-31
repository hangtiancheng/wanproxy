import { Module } from '@nestjs/common';

import { FilesModule } from '../files/files.module.js';
import { ToolsController } from './tools.controller.js';
import { ToolsService } from './tools.service.js';

@Module({
  imports: [FilesModule],
  controllers: [ToolsController],
  providers: [ToolsService],
})
export class ToolsModule {}
