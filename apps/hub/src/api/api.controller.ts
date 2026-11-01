import { BadRequestException, Body, Controller, Delete, Get, HttpCode, NotFoundException, Param, Post, Put, Query, Req, Res, UnauthorizedException } from '@nestjs/common';
import * as fs from 'fs';
import * as path from 'path';
import { AgentInfo, Bundle, Series } from '../contract';
import { Auth, Public, SESSION_COOKIE } from '../auth';
import { HubService } from '../hub.service';
import { Push } from '../engine/protocol';
import { describe, diagnose, NotFound, path as pathView, why } from '../engine/views';
import { linkName } from '../engine/evaluate';

/** Parse `at`: RFC 3339 ("2026-10-22T02:03Z"), relative ("-2h", "90m"), or unix ms. */
export function parseAt(v: string | undefined): number | null {
  if (!v) return null;
  const rel = /^-?(\d+(?:\.\d+)?)(s|m|h|d)$/.exec(v.trim());
  if (rel) return Date.now() - parseFloat(rel[1]) * ({ s: 1e3, m: 6e4, h: 3.6e6, d: 8.64e7 } as any)[rel[2]];
  if (/^\d{12,}$/.test(v)) return Number(v);
  const t = Date.parse(v);
  if (Number.isNaN(t)) throw new BadRequestException(`invalid time "${v}" (use RFC 3339 like 2026-10-22T02:03Z, or -2h)`);
  if (t > Date.now() + 60_000) throw new BadRequestException('time is in the future');
  return t;
}

function parseDuration(v: string | undefined, def: number): number {
  if (!v) return def;
  const m = /^(\d+(?:\.\d+)?)(s|m|h|d)$/.exec(v.trim().replace(/^-/, ''));
  if (!m) throw new BadRequestException(`invalid duration "${v}" (use e.g. 30m, 2h, 1d)`);
  return parseFloat(m[1]) * ({ s: 1e3, m: 6e4, h: 3.6e6, d: 8.64e7 } as any)[m[2]];
}

function wrap<T>(fn: () => T): T {
  try {
    return fn();
  } catch (e) {
    if (e instanceof NotFound) throw new NotFoundException(e.message);
    throw e;
  }
}

@Controller('api')
export class ApiController {
  constructor(
    private hub: HubService,
    private auth: Auth,
  ) {}

  // ------------------------------------------------------------- health & auth

