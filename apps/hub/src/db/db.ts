import { and, asc, desc, eq, gte, inArray, lte, sql } from 'drizzle-orm';
import { Bundle, EventKind, HubEvent, TopologyVersion } from '../contract';
import * as s from './schema';

/** A message the hub sent to Telegram (alert, recovery, assistant answer). */
export interface TelegramMessage {
  chatId: string;
  messageId: number;
  kind: 'alert' | 'resolved' | 'answer' | 'test' | string;
  alertKey?: string;
  at: number;
  text: string;
  /** For answers: the conversation so far, so a reply continues it. */
  thread?: { role: 'user' | 'assistant'; content: string }[];
}

/**
 * Database access. DATABASE_URL → Postgres (node-postgres); without it (demo,
 * tests) → PGlite, an embedded Postgres in WASM, so the same SQL runs everywhere.
 */
export class Db {
  private constructor(private readonly db: any, readonly driver: 'postgres' | 'pglite', private readonly close_: () => Promise<void>) {}

  static async open(url: string | undefined, pgliteDir?: string): Promise<Db> {
    if (url) {
      const { Pool } = await import('pg');
      const { drizzle } = await import('drizzle-orm/node-postgres');
      const pool = new Pool({ connectionString: url, max: 5 });
      const db = new Db(drizzle(pool, { schema: s }), 'postgres', () => pool.end());
      await db.migrate();
      return db;
    }
    const { PGlite } = await import('@electric-sql/pglite');
    const { drizzle } = await import('drizzle-orm/pglite');
    const client = new PGlite(pgliteDir);
    const db = new Db(drizzle(client, { schema: s }), 'pglite', () => client.close());
    await db.migrate();
    return db;
  }

  async close() {
    await this.close_();
  }

  private async migrate() {
    for (const m of s.MIGRATIONS) await this.db.execute(sql.raw(m));
  }

  // ---------------------------------------------------------------- topology

  async activeTopology(): Promise<{ version: number; bundle: Bundle } | null> {
    const rows = await this.db.select().from(s.topologyVersions).where(eq(s.topologyVersions.active, true)).limit(1);
    if (!rows.length) return null;
    return { version: rows[0].version, bundle: rows[0].bundle as Bundle };
  }

  async addTopology(bundle: Bundle, message: string, appliedBy: string): Promise<number> {
    return this.db.transaction(async (tx: any) => {
      await tx.update(s.topologyVersions).set({ active: false }).where(eq(s.topologyVersions.active, true));
      const [row] = await tx
        .insert(s.topologyVersions)
        .values({ hash: bundle.hash, message, gitCommit: bundle.source?.git_commit ?? null, appliedBy, active: true, bundle })
        .returning({ version: s.topologyVersions.version });
      return row.version as number;
    });
  }

  async activateTopology(version: number): Promise<Bundle | null> {
    return this.db.transaction(async (tx: any) => {
      const rows = await tx.select().from(s.topologyVersions).where(eq(s.topologyVersions.version, version));
      if (!rows.length) return null;
      await tx.update(s.topologyVersions).set({ active: false }).where(eq(s.topologyVersions.active, true));
      await tx.update(s.topologyVersions).set({ active: true }).where(eq(s.topologyVersions.version, version));
      return rows[0].bundle as Bundle;
    });
  }

  async topologyAt(at: Date): Promise<{ version: number; bundle: Bundle } | null> {
    const rows = await this.db.select().from(s.topologyVersions).where(lte(s.topologyVersions.createdAt, at)).orderBy(desc(s.topologyVersions.version)).limit(1);
    return rows.length ? { version: rows[0].version, bundle: rows[0].bundle as Bundle } : null;
  }

  async topologyVersion(version: number): Promise<Bundle | null> {
    const rows = await this.db.select().from(s.topologyVersions).where(eq(s.topologyVersions.version, version));
    return rows.length ? (rows[0].bundle as Bundle) : null;
  }

  async history(): Promise<TopologyVersion[]> {
    const rows = await this.db.select().from(s.topologyVersions).orderBy(desc(s.topologyVersions.version)).limit(100);
    return rows.map((r: any) => {
      const b = r.bundle as Bundle;
      return {
        version: r.version,
        hash: r.hash,
        createdAt: r.createdAt.toISOString(),
        message: r.message,
        gitCommit: r.gitCommit ?? undefined,
        appliedBy: r.appliedBy,
        active: r.active,
        counts: { nodes: b.nodes.length, links: b.links.length, routes: b.routes.length, alerts: b.alerts.length },
      };
    });
  }

  // ---------------------------------------------------------------- events

  async addEvents(list: Omit<HubEvent, 'id'>[]): Promise<HubEvent[]> {
    if (!list.length) return [];
    const out: HubEvent[] = [];
    for (let i = 0; i < list.length; i += 500) {
      const chunk = list.slice(i, i + 500);
      const rows = await this.db
        .insert(s.events)
        .values(chunk.map((e) => ({ ts: new Date(e.ts), kind: e.kind, severity: e.severity, entityKind: e.entityKind, entityId: e.entityId, title: e.title, detail: e.detail, data: e.data ?? null })))
        .returning();
      out.push(...rows.map(rowToEvent));
    }
    return out;
  }

