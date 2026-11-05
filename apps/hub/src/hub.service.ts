import { execFile } from 'child_process';
import { promisify } from 'util';
import { ActiveAlert, Bundle, DemoScenario, HubEvent, Snapshot, TopologyVersion } from './contract';
import { Config } from './config';
import { Db } from './db/db';
import { AlertEngine } from './engine/alerts';
import { computeDrift } from './engine/drift';
import { buildSnapshot, defaultTitle, linkName } from './engine/evaluate';
import { Observations } from './engine/observations';
import { Push } from './engine/protocol';
import { observe, scanLogs } from './engine/resolve';
import { LatestStore } from './engine/store';
import { MemoryTSDB, STEP_MS, TSDB, VictoriaMetricsTSDB, fromSeries, toSeries } from './engine/tsdb';
import { validateBundle } from './engine/validate';
import { LiveCollectors } from './collectors/live';
import { Simulator } from './sim/simulator';
import { NotifyPlanner, OutMessage, esc, tehranTime } from './engine/notify';
import { SendResult, TelegramSender } from './notify/telegram';
import { AssistantService } from './assistant/service';
import { SCENARIOS } from './sim/scenarios';

const run = promisify(execFile);

type Listener = (msg: { type: 'snapshot'; data: Snapshot } | { type: 'events'; data: HubEvent[] } | { type: 'topology'; data: { version: number } }) => void;

/**
 * The hub's core loop. Every tick (5 s): collect (or simulate) → resolve
 * observations → evaluate the snapshot → alerts → events → store → broadcast.
 * It never sends anything to the servers.
 */
export class HubService {
  bundle!: Bundle;
  version = 0;
  readonly store = new LatestStore();
  tsdb!: TSDB;
  db!: Db;
  readonly alerts = new AlertEngine();
  sim: Simulator | null = null;
  private live: LiveCollectors | null = null;
  private timer: NodeJS.Timeout | null = null;
  private since = new Map<string, number>();
  private incidentSince = new Map<string, number>();
  private announced = new Map<string, string>(); // incident id → root element announced
  private last: Snapshot | null = null;
  private lastObs: Observations | null = null;
  private lastStatus = new Map<string, string>();
  private lastWrite = 0;
  private listeners = new Set<Listener>();
  private pendingEvents: Omit<HubEvent, 'id'>[] = [];
  private baselineCache = new Map<string, number | null>();
  private activeAlerts: ActiveAlert[] = [];
  private lastLogScan = new Map<string, number>();
  private backfilling = false;
  private readonly notifier = new NotifyPlanner();
  /** Set only in live mode with TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID. */
  telegram: TelegramSender | null = null;
  /** Telegram assistant (settings always available; it listens only in live mode). */
  assistant!: AssistantService;
  ready = false;

  constructor(readonly cfg: Config) {}

  // ------------------------------------------------------------------ startup

