import 'reflect-metadata';
import * as fs from 'fs';
import * as path from 'path';
import { NestFactory } from '@nestjs/core';
import { NestExpressApplication } from '@nestjs/platform-express';
import { WebSocketServer } from 'ws';
import { appModule } from './app.module';
import { Auth } from './auth';
import { loadConfig } from './config';
import { HubService } from './hub.service';

async function main() {
  const cfg = loadConfig();
  const hub = new HubService(cfg);
  const auth = new Auth(cfg);
  console.log(`vpnmap hub starting in ${cfg.mode} mode (topology root ${cfg.repoRoot})`);
  await hub.init();

  const app = await NestFactory.create<NestExpressApplication>(appModule(cfg, hub, auth), { logger: ['error', 'warn', 'log'] });
  app.set('trust proxy', 1);
  app.useBodyParser('json', { limit: '5mb' });
  app.use((req: any, res: any, next: any) => {
    // Private board: never indexed, never framed.
    res.setHeader('X-Robots-Tag', 'noindex, nofollow, noarchive');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('X-Frame-Options', 'DENY');
    res.setHeader('Referrer-Policy', 'no-referrer');
    if (cfg.publicUrl.startsWith('https://')) res.setHeader('Strict-Transport-Security', 'max-age=31536000');
    if (req.path === '/robots.txt') return res.type('text/plain').send('User-agent: *\nDisallow: /\n');
    next();
  });
  if (cfg.webOrigin) app.enableCors({ origin: cfg.webOrigin, credentials: true });
  if (cfg.webDistDir && fs.existsSync(cfg.webDistDir)) {
    app.useStaticAssets(cfg.webDistDir, { index: false });
    app.use((req: any, res: any, next: any) => {
      if (req.method === 'GET' && !req.path.startsWith('/api') && !req.path.startsWith('/agent') && !req.path.startsWith('/ws')) return res.sendFile(path.join(cfg.webDistDir!, 'index.html'));
      next();
    });
  }
  await app.listen(cfg.port);

  // Live updates: one WebSocket channel, same auth as the API.
  const wss = new WebSocketServer({ noServer: true, maxPayload: 4096 });
  app.getHttpServer().on('upgrade', (req: any, socket: any, head: any) => {
    const url = new URL(req.url ?? '/', 'http://x');
    if (url.pathname !== '/ws') return socket.destroy();
    const token = url.searchParams.get('token');
    const authed = auth.isAuthorized(req) || (token !== null && auth.validApiToken(token));
    if (!authed) {
      socket.write('HTTP/1.1 401 Unauthorized\r\n\r\n');
      return socket.destroy();
    }
    wss.handleUpgrade(req, socket, head, (ws) => {
      try {
        ws.send(JSON.stringify({ type: 'snapshot', data: hub.current() }));
      } catch {
        /* starting */
      }
      const off = hub.subscribe((msg) => {
        if (ws.readyState === ws.OPEN) ws.send(JSON.stringify(msg));
      });
      ws.on('close', off);
      ws.on('error', off);
    });
  });
  console.log(`vpnmap hub listening on :${cfg.port} (${cfg.mode}, db ${hub.db.driver}, tsdb ${hub.tsdb.name}, auth ${cfg.authDisabled ? 'DISABLED (demo)' : 'on'})`);

  const shutdown = async () => {
    await app.close();
    await hub.stop();
    process.exit(0);
  };
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
