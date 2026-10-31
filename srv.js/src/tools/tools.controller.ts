import {
  Body,
  Controller,
  InternalServerErrorException,
  NotFoundException,
  Post,
} from '@nestjs/common';

import { ToolsService } from './tools.service.js';

@Controller('api/tools')
export class ToolsController {
  public constructor(private readonly tools: ToolsService) {}

  @Post('analyze')
  public async analyze(@Body() body: { fileId: string }) {
    try {
      return await this.tools.analyze(body.fileId);
    } catch (error) {
      if (error instanceof NotFoundException) {
        throw error;
      }
      throw new InternalServerErrorException(
        error instanceof Error ? error.message : String(error),
      );
    }
  }
}
