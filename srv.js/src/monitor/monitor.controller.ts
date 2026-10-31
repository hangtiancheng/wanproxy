import { BadGatewayException, Controller, Get } from '@nestjs/common';

import { MonitorService } from './monitor.service.js';

@Controller('api/monitor')
export class MonitorController {
  public constructor(private readonly monitor: MonitorService) {}

  @Get('status')
  public async status() {
    try {
      return await this.monitor.status();
    } catch (error) {
      throw new BadGatewayException((error as Error).message);
    }
  }

  @Get('scenarios')
  public scenarios() {
    return this.monitor.scenarios();
  }
}
