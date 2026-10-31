import { NestFactory } from '@nestjs/core';

import { AppModule } from './app.module.js';
import { srvPort } from './common/paths.js';

async function bootstrap(): Promise<void> {
  const app = await NestFactory.create(AppModule);
  // Reflect the request Origin so the Vite dev server can call the API.
  app.enableCors({ origin: true });
  app.enableShutdownHooks();
  await app.listen(srvPort(), '127.0.0.1');
}

await bootstrap();
