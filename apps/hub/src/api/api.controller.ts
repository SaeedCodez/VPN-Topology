import { BadRequestException, Body, Controller, Delete, Get, HttpCode, NotFoundException, Param, Post, Put, Query, Req, Res, UnauthorizedException } from '@nestjs/common';
import * as fs from 'fs';
import * as path from 'path';
import { AgentInfo, Bundle, Series } from '../contract';
import { Auth, Public, SESSION_COOKIE } from '../auth';
import { HubService } from '../hub.service';
import { Push } from '../engine/protocol';
import { parseAt, parseDuration, Queries } from '../queries';
import { AssistantPatch } from '../assistant/settings';
import { OpsPatch } from '../ops/settings';
import { linkName } from '../engine/evaluate';
import { selectionFrom } from '../board/image';
import { NotFound } from '../engine/views';

export { parseAt };

@Controller('api')
export class ApiController {
  private readonly q: Queries;
  constructor(
    private hub: HubService,
    private auth: Auth,
  ) {
    this.q = new Queries(hub);
  }

  // ------------------------------------------------------------- health & auth

  @Public()
  @Get('health')
  health() {
    return { ok: this.hub.ready, mode: this.hub.cfg.mode, topologyVersion: this.hub.version, tsdb: this.hub.tsdb?.name, db: this.hub.db?.driver, telegram: this.hub.telegram ? 'on' : 'off', ops: this.hub.ops?.enabled ? 'on' : 'off' };
  }

  @Public()
  @Post('auth/login')
  @HttpCode(200)
  login(@Req() req: any, @Res({ passthrough: true }) res: any, @Body() body: { password?: string; totp?: string }) {
    if (this.hub.cfg.authDisabled) return { ok: true, demo: true };
    const ip = String(req.ip ?? req.socket?.remoteAddress ?? '');
    if (!this.auth.login(ip, String(body?.password ?? ''), String(body?.totp ?? ''))) throw new UnauthorizedException('wrong password or code (5 attempts per 15 minutes)');
    const s = this.auth.issueSession();
    res.cookie(SESSION_COOKIE, s.value, { httpOnly: true, secure: this.hub.cfg.publicUrl.startsWith('https://'), sameSite: 'strict', maxAge: s.maxAge, path: '/' });
    return { ok: true };
  }

  @Public()
  @Post('auth/logout')
  @HttpCode(200)
  logout(@Res({ passthrough: true }) res: any) {
    res.clearCookie(SESSION_COOKIE, { path: '/' });
    return { ok: true };
  }

  @Public()
  @Get('auth/me')
  me(@Req() req: any) {
    return { authenticated: this.auth.isAuthorized(req), authDisabled: this.hub.cfg.authDisabled, mode: this.hub.cfg.mode };
  }

  // ------------------------------------------------------------- the board

  @Get('snapshot')
  async snapshot(@Query('at') at?: string) {
    return (await this.hub.snapshotAt(parseAt(at))).snap;
  }

  @Get('status')
  status(@Query('at') at?: string) {
    return this.q.status(parseAt(at));
  }

  @Get('topology')
  topology() {
    return { version: this.hub.version, bundle: this.hub.bundle };
  }

  @Get('topology/versions')
  versions() {
    return this.hub.history();
  }

  @Get('topology/versions/:v')
  async version(@Param('v') v: string) {
    const b = await this.hub.db.topologyVersion(Number(v));
    if (!b) throw new NotFoundException(`no topology version ${v}`);
    return { version: Number(v), bundle: b };
  }