  @Public()
  @Get('health')
  health() {
    return { ok: this.hub.ready, mode: this.hub.cfg.mode, topologyVersion: this.hub.version, tsdb: this.hub.tsdb?.name, db: this.hub.db?.driver };
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
  async status(@Query('at') at?: string) {
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return {
      at: snap.at,
      live: snap.live,
      mode: snap.mode,
      topology: snap.topology,
      overall: snap.overall,
      routes: bundle.routes.map((r) => ({ ...snap.routes[r.id], title: r.title, impact: r.impact, squads: r.squads ?? [] })),
      alerts: snap.alerts,
      drift: snap.drift,
    };
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
  async describe(@Param('id') id: string, @Query('at') at?: string) {
    const t = parseAt(at);
    const { snap, bundle } = await this.hub.snapshotAt(t);
    const ent = wrap(() => describe(bundle, snap, id, []));
    const kinds = ent.kind === 'node' ? ['node'] : ent.kind === 'link' ? ['link'] : ['route'];
    const evs = await this.hub.db.events({ entity: { kind: kinds[0], id: ent.id }, until: t ? new Date(t) : undefined, limit: 20 });
    ent.events = evs.filter((e) => e.kind !== 'state' || e.entityKind !== 'check');
    return ent;
  }

  @Get('why/:id')
  async why(@Param('id') id: string, @Query('at') at?: string) {
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return { id, at: snap.at, reasons: wrap(() => why(bundle, snap, id)) };
  }

  @Get('path')
  async path(@Query('from') from: string, @Query('to') to: string, @Query('route') route?: string, @Query('at') at?: string) {
    if (!from || !to) throw new BadRequestException('from and to are required');
    const { snap, bundle } = await this.hub.snapshotAt(parseAt(at));
    return wrap(() => pathView(bundle, snap, from, to, route));
  }

  @Get('diagnose')
  async diagnose(@Query('at') at?: string) {
    const t = parseAt(at);
    const { snap, bundle } = await this.hub.snapshotAt(t);
    const end = t ?? Date.now();
    const recent = (await this.hub.db.events({ since: new Date(end - 2 * 3_600_000), until: new Date(end), limit: 200 })).filter((e) => !(e.kind === 'state' && e.entityKind === 'check')).slice(0, 25);
    return diagnose(bundle, snap, recent);
  }

  @Get('events')
  async events(@Query('since') since?: string, @Query('until') until?: string, @Query('entity') entity?: string, @Query('kind') kind?: string, @Query('limit') limit?: string, @Query('checks') checks?: string) {
    const end = parseAt(until) ?? Date.now();
    const start = since ? end - parseDuration(since, 7_200_000) : end - 7_200_000;
    let ent: { kind: string; id: string } | undefined;
    if (entity) {
      const n = this.hub.bundle.nodes.find((x) => x.id === entity);
      const l = this.hub.bundle.links.find((x) => x.id === entity);
      const r = this.hub.bundle.routes.find((x) => x.id === entity);
      ent = n ? { kind: 'node', id: entity } : l ? { kind: 'link', id: entity } : r ? { kind: 'route', id: entity } : undefined;
      if (!ent) throw new NotFoundException(`no node, link or route "${entity}"`);
    }
    const list = await this.hub.db.events({ since: new Date(start), until: new Date(end), entity: ent, kinds: kind ? kind.split(',') : undefined, limit: Number(limit ?? 500) });
    return checks === 'true' ? list : list.filter((e) => !(e.kind === 'state' && e.entityKind === 'check'));
  }

  /** Series for one entity (default keys) or explicit keys=a,b. */
  @Get('metrics')
  async metrics(@Query('entity') entity?: string, @Query('keys') keys?: string, @Query('since') since?: string, @Query('until') until?: string, @Query('step') step?: string): Promise<Series[]> {
    const end = parseAt(until) ?? Date.now();
    const span = parseDuration(since, 3_600_000);
    const stepMs = step ? parseDuration(step, 60_000) : Math.max(10_000, Math.round(span / 240 / 10_000) * 10_000);
    const defs = keys ? keys.split(',').map((k) => ({ key: k, label: k, unit: '' })) : entity ? this.defaultSeries(entity) : [];
    if (!defs.length) throw new BadRequestException('give entity=<id> or keys=<series keys>');
    const m = await this.hub.tsdb.range(defs.map((d) => d.key), end - span, end, stepMs);
    return defs.map((d) => ({ ...d, points: m.get(d.key) ?? [] }));
  }

  private defaultSeries(id: string): { key: string; label: string; unit: string }[] {
    const b = this.hub.bundle;
    const n = b.nodes.find((x) => x.id === id);
    if (n) {
      const out: { key: string; label: string; unit: string }[] = [];
      if (n.agent) out.push({ key: `n:${id}:cpu`, label: 'CPU', unit: '%' }, { key: `n:${id}:mem`, label: 'Memory', unit: '%' }, { key: `n:${id}:net`, label: 'Network', unit: 'bps' });
      for (const m of n.metrics ?? []) out.push({ key: `m:${m.key}`, label: m.label ?? m.id, unit: m.collector === 'remnawave-online' ? 'users' : m.collector === 'ss-conns' ? 'conns' : '' });
      for (const c of n.checks ?? []) if (['http-probe', 'tcp-connect', 'xbridger-connection'].includes(c.collector)) out.push({ key: `c:${c.key}:v`, label: c.title ?? c.id, unit: 'ms' });
      return out;
    }
    const l = b.links.find((x) => x.id === id || x.label?.toLowerCase() === id.toLowerCase());
    if (l) {
      const out = [
        { key: `l:${l.id}:up`, label: 'Up', unit: 'bps' },
        { key: `l:${l.id}:down`, label: 'Down', unit: 'bps' },
      ];
      if (l.conns) out.push({ key: `l:${l.id}:conns`, label: 'Connections', unit: 'conns' });
      for (const c of l.health ?? []) if (['tcp-connect', 'http-probe', 'xbridger-connection'].includes(c.collector)) out.push({ key: `c:${c.key}:v`, label: c.title ?? c.id, unit: 'ms' });
      for (const c of l.health ?? []) if (c.collector === 'wg-handshake') out.push({ key: `c:${c.key}:v`, label: 'Handshake age', unit: 's' });
      return out;
    }
    const r = b.routes.find((x) => x.id === id);
    if (r) return [
      { key: `r:${id}:users`, label: 'Users', unit: 'users' },
      { key: `r:${id}:lat`, label: 'Probe latency', unit: 'ms' },
      { key: `r:${id}:fail`, label: 'Probe failures', unit: '' },
    ];
    throw new NotFoundException(`no node, link or route "${id}"`);
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
