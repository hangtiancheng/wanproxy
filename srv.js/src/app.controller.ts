import { Controller, Get } from '@nestjs/common';

@Controller()
export class AppController {
  @Get()
  public root(): { service: string; status: string } {
    return { service: 'wanproxy-server', status: 'ok' };
  }
}