  /** `vpnmap apply`: a validated, committed bundle becomes a new version. Changes only the hub. */
  @Post('topology')
  async apply(@Body() body: { bundle: Bundle; message?: string; by?: string }) {
    if (!body?.bundle) throw new BadRequestException('body must be {bundle, message}');
    try {
      return await this.hub.apply(body.bundle, String(body.message ?? ''), String(body.by ?? 'cli'));
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }

  @Post('topology/rollback')
  async rollback(@Body() body: { version: number; by?: string }) {
    const b = await this.hub.rollback(Number(body?.version), String(body?.by ?? 'cli'));
    if (!b) throw new NotFoundException(`no topology version ${body?.version}`);
    return { version: Number(body.version), hash: b.hash };
  }

  @Get('nodes')
  async nodes(@Query('at') at?: string) {
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return bundle.nodes.map((n) => ({ spec: n, state: snap.nodes[n.id] }));
  }

  @Get('links')
  async links(@Query('at') at?: string) {
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return bundle.links.map((l) => ({ spec: l, name: linkName(l), state: snap.links[l.id] }));
  }

  @Get('routes')
  async routes(@Query('at') at?: string) {
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return bundle.routes.map((r) => ({ spec: r, state: snap.routes[r.id] }));
  }

  @Get('alerts')
  async alerts(@Query('at') at?: string) {
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return { active: snap.alerts, rules: bundle.alerts };
  }

  @Get('components')
  components() {
    const b = this.hub.bundle;
    return b.components.map((c) => ({
      ...c,
      nodes: b.nodes.filter((n) => n.defined_in.component === c.name).map((n) => n.id),
      links: b.links.filter((l) => l.defined_in.component === c.name).map((l) => l.id),
      routes: b.routes.filter((r) => r.defined_in.component === c.name).map((r) => r.id),
      alerts: b.alerts.filter((a) => a.defined_in.component === c.name).map((a) => a.id),
    }));
  }

  @Get('describe/:id')
  describe(@Param('id') id: string, @Query('at') at?: string) {
    return this.q.describe(id, parseAt(at));
  }

  @Get('why/:id')
  why(@Param('id') id: string, @Query('at') at?: string) {
    return this.q.why(id, parseAt(at));
  }

  @Get('path')
  path(@Query('from') from: string, @Query('to') to: string, @Query('route') route?: string, @Query('at') at?: string) {
    return this.q.path(from, to, route, parseAt(at));
  }

  @Get('diagnose')
  diagnose(@Query('at') at?: string) {
    return this.q.diagnose(parseAt(at));
  }

  @Get('events')
  events(@Query('since') since?: string, @Query('until') until?: string, @Query('entity') entity?: string, @Query('kind') kind?: string, @Query('limit') limit?: string, @Query('checks') checks?: string) {
    return this.q.events({ sinceMs: parseDuration(since, 7_200_000), until: parseAt(until), entity, kinds: kind ? kind.split(',') : undefined, limit: Number(limit ?? 500), checks: checks === 'true' });
  }

  /** Series for one entity (default keys) or explicit keys=a,b. */
  @Get('metrics')
  metrics(@Query('entity') entity?: string, @Query('keys') keys?: string, @Query('since') since?: string, @Query('until') until?: string, @Query('step') step?: string): Promise<Series[]> {
    return this.q.metrics({ entity, keys: keys ? keys.split(',') : undefined, spanMs: parseDuration(since, 3_600_000), until: parseAt(until), stepMs: step ? parseDuration(step, 60_000) : undefined });
  }

  @Get('agents')
  agents(): AgentInfo[] {
    const now = Date.now();
    return [...this.hub.store.agents.values()].map((a) => ({
      node: a.node,
      kind: a.push.agent.kind,
      version: a.push.agent.version,
      hostname: a.push.agent.hostname,
      configSha: a.push.agent.config_sha,
      lastSeen: new Date(a.lastSeen).toISOString(),
      ageSec: Math.round((now - a.lastSeen) / 1000),
      capabilities: a.push.agent.capabilities as any,
    }));
  }

  @Get('drift')
  drift() {
    return this.hub.current().drift;
  }

  @Get('layout')
  layout() {
    return this.hub.db.layout();
  }

  /** The UI saves node positions here; `vpnmap layout pull` writes them to topology/layout.yaml. */
  @Put('layout')
  async saveLayout(@Body() body: Record<string, { x: number; y: number }>) {
    const clean: Record<string, { x: number; y: number }> = {};
    const ids = new Set(this.hub.bundle.nodes.map((n) => n.id));
    for (const [k, v] of Object.entries(body ?? {})) {
      if (ids.has(k) && Number.isFinite(v?.x) && Number.isFinite(v?.y)) clean[k] = { x: Math.round(v.x), y: Math.round(v.y) };
    }
    await this.hub.db.saveLayout(clean);
    return { saved: Object.keys(clean).length };
  }

  /** Forget all saved positions (the board falls back to auto-layout). */
  @Delete('layout')
  async clearLayout() {
    await this.hub.db.clearLayout();
    return { cleared: true };
  }

  @Post('silence')
  async silence(@Body() body: { alert: string; instance?: string; duration?: string; reason?: string }) {
    try {
      const until = await this.hub.silence(body.alert, body.instance ?? '', parseDuration(body.duration, 3_600_000), body.reason ?? '');
      return { alert: body.alert, until: new Date(until).toISOString() };
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }

  // ------------------------------------------------------------- board pictures

  /**
   * A picture of the board (PNG, or SVG with format=svg): the whole board, or
   * route=, from=&to=[&via=a,b], nodes=a,b, links=e1,e3 (combinable).
   * theme=light|dark, context=true keeps the rest dimmed, quality=file for full size.
   */
  @Get('board/image')
  async boardImage(@Query() q: Record<string, string>, @Res() res: any) {
    const img = await this.image(() =>
      this.hub.images.render(selectionFrom(q), { at: parseAt(q.at), theme: q.theme === 'dark' ? 'dark' : 'light', context: q.context === 'true', labels: q.labels !== 'false', quality: q.quality === 'file' ? 'file' : 'photo' }),
    );
    res.setHeader('Cache-Control', 'no-store');
    if (q.format === 'svg') {
      res.setHeader('Content-Type', 'image/svg+xml; charset=utf-8');
      res.end(img.svg);
      return;
    }
    res.setHeader('Content-Type', 'image/png');
    if (q.download === 'true') res.setHeader('Content-Disposition', `attachment; filename="vpn-board-${img.at.slice(0, 16).replace('T', '-').replace(':', '')}.png"`);
    res.end(img.png);
  }

  /** The board's "Send to Telegram": the same picture, to the alert chat. */
  @Post('board/image/telegram')
  async boardImageTelegram(@Body() body: Record<string, any>) {
    const r = await this.image(() =>
      this.hub.sendBoardImage(selectionFrom(body ?? {}), {
        at: parseAt(body?.at),
        theme: body?.theme === 'dark' ? 'dark' : 'light',
        context: body?.context === true,
        labels: body?.labels !== false,
        asFile: body?.asFile === true ? true : 'auto',
        caption: typeof body?.caption === 'string' ? body.caption.slice(0, 900) : undefined,
        by: typeof body?.by === 'string' ? body.by.slice(0, 64) : 'board',
      }),
    );
    if (!r.result.ok) throw new BadRequestException(`Telegram: ${r.result.error}`);
    return { sent: true, asFile: r.asFile, ...r.image };
  }

  private async image<T>(fn: () => Promise<T>): Promise<T> {
    try {
      return await fn();
    } catch (e: any) {
      if (e instanceof NotFound) throw new NotFoundException(e.message);
      if (e?.status) throw e;
      throw new BadRequestException(e?.message ?? String(e));
    }
  }

  // ------------------------------------------------------------- telegram

  @Get('telegram')
  telegram() {
    return this.hub.telegramStatus();
  }

  /** `vpnmap telegram test`: one test message to the alert chat. */
  @Post('telegram/test')
  async telegramTest(@Body() body: { by?: string }) {
    try {
      return await this.hub.telegramTest(String(body?.by ?? 'cli').slice(0, 64));
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }

  // ------------------------------------------------------------- assistant

  @Get('assistant')
  assistant() {
    return this.hub.assistant.view();
  }

  /** Change assistant settings from the board. `key` is write-only ("" clears it). */
  @Put('assistant')
  async updateAssistant(@Body() body: AssistantPatch & { by?: string }) {
    try {
      const { by, ...patch } = body ?? {};
      return await this.hub.assistant.update(patch, String(by ?? 'board').slice(0, 64));
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }

  @Get('assistant/models')
  async assistantModels() {
    try {
      return await this.hub.assistant.models();
    } catch (e: any) {
      throw new BadRequestException(`cannot load the OpenRouter model list: ${e.message}`);
    }
  }

  @Post('assistant/test')
  assistantTest() {
    return this.hub.assistant.test();
  }

  /** Reads the Notion root page once (token valid, page connected to the integration). */
  @Post('assistant/docs-test')
  assistantDocsTest() {
    return this.hub.assistant.docsTest();
  }

  // ------------------------------------------------------------- ops (docs/ops-actions.md)
  // Behind the global auth guard; every write is refused in demo mode (which
  // has no login) except the kill switch.

  @Get('ops')
  ops() {
    return this.hub.ops.view();
  }

  @Put('ops')
  async updateOps(@Body() body: OpsPatch & { by?: string }, @Req() req: any) {
    // Approvers, access, host keys and the accepted policy decide who can
    // change a server: only the owner's login session, never the CLI token.
    // The kill switch alone is allowed with any credential.
    const killOnly = body && Object.keys(body).filter((k) => k !== 'by').length === 1 && body.enabled === false;
    if (!killOnly && !this.auth.isAdminSession(req)) throw new UnauthorizedException('changing ops settings needs the board login (password + TOTP), not a token');
    try {
      const { by, ...patch } = body ?? {};
      const v = await this.hub.ops.update(patch, String(by ?? 'board').slice(0, 64));
      this.hub.assistant.refresh();
      return v;
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }

  @Get('ops/servers/:node/grant')
  opsGrant(@Param('node') node: string, @Req() req: any) {
    if (!this.auth.isAdminSession(req)) throw new UnauthorizedException('needs the board login (password + TOTP)');
    if (this.hub.cfg.mode === 'demo') throw new BadRequestException('ops actions are disabled in demo mode');
    const g = this.hub.ops.grantFor(node);
    if (!g) throw new NotFoundException(`${node} has no runner, or ops has no SSH key yet (switch ops on first)`);
    return g;
  }

  @Post('ops/servers/:node/test')
  async opsTest(@Param('node') node: string) {
    try {
      return await this.hub.ops.serverTest(node);
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }

  @Get('ops/tasks')
  opsTasks() {
    return this.hub.ops.tasks();
  }

  // ------------------------------------------------------------- demo

  @Get('demo/scenarios')
  scenarios() {
    return this.hub.scenarios();
  }

  @Post('demo/play')
  play(@Body() body: { scenario: string }) {
    try {
      const run = this.hub.play(body?.scenario);
      return { scenario: run.scenario.id, start: new Date(run.start).toISOString(), durationSec: run.scenario.durationSec };
    } catch (e: any) {
      throw new BadRequestException(e.message);
    }
  }
}

/** Agent ingest: per-agent token, one-way. The response carries no instructions. */
@Controller('api/ingest/v1')
export class IngestController {
  constructor(
    private hub: HubService,
    private auth: Auth,
  ) {}

  @Public()
  @Post('push')
  @HttpCode(200)
  push(@Req() req: any, @Body() body: Push) {
    const token = String(req.headers?.authorization ?? '').replace(/^Bearer /, '');
    const node = this.auth.agentForToken(token);
    if (!node) throw new UnauthorizedException('unknown agent token');
    if (!body || body.v !== 1 || body.agent?.node !== node) throw new BadRequestException(`payload must be protocol v1 from node ${node}`);
    if (this.hub.cfg.mode === 'demo') throw new BadRequestException('the hub runs in demo mode; real agents are ignored');
    this.hub.ingest(body, Date.now());
    return { ok: true, received: new Date().toISOString() };
  }
}

/** Agent install files, served from AGENT_DIST_DIR (built in the hub image). */
@Controller('agent')
export class AgentFilesController {
  constructor(private hub: HubService) {}

  @Public()
  @Get(':file')
  file(@Param('file') file: string, @Res() res: any) {
    this.send(file, res);
  }

  @Public()
  @Get('download/:file')
  download(@Param('file') file: string, @Res() res: any) {
    this.send(file, res);
  }

  private send(file: string, res: any) {
    if (!/^[a-z0-9][a-z0-9._-]{0,80}$/.test(file)) throw new NotFoundException();
    const p = path.join(this.hub.cfg.agentDistDir, file);
    if (!fs.existsSync(p)) throw new NotFoundException(`${file} is not built into this hub (AGENT_DIST_DIR)`);
    res.setHeader('Content-Type', file.endsWith('.sh') || file.endsWith('.sha256') ? 'text/plain; charset=utf-8' : 'application/octet-stream');
    fs.createReadStream(p).pipe(res);
  }
}
