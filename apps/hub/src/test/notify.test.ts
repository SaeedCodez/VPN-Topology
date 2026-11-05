import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { TopoAlert } from '../contract';
import { AlertTransition } from '../engine/alerts';
import { NotifyContext, NotifyPlanner, RESOLVE_GRACE_MS, RESTART_GRACE_MS } from '../engine/notify';
import { TelegramSender } from '../notify/telegram';

/** Telegram notifier: what gets sent for fired/resolved transitions, and that the sender never leaks the token. */

const alert = (id: string, severity: TopoAlert['severity'] = 'critical'): TopoAlert =>
  ({ id, when: '', severity, message_fa: 'قطعی {name}', message_en: 'down {name}', expr: {} as any, defined_in: { file: 'x', line: 1 } as any }) as TopoAlert;

const tr = (a: TopoAlert, fired: boolean, at: number, inst = { key: 'link:e3', name: 'E3 <relay>' }): AlertTransition => ({ alert: a, instance: inst, fired, at });

const ctx = (silenced = 0, rootCauses: any[] = []): NotifyContext => ({ boardUrl: 'https://map.example', silencedUntil: () => silenced, rootCauses });

const T0 = Date.UTC(2026, 10, 5, 10, 0, 0);

test('fired then resolved: one message each, resolved after the grace period, with duration', () => {
  const p = new NotifyPlanner();
  const a = alert('e3-down');
  const fired = p.onTransitions([tr(a, true, T0)], ctx(0, [{ title: 'XBridger node 3', summary: 'connection not running', usersAffected: 120 }]));
  assert.equal(fired.length, 1);
  assert.match(fired[0].text, /هشدار بحرانی/);
  assert.match(fired[0].text, /قطعی E3 &lt;relay&gt;/, 'name is filled and HTML-escaped');
  assert.match(fired[0].text, /علت اصلی: XBridger node 3 — connection not running · ۱۲۰ کاربر/);
  assert.match(fired[0].text, /href="https:\/\/map\.example"/);

  assert.deepEqual(p.onTransitions([tr(a, false, T0 + 12 * 60_000)], ctx()), []);
  assert.deepEqual(p.due(T0 + 12 * 60_000 + RESOLVE_GRACE_MS - 1, ''), [], 'not before the grace period');
  const res = p.due(T0 + 12 * 60_000 + RESOLVE_GRACE_MS, '');
  assert.equal(res.length, 1);
  assert.equal(res[0].kind, 'resolved');
  assert.match(res[0].text, /برطرف شد/);
  assert.match(res[0].text, /مدت: ۱۲ دقیقه/);
  assert.deepEqual(p.due(T0 + 60 * 60_000, ''), [], 'sent once');
});

test('flapping inside the grace period sends nothing more', () => {
  const p = new NotifyPlanner();
  const a = alert('e3-down');
  assert.equal(p.onTransitions([tr(a, true, T0)], ctx()).length, 1);
  p.onTransitions([tr(a, false, T0 + 60_000)], ctx());
  assert.deepEqual(p.onTransitions([tr(a, true, T0 + 90_000)], ctx()), []);
  assert.deepEqual(p.due(T0 + 10 * 60_000, ''), [], 'still firing → no resolved');
});

test('silenced alerts are not announced, and neither is their recovery', () => {
  const p = new NotifyPlanner();
  const a = alert('server-rebooted', 'warning');
  assert.deepEqual(p.onTransitions([tr(a, true, T0)], ctx(T0 + 3_600_000)), []);
  p.onTransitions([tr(a, false, T0 + 60_000)], ctx());
  assert.deepEqual(p.due(T0 + 3_600_000, ''), []);
});

test('warnings do not carry the board root cause', () => {
  const p = new NotifyPlanner();
  const [m] = p.onTransitions([tr(alert('tls-expiring', 'warning'), true, T0)], ctx(0, [{ title: 'Panel', summary: 'api down', usersAffected: 3 }]));
  assert.match(m.text, /🟠/);
  assert.doesNotMatch(m.text, /علت اصلی/);
});

test('after a restart, alerts already firing are not announced again but their recovery is', () => {
  const p = new NotifyPlanner();
  const a = alert('e3-down');
  p.carryOver([{ key: 'e3-down|link:e3', since: T0 - 3_600_000 }], T0);
  assert.deepEqual(p.onTransitions([tr(a, true, T0 + 2 * 60_000)], ctx()), []);
  p.onTransitions([tr(a, false, T0 + 5 * 60_000)], ctx());
  assert.equal(p.due(T0 + 5 * 60_000 + RESOLVE_GRACE_MS, '').length, 1);

  // A carried alert that comes back long after the restart grace is new.
  const q = new NotifyPlanner();
  q.carryOver([{ key: 'e3-down|link:e3', since: T0 }], T0);
  q.due(T0 + RESTART_GRACE_MS + 1, '');
  assert.equal(q.onTransitions([tr(a, true, T0 + RESTART_GRACE_MS + 2)], ctx()).length, 1);
});

test('sender: posts HTML to sendMessage, retries a 429, never reports the token', async () => {
  const token = '123456:SECRET-token';
  const calls: { url: string; body: any }[] = [];
  const answers = [
    { status: 429, body: { ok: false, description: 'Too Many Requests', parameters: { retry_after: 1 } } },
    { status: 200, body: { ok: true } },
    { status: 401, body: { ok: false, description: `Unauthorized ${token}` } },
  ];
  const fakeFetch = (async (url: string, init: any) => {
    calls.push({ url, body: JSON.parse(init.body) });
    const a = answers.shift()!;
    return { ok: a.status === 200, status: a.status, statusText: '', json: async () => a.body } as any;
  }) as unknown as typeof fetch;
  const slept: number[] = [];
  const s = new TelegramSender({ apiRoot: 'https://api.telegram.org/', token, chatId: '-100' }, fakeFetch, async (ms) => void slept.push(ms));
  s.enqueue('<b>hi</b>');
  for (let i = 0; i < 20 && (s.pending || calls.length < 2); i++) await new Promise((r) => setImmediate(r));
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, `https://api.telegram.org/bot${token}/sendMessage`);
  assert.deepEqual({ chat: calls[0].body.chat_id, mode: calls[0].body.parse_mode, text: calls[0].body.text }, { chat: '-100', mode: 'HTML', text: '<b>hi</b>' });
  assert.equal(slept[0], 1250, 'honours retry_after');
  assert.equal(s.sent, 1);

  const r = await s.sendNow('x');
  assert.equal(r.ok, false);
  assert.ok(!r.error!.includes(token) && !JSON.stringify(s.lastError).includes(token), 'token is redacted');
});
