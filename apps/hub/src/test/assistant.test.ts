import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { applyPatch, decrypt, DEFAULTS, publicView } from '../assistant/settings';
import { decide, TgMessage } from '../assistant/trigger';
import { answer, MAX_ROUNDS, splitMessage, userMessage } from '../assistant/agent';
import { ChatFn, ToolCall } from '../assistant/openrouter';
import { ReadApi, runTool, summarise, TOOLS, ToolContext } from '../assistant/tools';
import { parseAt, parseDuration } from '../queries';

/** Telegram assistant: settings secrecy, who gets answers, the agent loop and its read-only tools. */

const SECRET = 'session-secret-for-tests';
const KEY = 'sk-or-v1-0123456789abcdef0123456789abcdef';

test('settings: the key is stored encrypted and never returned, only a hint', () => {
  const { next, changes } = applyPatch(DEFAULTS, { enabled: true, model: 'google/gemini-2.5-flash', allowedUserIds: '111111, 222222 abc 111111', key: KEY }, SECRET);
  assert.deepEqual(changes, ['enabled', 'model → google/gemini-2.5-flash', 'allowed users → 2', 'key replaced']);
  assert.ok(!JSON.stringify(next).includes(KEY), 'not stored in clear');
  assert.equal(decrypt(next.key!, SECRET), KEY);
  assert.equal(decrypt(next.key!, 'another-secret'), null);
  const v = publicView(next, SECRET);
  assert.ok(!JSON.stringify(v).includes(KEY), 'not in the API view');
  assert.equal(v.keyHint, 'sk-or-…cdef');
  assert.deepEqual(v.allowedUserIds, ['111111', '222222']);
  assert.equal(publicView(next, 'another-secret').keyUnreadable, true);
  assert.equal(applyPatch(next, { key: '' }, SECRET).next.key, undefined, '"" clears');
  assert.ok(!applyPatch(next, { model: 'x' }, SECRET).changes.includes('key replaced'), 'absent key is kept');
  assert.throws(() => applyPatch(next, { model: 'two words' }, SECRET));
  assert.throws(() => applyPatch(next, { dailyLimit: 0 }, SECRET));
});

const cfg = { chatId: '-559000', botId: '777', allowedUserIds: ['111111'] };
const msg = (o: Partial<TgMessage>): TgMessage => ({ message_id: 10, date: 0, chat: { id: -559000 }, from: { id: 111111, first_name: 'Saeed' }, text: '', ...o });

test('trigger: only /ask or replies to the bot, only allowed users, only the alert chat', () => {
  assert.deepEqual(decide(msg({ text: 'hello everyone' }), cfg), { answer: false, reason: 'not-for-bot' });
  assert.equal(decide(msg({ text: '/ask why is e3 down?', chat: { id: -1 } }), cfg).answer, false);
  const ask = decide(msg({ text: '/ask@VpnTopologyBot why is e3 down?' }), cfg);
  assert.ok(ask.answer && ask.question === 'why is e3 down?' && ask.replyToBotMessage === null);
  const reply = decide(msg({ text: 'چرا؟', reply_to_message: { message_id: 5, from: { id: 777, is_bot: true } } }), cfg);
  assert.ok(reply.answer && reply.replyToBotMessage === 5 && reply.question === 'چرا؟');
  assert.equal(decide(msg({ text: 'چرا؟', reply_to_message: { message_id: 5, from: { id: 999 } } }), cfg).answer, false, 'reply to someone else');
  const stranger = decide(msg({ text: '/ask status', from: { id: 5 } }), cfg);
  assert.deepEqual(stranger, { answer: false, reason: 'not-allowed', fromId: '5' });
  assert.deepEqual(decide(msg({ text: '/ask' }), cfg), { answer: false, reason: 'empty', fromId: '111111', help: true });
  assert.equal(decide(msg({ text: '/ask hi', from: { id: 777, is_bot: true } }), cfg).answer, false);
});

function fakeApi(seen: { name: string; at: number | null }[]): ReadApi {
  const rec = (name: string, at: number | null) => seen.push({ name, at });
  return {
    bundle: () => ({ nodes: [{ id: 'relay-1', name: 'Relay 1', kind: 'relay', group: 'iran-dc' }], links: [], routes: [] }) as any,
    status: async (at) => (rec('status', at), { at: 'x', overall: { status: 'down' }, routes: [], alerts: [], drift: [] }),
    diagnose: async (at) => (rec('diagnose', at), { at: 'x', status: 'down', headline: 'E3 down', rootCauses: [], degraded: [], alerts: [], recentEvents: [] }),
    describe: async (id, at) => (rec('describe', at), { id, events: [] }),
    why: async (id, at) => (rec('why', at), { id, reasons: ['tcp :9000 refused'] }),
    path: async (_f, _t, _r, at) => (rec('path', at), { hops: [] }),
    events: async (q) => (rec('events', q.until), []),
    metrics: async (q) => (rec('metrics', q.until), []),
  };
}