  async events(q: { since?: Date; until?: Date; entity?: { kind: string; id: string }; kinds?: string[]; limit?: number; order?: 'asc' | 'desc' }): Promise<HubEvent[]> {
    const conds = [];
    if (q.since) conds.push(gte(s.events.ts, q.since));
    if (q.until) conds.push(lte(s.events.ts, q.until));
    if (q.entity) {
      conds.push(eq(s.events.entityId, q.entity.id));
      conds.push(eq(s.events.entityKind, q.entity.kind));
    }
    if (q.kinds?.length) conds.push(inArray(s.events.kind, q.kinds));
    const rows = await this.db
      .select()
      .from(s.events)
      .where(conds.length ? and(...conds) : undefined)
      .orderBy(q.order === 'asc' ? asc(s.events.ts) : desc(s.events.ts), q.order === 'asc' ? asc(s.events.id) : desc(s.events.id))
      .limit(Math.min(q.limit ?? 200, 5000));
    return rows.map(rowToEvent);
  }

  /** Last detail text of every check at or before `at` (for time travel). */
  async checkDetailsAt(at: Date): Promise<Map<string, { detail: string; status: string }>> {
    const res = await this.db.execute(sql`
      SELECT DISTINCT ON (entity_id) entity_id, data
      FROM events WHERE kind = 'state' AND entity_kind = 'check' AND ts <= ${at}
      ORDER BY entity_id, ts DESC, id DESC`);
    const rows = (res.rows ?? res) as any[];
    const m = new Map<string, { detail: string; status: string }>();
    for (const r of rows) if (r.data?.detail) m.set(r.entity_id, { detail: r.data.detail, status: r.data.status });
    return m;
  }

  async alertsActiveAt(at: Date): Promise<HubEvent[]> {
    const res = await this.db.execute(sql`
      SELECT DISTINCT ON (entity_id) * FROM events
      WHERE kind = 'alert' AND ts <= ${at}
      ORDER BY entity_id, ts DESC, id DESC`);
    const rows = ((res.rows ?? res) as any[]).map((r) => rowToEvent({ ...r, entityKind: r.entity_kind, entityId: r.entity_id, ts: new Date(r.ts) }));
    return rows.filter((e) => e.data?.state === 'firing');
  }

  // ---------------------------------------------------------------- layout, silences

  async layout(): Promise<Record<string, { x: number; y: number }>> {
    const rows = await this.db.select().from(s.layout);
    const out: Record<string, { x: number; y: number }> = {};
    for (const r of rows) out[r.nodeId] = { x: r.x, y: r.y };
    return out;
  }

  async saveLayout(pos: Record<string, { x: number; y: number }>) {
    for (const [nodeId, p] of Object.entries(pos)) {
      await this.db
        .insert(s.layout)
        .values({ nodeId, x: p.x, y: p.y })
        .onConflictDoUpdate({ target: s.layout.nodeId, set: { x: p.x, y: p.y, updatedAt: new Date() } });
    }
  }

  async clearLayout() {
    await this.db.delete(s.layout);
  }

  async addSilence(alertId: string, instance: string, until: Date, reason: string) {
    await this.db.insert(s.silences).values({ alertId, instance, until, reason });
  }

  async activeSilences(now: Date) {
    return this.db.select().from(s.silences).where(gte(s.silences.until, now));
  }

  // ---------------------------------------------------------------- settings, telegram messages

  async getSetting<T>(key: string): Promise<T | null> {
    const rows = await this.db.select().from(s.settings).where(eq(s.settings.key, key)).limit(1);
    return rows.length ? (rows[0].value as T) : null;
  }

  async putSetting(key: string, value: unknown) {
    await this.db
      .insert(s.settings)
      .values({ key, value })
      .onConflictDoUpdate({ target: s.settings.key, set: { value, updatedAt: new Date() } });
  }

  async addTelegramMessage(m: TelegramMessage) {
    await this.db
      .insert(s.telegramMessages)
      .values({ chatId: m.chatId, messageId: m.messageId, kind: m.kind, alertKey: m.alertKey ?? null, at: new Date(m.at), text: m.text.slice(0, 4000), thread: m.thread ?? null })
      .onConflictDoNothing();
  }

  async telegramMessage(chatId: string, messageId: number): Promise<TelegramMessage | null> {
    const rows = await this.db
      .select()
      .from(s.telegramMessages)
      .where(and(eq(s.telegramMessages.chatId, chatId), eq(s.telegramMessages.messageId, messageId)))
      .limit(1);
    if (!rows.length) return null;
    const r = rows[0];
    return { chatId: r.chatId, messageId: Number(r.messageId), kind: r.kind, alertKey: r.alertKey ?? undefined, at: new Date(r.at).getTime(), text: r.text, thread: (r.thread as any) ?? undefined };
  }
}

function rowToEvent(r: any): HubEvent {
  return {
    id: Number(r.id),
    ts: (r.ts instanceof Date ? r.ts : new Date(r.ts)).toISOString(),
    kind: r.kind as EventKind,
    severity: r.severity,
    entityKind: r.entityKind ?? null,
    entityId: r.entityId ?? null,
    title: r.title,
    detail: r.detail ?? '',
    data: r.data ?? undefined,
  };
}
