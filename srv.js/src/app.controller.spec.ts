import { Test, type TestingModule } from '@nestjs/testing';
import { describe, expect, it } from 'vitest';

import { AppController } from './app.controller.js';

describe('AppController', () => {
  it('reports service health at the root', async () => {
    const module: TestingModule = await Test.createTestingModule({
      controllers: [AppController],
    }).compile();
    const controller = module.get<AppController>(AppController);
    expect(controller.root()).toEqual({
      service: 'wanproxy-server',
      status: 'ok',
    });
  });
});