const tc = (name: string, args: object, id = 'c1'): ToolCall => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });

test('agent: tool calls run against the replied alert time, then the model answers', async () => {
  const seen: { name: string; at: number | null }[] = [];
  const ctx: ToolContext = { api: fakeApi(seen), defaultAt: 1_700_000_000_000, parseAt, parseDuration };
  const sent: any[][] = [];
  const replies = [
    { content: null, toolCalls: [tc('diagnose', {}), tc('why', { id: 'e3' }, 'c2')], usage: { promptTokens: 100, completionTokens: 10, costUsd: 0.001 } },
    { content: 'لینک E3 قطع است.', toolCalls: [], usage: { promptTokens: 200, completionTokens: 20, costUsd: 0.002 } },
  ];
  const chat: ChatFn = async (m, tools) => (sent.push([...m]), assert.equal(tools.length, TOOLS.length), replies.shift()!);
  const r = await answer({ question: 'چرا؟', replyTo: { kind: 'alert', alertKey: 'e3-down|link:e3', at: ctx.defaultAt!, text: '<b>هشدار</b> E3' }, now: Date.now(), boardUrl: 'https://map' }, chat, ctx);
  assert.equal(r.text, 'لینک E3 قطع است.');
  assert.equal(r.rounds, 2);
  assert.deepEqual(seen, [{ name: 'diagnose', at: ctx.defaultAt }, { name: 'why', at: ctx.defaultAt }]);
  assert.deepEqual(r.usage, { promptTokens: 300, completionTokens: 30, costUsd: 0.003 });
  const second = sent[1];
  assert.equal(second.filter((m: any) => m.role === 'tool').length, 2, 'both tool results go back');
  assert.match(String(second[1].content), /replying to an alert/);
  assert.match(String(second[1].content), /\[That message: هشدار E3\]/, 'HTML stripped');
});

test('agent: stops calling tools after MAX_ROUNDS and must answer', async () => {
  let rounds = 0;
  const chat: ChatFn = async (_m, tools) => {
    rounds++;
    return tools.length ? { content: null, toolCalls: [tc('status', {})], usage: { promptTokens: 1, completionTokens: 1, costUsd: null } } : { content: 'done', toolCalls: [], usage: { promptTokens: 1, completionTokens: 1, costUsd: null } };
  };
  const r = await answer({ question: 'q', now: 0, boardUrl: '' }, chat, { api: fakeApi([]), defaultAt: null, parseAt, parseDuration });
  assert.equal(rounds, MAX_ROUNDS);
  assert.equal(r.text, 'done');
});

test('tools: explicit at overrides the default, errors come back as text, there is no write tool', async () => {
  const seen: { name: string; at: number | null }[] = [];
  const ctx: ToolContext = { api: fakeApi(seen), defaultAt: 5, parseAt, parseDuration };
  await runTool('status', JSON.stringify({ at: '2026-10-22T02:03:00Z' }), ctx);
  assert.equal(seen[0].at, Date.parse('2026-10-22T02:03:00Z'));
  assert.match(await runTool('restart', '{}', ctx), /^error: unknown tool/);
  assert.match(await runTool('status', '{bad', ctx), /^error: arguments/);
  assert.match(await runTool('status', JSON.stringify({ at: 'yesterday-ish' }), ctx), /^error: invalid time/);
  const names = TOOLS.map((t) => t.function.name);
  assert.deepEqual(names, ['list_entities', 'status', 'diagnose', 'describe', 'why', 'path', 'events', 'metrics']);
});

test('helpers: metric summary, message split, reply context', () => {
  const s = summarise({ key: 'k', label: 'CPU', unit: '%', points: [[0, 10], [10_000, null], [20_000, 30]] });
  assert.deepEqual({ min: s.min, avg: s.avg, max: s.max, last: s.last, samples: s.samples }, { min: 10, avg: 20, max: 30, last: 30, samples: 2 });
  const parts = splitMessage('a'.repeat(3000) + '\n' + 'b'.repeat(3000), 4000);
  assert.equal(parts.length, 2);
  assert.ok(parts.every((p) => p.length <= 4000));
  assert.equal(userMessage({ question: 'hi', now: 0, boardUrl: '' }), 'hi');
});
