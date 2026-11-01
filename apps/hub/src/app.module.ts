import { Module } from '@nestjs/common';
import { APP_GUARD } from '@nestjs/core';
import { AgentFilesController, ApiController, IngestController } from './api/api.controller';
import { Auth, AuthGuard } from './auth';
import { Config } from './config';
import { HubService } from './hub.service';

export function appModule(cfg: Config, hub: HubService, auth: Auth) {
  @Module({
    controllers: [ApiController, IngestController, AgentFilesController],
    providers: [
      { provide: HubService, useValue: hub },
      { provide: Auth, useValue: auth },
      { provide: 'CONFIG', useValue: cfg },
      { provide: APP_GUARD, useClass: AuthGuard },
    ],
  })
  class AppModule {}
  return AppModule;
}
