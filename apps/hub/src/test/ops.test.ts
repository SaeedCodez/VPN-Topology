import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { Health, OpsPlan } from '../contract';
import { Vault } from '../redact';
import { ExecEnv, runPlan } from '../ops/executor';
import { OpsEnv, runOpsTool, validatePlan, normalizePlan } from '../ops/tools';
import { applyOpsPatch, grantCommand, OPS_DEFAULTS, opsKeyOpenSsh, opsPrivateKey, serverReachable, StoredOps } from '../ops/settings';
import { ExecResult, SshTarget } from '../ops/ssh';

const SECRET = 'ops-session-secret';

function enabledSettings(): StoredOps {
  const { next } = applyOpsPatch(OPS_DEFAULTS, { enabled: true, actionModel: 'x/y', approverUserIds: '111', hubIp: '171.22.27.214', servers: [{ id: 'relay-1', host: '31.56.89.121', access: 'on' }] }, SECRET);
  return next;
}

/** A fake SSH that records commands and answers from a script keyed by substring. */
function fakeSsh(script: (cmd: string) => { ok: boolean; output?: string; code?: number }) {
  const calls: { target: SshTarget; command: string }[] = [];
  const fn = async (target: SshTarget, _key: string, command: string): Promise<ExecResult> => {
    calls.push({ target, command });
    const r = script(command);
    return { ok: r.ok, code: r.code ?? (r.ok ? 0 : 1), output: r.output ?? '', hostKeyFp: 'SHA256:testfp', truncated: false };
  };
  return { fn, calls };
}

const noRemnawave = { request: async () => ({ ok: true, status: 200, body: {} }) };

function execEnv(settings: StoredOps, ssh: ReturnType<typeof fakeSsh>, verify: () => Map<string, Health>, over: Partial<ExecEnv> = {}): ExecEnv {
  return {
    settings,
    privateKey: opsPrivateKey(settings, SECRET),
    sshExec: ssh.fn,
    rw: noRemnawave as any,
    vault: new Vault(),
    verify: async () => verify(),
    settle: async () => {},
    onStep: () => {},
    onProgress: () => {},
    stopped: () => false,
    ...over,
  };
}

// --------------------------------------------------------------- settings

test('ops settings: key is generated on enable, stored encrypted, only the public half is shown', () => {
  const s = enabledSettings();
  assert.ok(s.sshPrivateKey, 'private key stored');
  assert.ok(!JSON.stringify(s.sshPrivateKey).includes('BEGIN'), 'private key is encrypted, not PEM');
  assert.ok(opsKeyOpenSsh(s)!.startsWith('ssh-ed25519 '), 'public key in OpenSSH form');
  assert.ok(opsPrivateKey(s, SECRET)!.includes('PRIVATE KEY'), 'private key decrypts');
  assert.equal(opsPrivateKey(s, 'wrong'), null, 'wrong secret cannot read the key');
  const grant = grantCommand('vpnmap-op', opsKeyOpenSsh(s)!, s.hubIp);
  assert.match(grant, /from=\\"171\.22\.27\.214\\"/, 'key is pinned to the hub IP');
  assert.match(grant, /sudoers\.d\/vpnmap-op/);
  assert.equal(serverReachable(s, s.servers[0]), true);
  const off = applyOpsPatch(s, { enabled: false }, SECRET).next;
  assert.equal(serverReachable(off, off.servers[0]), false, 'kill switch blocks every server');
});

// --------------------------------------------------------------- plan validation

function opsEnv(settings: StoredOps, over: Partial<OpsEnv> = {}): OpsEnv {
  return {
    settings,
    privateKey: opsPrivateKey(settings, SECRET),
    sshExec: async () => ({ ok: true, code: 0, output: '', hostKeyFp: '', truncated: false }),
    rw: noRemnawave as any,
    vault: new Vault(),
    validIds: new Set(['relay-1', 'main', 'e3']),
    onHostKey: async () => ({ ok: true, changed: false }),
    createTask: async () => ({ taskId: 'T1' }),
    ...over,
  };
}

test('plan validation rejects unknown server, unreachable server, refused command and bad verify id', () => {
  const env = opsEnv(enabledSettings());
  const bad: OpsPlan = normalizePlan({
    title: 'x',
    steps: [
      { target: 'nope', run: 'echo hi' },
      { target: 'relay-1', run: 'rm -rf /etc' },
    ],
    verify: ['relay-1', 'ghost'],
  });
  const errs = validatePlan(bad, env);
  assert.ok(errs.some((e) => /unknown server "nope"/.test(e)));
  assert.ok(errs.some((e) => /refused command/.test(e)));
  assert.ok(errs.some((e) => /verify id "ghost"/.test(e)));
  const ok = normalizePlan({ title: 'renew', steps: [{ target: 'relay-1', run: 'systemctl reload nginx', rollback: 'systemctl reload nginx' }], verify: ['main'] });
  assert.deepEqual(validatePlan(ok, env), []);
});

// --------------------------------------------------------------- ssh_read tool

test('ssh_read runs a read but refuses a change (which must go through a plan)', async () => {
  const ssh = fakeSsh(() => ({ ok: true, output: 'active' }));
  const env = opsEnv(enabledSettings(), { sshExec: ssh.fn });
  const okr = await runOpsTool('ssh_read', JSON.stringify({ server: 'relay-1', command: 'systemctl status nginx' }), env);
  assert.match(okr.output, /active/);
  assert.equal(ssh.calls.length, 1);
  const refused = await runOpsTool('ssh_read', JSON.stringify({ server: 'relay-1', command: 'systemctl restart nginx' }), env);
  assert.match(refused.output, /refused/);
  assert.equal(ssh.calls.length, 1, 'the change command never ran');
});

