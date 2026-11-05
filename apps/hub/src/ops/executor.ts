import { Health } from '../contract';
import { OpsPlan, OpsStep, OpsStepLog } from '../contract';
import { revealValue, Vault } from '../redact';
import { classify } from './classify';
import { RemnawaveClient } from './remnawave';
import { OpsServer, serverReachable, StoredOps } from './settings';
import { SshExec, SshTarget } from './ssh';

/**
 * Runs an approved plan, deterministically — no model in the loop. For each
 * step it backs up the files the plan named, runs the step, then waits for a
 * fresh map reading and compares the plan's `verify` ids with the baseline
 * taken before the task. If anything got worse, or a step failed, it rolls the
 * completed steps back in reverse (restoring backups first) and stops. A
 * command the classifier refuses is never run, even here. The real secret
 * values (held in the vault) are put back only at the moment of running on the
 * server; they are never in the stored plan or the log.
 */

const RANK: Record<Health, number> = { ok: 0, unknown: 1, degraded: 2, down: 3 };

export interface ExecEnv {
  settings: StoredOps;
  privateKey: string | null;
  sshExec: SshExec;
  rw: RemnawaveClient;
  vault: Vault;
  /** Current status of these map ids (from the latest snapshot). */
  verify: (ids: string[]) => Promise<Map<string, Health>>;
  /** Wait for a fresh observation after a change (≈ one collector tick). */
  settle: () => Promise<void>;
  /** Append a step result (persisted + shown on the board). */
  onStep: (log: OpsStepLog) => Promise<void> | void;
  /** Update the Telegram progress message. */
  onProgress: (text: string) => Promise<void> | void;
  /** Kill switch / `/stop`: halt before the next step. */
  stopped: () => boolean;
}

export interface ExecOutcome {
  status: 'done' | 'failed' | 'rolled-back' | 'cancelled';
  outcome: string;
  verify: { id: string; before: Health; after: Health; worse: boolean }[];
  log: OpsStepLog[];
}

export async function runPlan(plan: OpsPlan, taskId: string, env: ExecEnv): Promise<ExecOutcome> {
  const log: OpsStepLog[] = [];
  const done: { step: OpsStep; index: number }[] = [];
  const baseline = await env.verify(plan.verify);
  const short = taskId.slice(0, 8);

  const record = async (l: OpsStepLog) => {
    log.push(l);
    await env.onStep(l);
  };

  for (let i = 0; i < plan.steps.length; i++) {
    const step = plan.steps[i];
    if (env.stopped()) {
      await record({ index: i, target: step.target, summary: summariseStep(step), status: 'skipped', output: 'stopped before this step', at: new Date().toISOString() });
      return finish('cancelled', 'متوقف شد؛ مرحله‌های بعدی اجرا نشدند.', baseline, baseline, log);
    }

    // Defence in depth: never run a refused command, even in an approved plan.
    if (typeof step.run === 'string') {
      const c = classify(step.run);
      if (c.kind === 'refused') {
        await record({ index: i, target: step.target, summary: summariseStep(step), status: 'failed', output: `refused: ${c.reason}`, at: new Date().toISOString() });
        await rollback(done, taskId, env);
        return finish('rolled-back', `مرحله ${i + 1} رد شد (${c.reason})؛ تغییرهای قبلی برگردانده شد.`, baseline, await env.verify(plan.verify), log);
      }
    }

    // Back up the files this step names, so it can be restored.
    if (step.backup?.length && step.target !== 'remnawave') {
      const srv = server(env, step.target);
      const bak = step.backup.map((f) => `cp -a -- ${sh(f)} ${sh(f + '.vpnmap-bak-' + short)} 2>/dev/null || true`).join(' && ');
      if (srv) await env.sshExec(toTarget(srv), env.privateKey ?? '', bak, { vault: env.vault, expectHostKey: srv.hostKey });
    }

    const res = await runStep(step, env);
    await record({ index: i, target: step.target, summary: summariseStep(step), status: res.ok ? 'ok' : 'failed', output: res.output, at: new Date().toISOString() });

    if (!res.ok) {
      await env.onProgress(`مرحله ${i + 1} ناموفق بود؛ در حال برگرداندن…`);
      await rollback([...done, { step, index: i }], taskId, env);
      return finish('rolled-back', `مرحله ${i + 1} ناموفق بود؛ تغییرها برگردانده شد.`, baseline, await env.verify(plan.verify), log);
    }
    done.push({ step, index: i });

    // Did anything on the map get worse?
    if (plan.verify.length) {
      await env.settle();
      const now = await env.verify(plan.verify);
      const worse = plan.verify.filter((id) => isWorse(baseline.get(id), now.get(id)));
      if (worse.length) {
        await env.onProgress(`بعد از مرحله ${i + 1} وضعیت ${worse.join('، ')} بدتر شد؛ در حال برگرداندن…`);
        await rollback(done, taskId, env);
        return finish('rolled-back', `بعد از مرحله ${i + 1}، ${worse.join('، ')} بدتر شد؛ همه‌ی تغییرها برگردانده شد.`, baseline, await env.verify(plan.verify), log);
      }
    }
    await env.onProgress(`مرحله ${i + 1} از ${plan.steps.length} انجام شد.`);
  }

  return finish('done', 'انجام شد.', baseline, await env.verify(plan.verify), log);

  function finish(status: ExecOutcome['status'], outcome: string, before: Map<string, Health>, after: Map<string, Health>, l: OpsStepLog[]): ExecOutcome {
    return {
      status,
      outcome,
      log: l,
      verify: plan.verify.map((id) => ({ id, before: before.get(id) ?? 'unknown', after: after.get(id) ?? 'unknown', worse: isWorse(before.get(id), after.get(id)) })),
    };
  }
}