  async init() {
    this.db = await Db.open(this.cfg.databaseUrl, this.cfg.pgliteDir);
    this.tsdb = this.cfg.victoriaUrl ? new VictoriaMetricsTSDB(this.cfg.victoriaUrl) : new MemoryTSDB();
    const active = this.cfg.mode === 'demo' && !this.cfg.databaseUrl ? null : await this.db.activeTopology();
    if (active) {
      this.bundle = active.bundle;
      this.version = active.version;
    } else {
      const b = await this.bundleFromFiles();
      this.version = await this.db.addTopology(b, 'bootstrap from topology files', 'bootstrap');
      this.bundle = b;
    }
    const layout = await this.db.layout();
    if (!Object.keys(layout).length && Object.keys(this.bundle.layout ?? {}).length) await this.db.saveLayout(this.bundle.layout);
    for (const s of await this.db.activeSilences(new Date())) this.alerts.silences.set(s.instance ? `${s.alertId}|${s.instance}` : s.alertId, s.until.getTime());

    if (this.cfg.mode === 'demo') {
      const now = Date.now();
      const start = now - this.cfg.demoBackfillHours * 3_600_000;
      this.sim = new Simulator(this.bundle, start);
      // Replay the real incidents in the past day so the timeline has history.
      this.sim.schedule('xbridger-reboot-2026-10-13', now - 20 * 3_600_000);
      this.sim.schedule('rw-api-hang-2026-10-20', now - 11 * 3_600_000);
      this.sim.schedule('rw-api-hang-2026-10-22', now - 4 * 3_600_000 - 25 * 60_000);
      this.sim.schedule('game-tunnel-drop', now - 2 * 3_600_000);
      await this.backfill(start, now);
    } else {
      this.live = new LiveCollectors(this.cfg, this.store, () => this.bundle);
      this.live.start();
    }
    if (this.cfg.telegram && this.cfg.mode === 'live') {
      this.telegram = new TelegramSender(this.cfg.telegram);
      // Alerts still firing when the hub stopped fire again after a restart;
      // don't announce them twice.
      const now = Date.now();
      const firing = await this.db.alertsActiveAt(new Date(now)).catch(() => []);
      this.notifier.carryOver(firing.filter((e) => e.entityId).map((e) => ({ key: e.entityId!, since: Date.parse(e.ts) })), now);
      console.log(`telegram: alerts on (${new URL(this.cfg.telegram.apiRoot).host}, ${firing.length} alert(s) already firing)`);
    } else if (this.cfg.telegram) {
      console.log('telegram: configured, but nothing is sent in demo mode');
    } else {
      console.log('telegram: off (set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID)');
    }
    this.assistant = new AssistantService(this, this.telegram);
    await this.assistant.init();
    this.ready = true;
    this.timer = setInterval(() => void this.tick(Date.now()).catch((e) => console.error('tick failed', e)), this.cfg.tickMs);
    await this.tick(Date.now());
  }

  async stop() {
    if (this.timer) clearInterval(this.timer);
    this.assistant?.stop();
    this.live?.stop();
    await this.db?.close();
  }

  /** Load and validate the topology files through the vpnmap binary (the only YAML parser). */
  async bundleFromFiles(): Promise<Bundle> {
    const tries: [string, string[]][] = [
      [this.cfg.vpnmapBin, ['bundle', '--dir', this.cfg.repoRoot]],
      ['go', ['run', './apps/cli', 'bundle', '--dir', this.cfg.repoRoot]],
    ];
    let lastErr: any;
    for (const [bin, args] of tries) {
      try {
        const { stdout } = await run(bin, args, { cwd: this.cfg.repoRoot, maxBuffer: 64 * 1024 * 1024 });
        const b = JSON.parse(stdout) as Bundle;
        validateBundle(b);
        return b;
      } catch (e) {
        lastErr = e;
      }
    }
    throw new Error(`cannot load the topology files with vpnmap (set VPNMAP_BIN): ${lastErr?.stderr || lastErr?.message}`);
  }

  // ------------------------------------------------------------------ demo backfill

  private async backfill(from: number, to: number) {
    this.backfilling = true;
    const t0 = Date.now();
    for (let t = from; t < to; t += STEP_MS) await this.tick(t);
    this.backfilling = false;
    await this.flushEvents();
    console.log(`demo: simulated ${((to - from) / 3_600_000).toFixed(0)} h of history in ${((Date.now() - t0) / 1000).toFixed(1)} s`);
  }

  // ------------------------------------------------------------------ ingest

