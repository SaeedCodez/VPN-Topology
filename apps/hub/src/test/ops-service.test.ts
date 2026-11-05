import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { Health } from '../contract';
import { Db } from '../db/db';
import { OpsService } from '../ops/service';
import { ExecResult, SshTarget } from '../ops/ssh';

/** End-to-end through OpsService: a proposed plan becomes a pending Telegram
 * approval; tapping Approve runs it; a route going worse rolls it back. Uses a
 * real (in-memory) DB, a fake Telegram and a fake SSH. */

const SECRET = 'ops-svc-secret';
const CHAT = '-100999';

function fakeTelegram() {
  const calls: { method: string; body: any }[] = [];
  return {
    chatId: CHAT,
    botId: '42',
    calls,
    async call(method: string, body: any) {
      calls.push({ method, body });
      return { ok: true, status: 200, result: { message_id: 555 } };
    },
    async sendNow() {
      return { ok: true };
    },
  };
}

async function makeHub(statuses: () => Record<string, Health>) {
  const db = await Db.open(undefined); // PGlite
  const telegram = fakeTelegram();
  const bundle = { nodes: [{ id: 'relay-1' }, { id: 'main' }], links: [], routes: [{ id: 'main' }] } as any;
  const hub: any = {
    cfg: { sessionSecret: SECRET, mode: 'live', publicUrl: 'https://map.example', tickMs: 10, rwApiUrl: 'https://rw', rwApiToken: 't' },
    db,
    telegram,
    bundle,
    events: [] as any[],
    addEvent(e: any) {
      this.events.push(e);
    },
    async snapshotAt() {
      const st = statuses();
      return { snap: { nodes: { 'relay-1': { status: st['relay-1'] ?? 'ok' } }, links: {}, routes: { main: { status: st.main ?? 'ok' } } } };
    },
  };
  return { hub, db, telegram };
}

function fakeSsh(onCmd: (cmd: string) => { ok: boolean; output?: string }) {
  const calls: string[] = [];
  const fn = async (_t: SshTarget, _k: string, command: string): Promise<ExecResult> => {
    calls.push(command);
    const r = onCmd(command);
    return { ok: r.ok, code: r.ok ? 0 : 1, output: r.output ?? '', hostKeyFp: 'SHA256:fp', truncated: false };
  };
  return { fn, calls };
}

async function enable(ops: OpsService) {
  await ops.update({ enabled: true, actionModel: 'x/y', approverUserIds: '111', hubIp: '1.2.3.4', servers: [{ id: 'relay-1', host: '10.0.0.1', access: 'on' }] }, 'test');
}

const planJson = {
  title: 'reload nginx',
  why: 'cert renewed',
  impact: 'none expected',
  steps: [{ target: 'relay-1', run: 'systemctl reload nginx', purpose: 'reload', rollback: 'systemctl reload nginx' }],
  verify: ['main'],
};

test('propose → approve → run: success path, task stored done, Telegram edited', async () => {
  const { hub, db, telegram } = await makeHub(() => ({ main: 'ok' }));
  const ssh = fakeSsh(() => ({ ok: true, output: 'ok' }));
  const ops = new OpsService(hub, ssh.fn);
  await ops.init();
  await enable(ops);

  const rt = ops.agentRuntime(new (await import('../redact')).Vault(), '111', CHAT);
  const r = await rt.run('propose_plan', JSON.stringify(planJson));
  const taskId = r.proposedTaskId!;
  assert.ok(taskId, 'plan proposed');
  const sent = telegram.calls.find((c) => c.method === 'sendMessage');
  assert.ok(sent?.body.reply_markup, 'approval message has buttons');
  assert.equal((await db.opsTask(taskId))!.status, 'pending');

  // Non-approver cannot approve.
  assert.match(await ops.handleCallback(`ops:${taskId}:a`, '999'), /اجازه/);

  await ops.handleCallback(`ops:${taskId}:a`, '111');
  await waitFor(async () => (await db.opsTask(taskId))!.status === 'done');
  assert.ok(ssh.calls.includes('systemctl reload nginx'), 'the step ran');
  assert.ok(telegram.calls.some((c) => c.method === 'editMessageText'), 'progress/result edited the message');
});

test('approve: a step makes the route worse → rolled back, task rolled-back', async () => {
  let main: Health = 'ok';
  const { hub, db } = await makeHub(() => ({ main }));
  const ran: string[] = [];
  const ssh = fakeSsh((cmd) => {
    ran.push(cmd);
    if (ran.filter((c) => c === 'systemctl reload nginx').length === 1) main = 'down'; // first run breaks it
    return { ok: true, output: 'ok' };
  });
  const ops = new OpsService(hub, ssh.fn);
  await ops.init();
  await enable(ops);
  const rt = ops.agentRuntime(new (await import('../redact')).Vault(), '111', CHAT);
  const taskId = (await rt.run('propose_plan', JSON.stringify(planJson))).proposedTaskId!;
  await ops.handleCallback(`ops:${taskId}:a`, '111');
  await waitFor(async () => ['rolled-back', 'failed'].includes((await db.opsTask(taskId))!.status));
  assert.equal((await db.opsTask(taskId))!.status, 'rolled-back');
  assert.equal(ran.filter((c) => c === 'systemctl reload nginx').length, 2, 'ran once then rolled back once');
});

test('cancel leaves nothing run', async () => {
  const { hub, db } = await makeHub(() => ({ main: 'ok' }));
  const ssh = fakeSsh(() => ({ ok: true }));
  const ops = new OpsService(hub, ssh.fn);
  await ops.init();
  await enable(ops);
  const rt = ops.agentRuntime(new (await import('../redact')).Vault(), '111', CHAT);
  const taskId = (await rt.run('propose_plan', JSON.stringify(planJson))).proposedTaskId!;
  assert.match(await ops.handleCallback(`ops:${taskId}:c`, '111'), /لغو/);
  assert.equal((await db.opsTask(taskId))!.status, 'cancelled');
  assert.equal(ssh.calls.length, 0);
});

async function waitFor(cond: () => Promise<boolean>, ms = 2000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) {
    if (await cond()) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error('condition not met in time');
}