function isWorse(before: Health | undefined, after: Health | undefined): boolean {
  if (!before || !after || before === 'unknown') return false; // no trustworthy baseline
  return RANK[after] > RANK[before];
}

async function runStep(step: OpsStep, env: ExecEnv): Promise<{ ok: boolean; output: string }> {
  if (step.target === 'remnawave') {
    if (typeof step.run !== 'object') return { ok: false, output: 'remnawave step without a call' };
    const body = revealValue(step.run.body, env.vault);
    const r = await env.rw.request(step.run.method, step.run.path, body, env.vault);
    return { ok: r.ok, output: `${r.status} ${r.error ?? 'ok'} ${JSON.stringify(r.body).slice(0, 2000)}` };
  }
  const srv = server(env, step.target);
  if (!srv) return { ok: false, output: `unknown server ${step.target}` };
  if (!serverReachable(env.settings, srv)) return { ok: false, output: `server ${step.target} not reachable` };
  const cmd = typeof step.run === 'string' ? env.vault.reveal(step.run) : '';
  const r = await env.sshExec(toTarget(srv), env.privateKey ?? '', cmd, { vault: env.vault, expectHostKey: srv.hostKey });
  return { ok: r.ok, output: r.output || `exit ${r.code}${r.error ? ': ' + r.error : ''}` };
}

async function rollback(done: { step: OpsStep; index: number }[], taskId: string, env: ExecEnv) {
  const short = taskId.slice(0, 8);
  for (let i = done.length - 1; i >= 0; i--) {
    const { step } = done[i];
    try {
      // Restore backed-up files first.
      if ((step.rollback === 'restore' || step.backup?.length) && step.target !== 'remnawave') {
        const srv = server(env, step.target);
        if (srv && step.backup?.length) {
          const restore = step.backup.map((f) => `test -e ${sh(f + '.vpnmap-bak-' + short)} && cp -a -- ${sh(f + '.vpnmap-bak-' + short)} ${sh(f)} || true`).join(' && ');
          await env.sshExec(toTarget(srv), env.privateKey ?? '', restore, { vault: env.vault, expectHostKey: srv.hostKey });
        }
      }
      // Then run an explicit rollback command / API call.
      if (step.rollback && step.rollback !== 'restore') {
        await runStep({ ...step, run: step.rollback as OpsStep['run'] }, env);
      }
    } catch {
      /* best effort: keep rolling back the rest */
    }
  }
}

function server(env: ExecEnv, id: string): OpsServer | undefined {
  return env.settings.servers.find((s) => s.id === id);
}
function toTarget(s: OpsServer): SshTarget {
  return { host: s.host, port: s.port, user: s.user };
}
function sh(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

export function summariseStep(step: OpsStep): string {
  const what = typeof step.run === 'string' ? step.run : `${step.run.method} ${step.run.path}`;
  return `[${step.target}] ${what}`.slice(0, 300);
}