  ingest(push: Push, now: number) {
    const fx = this.store.ingest(push, now);
    const node = push.agent.node;
    const nodeName = this.bundle.nodes.find((n) => n.id === node)?.name ?? node;
    const ts = new Date(now).toISOString();
    for (const r of fx.reboots) {
      this.pendingEvents.push({ ts: new Date(r.bootTime * 1000).toISOString(), kind: 'reboot', severity: 'warning', entityKind: 'node', entityId: node, title: `${nodeName} rebooted`, detail: `boot time ${new Date(r.bootTime * 1000).toISOString()}` });
    }
    for (const r of fx.restarts) {
      this.pendingEvents.push({ ts, kind: 'restart', severity: 'warning', entityKind: 'node', entityId: node, title: `${nodeName}: container ${r.container} restarted`, detail: `restart count ${r.previous} → ${r.count}` });
    }
    if (fx.firstSeen.length && !this.backfilling && this.cfg.mode === 'live') {
      this.pendingEvents.push({ ts, kind: 'agent', severity: 'info', entityKind: 'agent', entityId: node, title: `agent ${node} connected`, detail: `v${push.agent.version} on ${push.agent.hostname}` });
    }
    if (push.logs?.length) {
      const since = this.lastLogScan.get(node) ?? 0;
      for (const m of scanLogs(this.bundle, this.store, node, since)) {
        const watchdog = /watchdog/i.test(m.check.key + (m.check.params.path ?? ''));
        this.pendingEvents.push({
          ts: new Date(m.t).toISOString(),
          kind: watchdog ? 'watchdog' : 'log',
          severity: 'warning',
          entityKind: 'node',
          entityId: node,
          title: `${nodeName}: ${m.check.params.event ?? 'log match'}`,
          detail: m.line.slice(0, 300),
        });
      }
      this.lastLogScan.set(node, now);
    }
  }

  // ------------------------------------------------------------------ tick

  async tick(now: number) {
    if (this.sim) this.sim.step(now, this.store, (p, t) => this.ingest(p, t));
    // Forget agents that are not in the topology and stopped reporting (e.g. after a rollback).
    for (const [node, a] of this.store.agents) {
      if (now - a.lastSeen > 600_000 && node !== 'probe' && !this.bundle.nodes.some((n) => n.id === node)) this.store.agents.delete(node);
    }
    const obs = observe(this.bundle, this.store, now);
    const draft = buildSnapshot(this.bundle, obs, { mode: this.cfg.mode, live: true, version: this.version, since: this.since, incidentSince: this.incidentSince, alerts: this.activeAlerts });
    const { active, transitions } = this.alerts.evaluate(this.bundle, draft, now, (l, at) => this.baseline(l, at));
    this.activeAlerts = active;
    draft.alerts = active;
    draft.overall.activeAlerts = active.filter((a) => !a.silencedUntil).length;
    draft.drift = computeDrift(this.bundle, this.store, now);
    this.diff(draft, obs, now);
    for (const t of transitions) {
      const id = `${t.alert.id}|${t.instance.key}`;
      const msg = (s: string) => s.replace(/\{name\}/g, t.instance.name).replace(/\{id\}/g, t.instance.key);
      this.pendingEvents.push({
        ts: new Date(t.at).toISOString(),
        kind: 'alert',
        severity: t.fired ? (t.alert.severity === 'critical' ? 'critical' : 'warning') : 'ok',
        entityKind: 'alert',
        entityId: id,
        title: `${t.fired ? 'Alert' : 'Resolved'}: ${msg(t.alert.message_en || t.alert.title || t.alert.id)}`,
        detail: msg(t.alert.message_fa),
        data: { state: t.fired ? 'firing' : 'resolved', alertId: t.alert.id, instance: t.instance.key, severity: t.alert.severity, title: t.alert.title, messageFa: msg(t.alert.message_fa) },
      });
    }
    if (this.telegram && !this.backfilling) {
      const out: OutMessage[] = [
        ...this.notifier.onTransitions(transitions, {
          boardUrl: this.cfg.publicUrl,
          silencedUntil: (id, inst) => Math.max(this.alerts.silences.get(id) ?? 0, this.alerts.silences.get(`${id}|${inst}`) ?? 0),
          rootCauses: draft.overall.rootCauses,
        }),
        ...this.notifier.due(now, this.cfg.publicUrl),
      ];
      for (const m of out) {
        const tg = this.telegram;
        // Remember what each Telegram message was about, so a reply to it can be answered in context.
        tg.enqueue(m.text, { onSent: (id) => void this.db.addTelegramMessage({ chatId: tg.chatId, messageId: id, kind: m.kind === 'fired' ? 'alert' : 'resolved', alertKey: m.key, at: m.at, text: m.text }).catch((e) => console.error('telegram: cannot record message', e?.message)) });
      }
    }
    // Rebuild with up-to-date since/incident times.
    const snap = buildSnapshot(this.bundle, obs, { mode: this.cfg.mode, live: true, version: this.version, since: this.since, incidentSince: this.incidentSince, alerts: active });
    snap.drift = draft.drift;
    this.last = snap;
    this.lastObs = obs;
    if (now - this.lastWrite >= STEP_MS - 500) {
      this.lastWrite = now;
      await this.tsdb.write(now, toSeries(obs));
    }
    if (!this.backfilling) {
      const evs = await this.flushEvents();
      for (const l of this.listeners) {
        l({ type: 'snapshot', data: snap });
        if (evs.length) l({ type: 'events', data: evs });
      }
    } else if (this.pendingEvents.length > 2000) await this.flushEvents();
  }

