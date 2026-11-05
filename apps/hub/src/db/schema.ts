import { bigint, bigserial, boolean, doublePrecision, integer, jsonb, pgTable, serial, text, timestamp, index } from 'drizzle-orm/pg-core';

/** Postgres schema (Drizzle). Topology versions, events, layout, silences, settings, Telegram messages. */

export const topologyVersions = pgTable('topology_versions', {
  version: serial('version').primaryKey(),
  hash: text('hash').notNull(),
  createdAt: timestamp('created_at', { withTimezone: true }).notNull().defaultNow(),
  message: text('message').notNull().default(''),
  gitCommit: text('git_commit'),
  appliedBy: text('applied_by').notNull().default('bootstrap'),
  active: boolean('active').notNull().default(false),
  bundle: jsonb('bundle').notNull(),
});

export const events = pgTable(
  'events',
  {
    id: bigserial('id', { mode: 'number' }).primaryKey(),
    ts: timestamp('ts', { withTimezone: true }).notNull(),
    kind: text('kind').notNull(),
    severity: text('severity').notNull(),
    entityKind: text('entity_kind'),
    entityId: text('entity_id'),
    title: text('title').notNull(),
    detail: text('detail').notNull().default(''),
    data: jsonb('data'),
  },
  (t) => [index('events_ts_idx').on(t.ts), index('events_entity_idx').on(t.entityKind, t.entityId, t.ts)],
);

export const layout = pgTable('layout', {
  nodeId: text('node_id').primaryKey(),
  x: doublePrecision('x').notNull(),
  y: doublePrecision('y').notNull(),
  updatedAt: timestamp('updated_at', { withTimezone: true }).notNull().defaultNow(),
});

export const silences = pgTable('silences', {
  id: serial('id').primaryKey(),
  alertId: text('alert_id').notNull(),
  instance: text('instance').notNull().default(''),
  until: timestamp('until', { withTimezone: true }).notNull(),
  reason: text('reason').notNull().default(''),
  createdAt: timestamp('created_at', { withTimezone: true }).notNull().defaultNow(),
});

/** Hub settings edited from the board (assistant). Secrets in here are encrypted (see assistant/settings.ts). */
export const settings = pgTable('settings', {
  key: text('key').primaryKey(),
  value: jsonb('value').notNull(),
  updatedAt: timestamp('updated_at', { withTimezone: true }).notNull().defaultNow(),
});

/** Messages the hub sent to Telegram, so a reply knows which alert or answer it refers to. */
export const telegramMessages = pgTable('telegram_messages', {
  chatId: text('chat_id').notNull(),
  messageId: bigint('message_id', { mode: 'number' }).notNull(),
  kind: text('kind').notNull(), // alert | resolved | answer | test
  alertKey: text('alert_key'),
  at: timestamp('at', { withTimezone: true }).notNull(),
  text: text('text').notNull().default(''),
  thread: jsonb('thread'),
});

/** Ops-agent tasks: a proposed plan, its approval and the result of running it. */
export const opsTasks = pgTable('ops_tasks', {
  id: text('id').primaryKey(),
  status: text('status').notNull().default('pending'),
  plan: jsonb('plan').notNull(),
  hash: text('hash').notNull(),
  vault: jsonb('vault'), // sealed secret placeholders → values (secretbox)
  requestedBy: text('requested_by').notNull(),
  approvedBy: text('approved_by'),
  chatId: text('chat_id'),
  messageId: bigint('message_id', { mode: 'number' }),
  createdAt: timestamp('created_at', { withTimezone: true }).notNull().defaultNow(),
  finishedAt: timestamp('finished_at', { withTimezone: true }),
  log: jsonb('log').notNull().default([]),
  outcome: text('outcome'),
});

export const schemaVersion = pgTable('schema_version', {
  version: integer('version').primaryKey(),
});

/** Idempotent DDL, run at startup (kept in sync with the tables above). */
export const MIGRATIONS: string[] = [
  `CREATE TABLE IF NOT EXISTS topology_versions (
     version serial PRIMARY KEY,
     hash text NOT NULL,
     created_at timestamptz NOT NULL DEFAULT now(),
     message text NOT NULL DEFAULT '',
     git_commit text,
     applied_by text NOT NULL DEFAULT 'bootstrap',
     active boolean NOT NULL DEFAULT false,
     bundle jsonb NOT NULL)`,
  `CREATE TABLE IF NOT EXISTS events (
     id bigserial PRIMARY KEY,
     ts timestamptz NOT NULL,
     kind text NOT NULL,
     severity text NOT NULL,
     entity_kind text,
     entity_id text,
     title text NOT NULL,
     detail text NOT NULL DEFAULT '',
     data jsonb)`,
  `CREATE INDEX IF NOT EXISTS events_ts_idx ON events (ts)`,
  `CREATE INDEX IF NOT EXISTS events_entity_idx ON events (entity_kind, entity_id, ts)`,
  `CREATE TABLE IF NOT EXISTS layout (
     node_id text PRIMARY KEY,
     x double precision NOT NULL,
     y double precision NOT NULL,
     updated_at timestamptz NOT NULL DEFAULT now())`,
  `CREATE TABLE IF NOT EXISTS silences (
     id serial PRIMARY KEY,
     alert_id text NOT NULL,
     instance text NOT NULL DEFAULT '',
     until timestamptz NOT NULL,
     reason text NOT NULL DEFAULT '',
     created_at timestamptz NOT NULL DEFAULT now())`,
  `CREATE TABLE IF NOT EXISTS settings (
     key text PRIMARY KEY,
     value jsonb NOT NULL,
     updated_at timestamptz NOT NULL DEFAULT now())`,
  `CREATE TABLE IF NOT EXISTS telegram_messages (
     chat_id text NOT NULL,
     message_id bigint NOT NULL,
     kind text NOT NULL,
     alert_key text,
     at timestamptz NOT NULL,
     text text NOT NULL DEFAULT '',
     thread jsonb,
     PRIMARY KEY (chat_id, message_id))`,
  `CREATE TABLE IF NOT EXISTS ops_tasks (
     id text PRIMARY KEY,
     status text NOT NULL DEFAULT 'pending',
     plan jsonb NOT NULL,
     hash text NOT NULL,
     vault jsonb,
     requested_by text NOT NULL,
     approved_by text,
     chat_id text,
     message_id bigint,
     created_at timestamptz NOT NULL DEFAULT now(),
     finished_at timestamptz,
     log jsonb NOT NULL DEFAULT '[]'::jsonb,
     outcome text)`,
  `CREATE INDEX IF NOT EXISTS ops_tasks_created_idx ON ops_tasks (created_at DESC)`,
];