test('propose_plan stores the task and returns its id', async () => {
  let got: OpsPlan | null = null;
  const env = opsEnv(enabledSettings(), { createTask: async (p) => ((got = p), { taskId: 'T42' }) });
  const r = await runOpsTool('propose_plan', JSON.stringify({ title: 'reload', why: 'w', impact: 'i', steps: [{ target: 'relay-1', run: 'systemctl reload nginx', purpose: 'p', rollback: 'systemctl reload nginx' }], verify: ['relay-1'] }), env);
  assert.equal(r.proposedTaskId, 'T42');
  assert.ok(got, 'createTask was called');
});

// --------------------------------------------------------------- executor

const plan = (over: Partial<OpsPlan> = {}): OpsPlan =>
  normalizePlan({
    title: 'renew cert',
    steps: [{ target: 'relay-1', run: 'certbot renew --cert-name x', purpose: 'renew', backup: ['/etc/x.conf'], rollback: 'restore' }, { target: 'relay-1', run: 'systemctl reload nginx', purpose: 'reload', rollback: 'systemctl reload nginx' }],
    verify: ['main'],
    ...over,
  });

test('executor: all steps succeed, nothing gets worse → done', async () => {
  const ssh = fakeSsh(() => ({ ok: true, output: 'ok' }));
  const steps: number[] = [];
  const env = execEnv(enabledSettings(), ssh, () => new Map([['main', 'ok']]), { onStep: (l) => void steps.push(l.index) });
  const out = await runPlan(plan(), 'task-abcdef12', env);
  assert.equal(out.status, 'done');
  assert.deepEqual(steps, [0, 1]);
  assert.ok(ssh.calls.some((c) => /cp -a .*\/etc\/x\.conf.*vpnmap-bak-task-abc/.test(c.command)), 'backed up before step 1');
});

test('executor: a route gets worse after a step → rolls back in reverse, restoring backups', async () => {
  let after: Health = 'ok';
  const ran: string[] = [];
  const ssh = fakeSsh((cmd) => {
    ran.push(cmd);
    if (/systemctl reload nginx/.test(cmd)) after = 'down'; // step 2 breaks the route
    return { ok: true, output: 'ok' };
  });
  const env = execEnv(enabledSettings(), ssh, () => new Map([['main', after]]));
  const out = await runPlan(plan(), 'task-abcdef12', env);
  assert.equal(out.status, 'rolled-back');
  // rollback order: step 2 first (its explicit rollback), then step 1 (restore backup)
  const idxReload = ran.findIndex((c, i) => i > ran.indexOf('systemctl reload nginx') && /systemctl reload nginx/.test(c));
  assert.ok(idxReload > 0, 'step 2 rollback ran');
  assert.ok(ran.some((c) => /cp -a .*vpnmap-bak-task-abc.* '\/etc\/x\.conf'/.test(c)), 'step 1 backup restored');
});

test('executor: a failing step rolls everything back', async () => {
  const ssh = fakeSsh((cmd) => ({ ok: !/certbot/.test(cmd), output: /certbot/.test(cmd) ? 'cert error' : 'ok' }));
  const env = execEnv(enabledSettings(), ssh, () => new Map([['main', 'ok']]));
  const out = await runPlan(plan(), 'task-abcdef12', env);
  assert.equal(out.status, 'rolled-back');
  assert.match(out.outcome, /مرحله ۱|ناموفق/);
});

test('executor: /stop before a step halts without running it', async () => {
  const ssh = fakeSsh(() => ({ ok: true, output: 'ok' }));
  const env = execEnv(enabledSettings(), ssh, () => new Map([['main', 'ok']]), { stopped: () => true });
  const out = await runPlan(plan(), 'task-abcdef12', env);
  assert.equal(out.status, 'cancelled');
  assert.equal(ssh.calls.length, 0, 'no command ran');
});

test('executor: a refused command is never run, even in an approved plan', async () => {
  const ssh = fakeSsh(() => ({ ok: true, output: 'ok' }));
  const env = execEnv(enabledSettings(), ssh, () => new Map([['main', 'ok']]));
  const out = await runPlan(normalizePlan({ title: 'bad', steps: [{ target: 'relay-1', run: 'rm -rf /var/lib', purpose: 'x' }], verify: [] }), 'task-abcdef12', env);
  assert.equal(out.status, 'rolled-back');
  assert.ok(!ssh.calls.some((c) => /rm -rf/.test(c.command)), 'the refused command never ran');
});

test('executor: secrets are revealed only at run time, from the vault', async () => {
  const vault = new Vault();
  const ph = vault.put('REALPSK==', 'PresharedKey'); // e.g. «s1»
  const ssh = fakeSsh(() => ({ ok: true, output: 'ok' }));
  const env = execEnv(enabledSettings(), ssh, () => new Map(), { vault });
  await runPlan(normalizePlan({ title: 'set key', steps: [{ target: 'relay-1', run: `wg set wg0 peer A preshared-key <(echo ${ph})`, purpose: 'x', rollback: null }], verify: [] }), 'task-abcdef12', env);
  assert.ok(ssh.calls[0].command.includes('REALPSK=='), 'the real secret reached the server');
  assert.ok(!ssh.calls[0].command.includes(ph), 'the placeholder was substituted');
});