  private async flushEvents(): Promise<HubEvent[]> {
    if (!this.pendingEvents.length) return [];
    const list = this.pendingEvents.splice(0);
    list.sort((a, b) => a.ts.localeCompare(b.ts));
    return this.db.addEvents(list);
  }

  /** Detect status changes and incidents; produce events and since-times. */
  private diff(snap: Snapshot, obs: Observations, now: number) {
    const ts = new Date(now).toISOString();
    const first = this.lastStatus.size === 0;
    const change = (key: string, status: string) => {
      const prev = this.lastStatus.get(key);
      this.lastStatus.set(key, status);
      if (prev !== status) this.since.set(key, now);
      return prev !== undefined && prev !== status ? prev : null;
    };
    const sev = (s: string) => (s === 'down' ? 'critical' : s === 'degraded' ? 'warning' : s === 'ok' ? 'ok' : 'info');
    for (const n of this.bundle.nodes) {
      const st = snap.nodes[n.id];
      if (!st.observed) continue;
      const prev = change(`node:${n.id}`, st.status);
      if (prev && !first) {
        const bad = st.checks.filter((c) => c.effective !== 'ok' && c.effective !== 'unknown').map((c) => c.detail);
        this.pendingEvents.push({ ts, kind: 'state', severity: sev(st.status), entityKind: 'node', entityId: n.id, title: `${n.name}: ${prev} → ${st.status}`, detail: bad.join('; '), data: { from: prev, to: st.status } });
      }
    }
    for (const l of this.bundle.links) {
      const st = snap.links[l.id];
      if (!st.observed) continue;
      const prev = change(`link:${l.id}`, st.status);
      if (prev && !first) {
        const bad = st.checks.filter((c) => c.effective !== 'ok').map((c) => c.detail);
        this.pendingEvents.push({ ts, kind: 'state', severity: sev(st.status), entityKind: 'link', entityId: l.id, title: `${linkName(l)}: ${prev} → ${st.status}`, detail: bad.join('; '), data: { from: prev, to: st.status } });
      }
    }
    for (const r of this.bundle.routes) {
      const st = snap.routes[r.id];
      const prev = change(`route:${r.id}`, st.status);
      if (prev && !first && (prev === 'down' || st.status === 'down')) {
        this.pendingEvents.push({ ts, kind: 'state', severity: sev(st.status), entityKind: 'route', entityId: r.id, title: `route ${r.title}: ${st.broken ? 'broken' : 'restored'}`, detail: st.probe?.detail ?? '', data: { from: prev, to: st.status } });
      }
    }
    // Check-level changes carry the detail text used by time travel.
    for (const [key, c] of Object.entries(obs.checks)) {
      const prev = change(`check:${key}`, c.status);
      if (prev !== null || first) {
        this.pendingEvents.push({ ts, kind: 'state', severity: sev(c.status), entityKind: 'check', entityId: key, title: `${key}: ${prev ?? 'start'} → ${c.status}`, detail: c.detail, data: { detail: c.detail, status: c.status } });
      }
    }
    // Incidents: one per root cause, announced once it has lasted 20 s (a
    // server that just went silent first looks like a failing link).
    const current = new Set(snap.overall.rootCauses.map((r) => r.id));
    for (const rc of snap.overall.rootCauses) {
      if (!this.incidentSince.has(rc.id)) this.incidentSince.set(rc.id, now);
      const announced = this.announced.get(rc.id);
      if (announced && announced !== `${rc.kind}:${rc.elementId}`) {
        this.announced.set(rc.id, `${rc.kind}:${rc.elementId}`);
        this.pendingEvents.push({
          ts,
          kind: 'incident',
          severity: rc.impact === 'outage' ? 'critical' : 'warning',
          entityKind: rc.kind === 'route' ? 'route' : rc.kind,
          entityId: rc.elementId,
          title: `Root cause updated: ${rc.title} — ${rc.summary}`,
          detail: `${rc.usersAffected.toLocaleString('en-US')} users affected on ${rc.routes.join(', ') || 'no route'}`,
          data: { state: 'update', rootCause: rc.id, users: rc.usersAffected, routes: rc.routes },
        });
      }
      if (!announced && now - this.incidentSince.get(rc.id)! >= 20_000) {
        this.announced.set(rc.id, `${rc.kind}:${rc.elementId}`);
        {
          this.pendingEvents.push({
            ts: new Date(this.incidentSince.get(rc.id)!).toISOString(),
            kind: 'incident',
            severity: rc.impact === 'outage' ? 'critical' : 'warning',
            entityKind: rc.kind === 'route' ? 'route' : rc.kind,
            entityId: rc.elementId,
            title: `Incident: ${rc.title} — ${rc.summary}`,
            detail: `${rc.usersAffected.toLocaleString('en-US')} users affected on ${rc.routes.join(', ') || 'no route'}`,
            data: { state: 'open', rootCause: rc.id, users: rc.usersAffected, routes: rc.routes },
          });
        }
      }
    }
    for (const [id, since] of [...this.incidentSince]) {
      if (current.has(id)) continue;
      this.incidentSince.delete(id);
      const root = this.announced.get(id);
      this.announced.delete(id);
      if (!root) continue; // never announced: a blip shorter than 20 s
      const [kind, elementId] = root.split(/:(.*)/s);
      this.pendingEvents.push({
        ts,
        kind: 'incident',
        severity: 'ok',
        entityKind: kind as any,
        entityId: elementId,
        title: `Resolved: ${this.nameOf(kind, elementId)} after ${Math.round((now - since) / 60000)} min`,
        detail: '',
        data: { state: 'resolved', rootCause: id, durationSec: Math.round((now - since) / 1000) },
      });
    }
  }

  nameOf(kind: string, id: string): string {
    if (kind === 'node') return this.bundle.nodes.find((n) => n.id === id)?.name ?? id;
    if (kind === 'link') {
      const l = this.bundle.links.find((x) => x.id === id);
      return l ? linkName(l) : id;
    }
    return this.bundle.routes.find((r) => r.id === id)?.title ?? id;
  }

  /** Average throughput of a link around the same time yesterday (for traffic_drop). */
  private baseline(linkId: string, at: number): number | null {
    const bucket = Math.floor(at / 300_000);
    const key = `${linkId}:${bucket}`;
    if (this.baselineCache.has(key)) return this.baselineCache.get(key)!;
    // Synchronous approximation: the memory TSDB answers immediately; with
    // VictoriaMetrics the value is filled in asynchronously on the next bucket.
    let v: number | null = null;
    const from = at - 86_400_000 - 1_800_000;
    const to = at - 86_400_000 + 1_800_000;
    void this.tsdb.range([`l:${linkId}:up`, `l:${linkId}:down`], from, to, 3_600_000).then((m) => {
      const pts = [...(m.get(`l:${linkId}:up`) ?? []), ...(m.get(`l:${linkId}:down`) ?? [])].map((p) => p[1]).filter((x): x is number => x !== null);
      const up = avg((m.get(`l:${linkId}:up`) ?? []).map((p) => p[1]));
      const down = avg((m.get(`l:${linkId}:down`) ?? []).map((p) => p[1]));
      v = pts.length ? (up ?? 0) + (down ?? 0) : null;
      this.baselineCache.set(key, v);
    });
    if (this.baselineCache.size > 5000) this.baselineCache.clear();
    return this.baselineCache.get(key) ?? null;
  }

  // ------------------------------------------------------------------ reads

  current(): Snapshot {
    if (!this.last) throw new Error('hub is starting');
    return this.last;
  }

  /** The board at a past moment, rebuilt from stored observations and events. */
  async snapshotAt(at: number | null): Promise<{ snap: Snapshot; bundle: Bundle }> {
    if (at === null || at >= (this.lastObs?.at ?? 0) - 1000) return { snap: this.current(), bundle: this.bundle };
    const topo = await this.db.topologyAt(new Date(at));
    const bundle = topo?.bundle ?? this.bundle;
    const series = await this.tsdb.instant(at, 30_000);
    // A stored sample covers its 10 s slot; read details up to the end of that slot.
    const details = await this.db.checkDetailsAt(new Date(Math.floor(at / STEP_MS) * STEP_MS + STEP_MS));
    const obs = fromSeries(bundle, at, series, details);
    const alertEvents = await this.db.alertsActiveAt(new Date(at));
    const alerts: ActiveAlert[] = alertEvents.map((e) => {
      const a = bundle.alerts.find((x) => x.id === e.data?.alertId);
      return {
        id: e.data?.alertId,
        instance: e.data?.instance ?? '',
        severity: e.data?.severity ?? 'warning',
        title: e.data?.title ?? e.data?.alertId,
        message: e.title.replace(/^Alert: /, ''),
        messageFa: e.data?.messageFa ?? e.detail,
        since: e.ts,
        silencedUntil: null,
        definedIn: a?.defined_in ?? { component: '', file: '', line: 0 },
      };
    });
    const incidents = await this.db.events({ until: new Date(at), since: new Date(at - 86_400_000), kinds: ['incident'], order: 'asc', limit: 2000 });
    const incSince = new Map<string, number>();
    for (const e of incidents) {
      if (e.data?.state === 'open') incSince.set(e.data.rootCause, Date.parse(e.ts));
      else if (e.data?.state === 'resolved') incSince.delete(e.data?.rootCause);
    }
    const snap = buildSnapshot(bundle, obs, { mode: this.cfg.mode, live: false, version: topo?.version ?? this.version, since: new Map(), incidentSince: incSince, alerts });
    return { snap, bundle };
  }

  subscribe(l: Listener): () => void {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  // ------------------------------------------------------------------ changes (hub-side only)

  async apply(bundle: Bundle, message: string, by: string): Promise<{ version: number; unchanged: boolean }> {
    validateBundle(bundle);
    if (bundle.hash === this.bundle.hash) return { version: this.version, unchanged: true };
    const version = await this.db.addTopology(bundle, message, by);
    this.setBundle(bundle, version, `topology v${version} applied by ${by}`, message);
    return { version, unchanged: false };
  }

  async rollback(version: number, by: string): Promise<Bundle | null> {
    const b = await this.db.activateTopology(version);
    if (!b) return null;
    this.setBundle(b, version, `rolled back to topology v${version} by ${by}`, '');
    return b;
  }

  private setBundle(b: Bundle, version: number, title: string, detail: string) {
    this.bundle = b;
    this.version = version;
    this.sim?.setBundle(b);
    this.pendingEvents.push({ ts: new Date().toISOString(), kind: 'topology', severity: 'info', entityKind: 'topology', entityId: String(version), title, detail, data: { version, hash: b.hash, commit: b.source?.git_commit } });
    for (const l of this.listeners) l({ type: 'topology', data: { version } });
  }

  history(): Promise<TopologyVersion[]> {
    return this.db.history();
  }

  async silence(alertId: string, instance: string, ms: number, reason: string) {
    if (!this.bundle.alerts.some((a) => a.id === alertId)) throw new Error(`unknown alert "${alertId}"`);
    const until = Date.now() + ms;
    this.alerts.silences.set(instance ? `${alertId}|${instance}` : alertId, until);
    await this.db.addSilence(alertId, instance, new Date(until), reason);
    this.pendingEvents.push({ ts: new Date().toISOString(), kind: 'alert', severity: 'info', entityKind: 'alert', entityId: `${alertId}|${instance}`, title: `Silenced ${alertId}${instance ? ' (' + instance + ')' : ''} until ${new Date(until).toISOString()}`, detail: reason, data: { state: 'silenced' } });
    return until;
  }

  /** Add an event to the log (flushed with the next tick). */
  addEvent(e: Omit<HubEvent, 'id'>) {
    this.pendingEvents.push(e);
  }

  // ------------------------------------------------------------------ telegram

  telegramStatus() {
    if (!this.cfg.telegram) return { enabled: false, reason: 'TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID not set' };
    if (!this.telegram) return { enabled: false, reason: 'demo mode' };
    return { enabled: true, apiHost: new URL(this.cfg.telegram.apiRoot).host, sent: this.telegram.sent, pending: this.telegram.pending, lastError: this.telegram.lastError };
  }

  async telegramTest(by: string): Promise<SendResult> {
    if (!this.telegram) throw new Error(this.telegramStatus().reason ?? 'telegram is off');
    const tg = this.telegram;
    const r = await tg.sendNow(`🧪 <b>پیام آزمایشی نقشه VPN</b>\nاتصال هاب به تلگرام برقرار است.\n${esc(by)} · ${tehranTime(Date.now())}`, { onSent: (id) => void this.db.addTelegramMessage({ chatId: tg.chatId, messageId: id, kind: 'test', at: Date.now(), text: 'test message' }).catch(() => undefined) });
    this.pendingEvents.push({ ts: new Date().toISOString(), kind: 'alert', severity: r.ok ? 'info' : 'warning', entityKind: null, entityId: null, title: r.ok ? 'Telegram test message sent' : 'Telegram test message failed', detail: r.error ?? '', data: { state: 'test' } });
    return r;
  }

  // ------------------------------------------------------------------ demo

  scenarios(): (DemoScenario & { lastStart?: string; running: boolean })[] {
    const now = Date.now();
    return SCENARIOS.map(({ effects: _e, ...s }) => {
      const runs = this.sim?.scheduled().filter((r) => r.scenario.id === s.id) ?? [];
      const last = runs[runs.length - 1];
      return { ...s, lastStart: last ? new Date(last.start).toISOString() : undefined, running: !!last && now < last.start + s.durationSec * 1000 };
    });
  }

  play(id: string) {
    if (!this.sim) throw new Error('scenarios only run in demo mode');
    const run = this.sim.schedule(id, Date.now());
    this.pendingEvents.push({ ts: new Date().toISOString(), kind: 'demo', severity: 'info', entityKind: null, entityId: null, title: `Demo: replaying "${run.scenario.title}"`, detail: run.scenario.description });
    return run;
  }

  checkTitle(key: string): string {
    for (const n of this.bundle.nodes) for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) if (c.key === key) return c.title || defaultTitle(c);
    for (const l of this.bundle.links) for (const c of l.health ?? []) if (c.key === key) return c.title || defaultTitle(c);
    return key;
  }
}

function avg(a: (number | null)[]): number | null {
  const v = a.filter((x): x is number => x !== null);
  return v.length ? v.reduce((s, x) => s + x, 0) / v.length : null;
}
