import { Health, OpsPlan, OpsStep, OpsStepLog } from '../contract';
import { fillRollback } from './plan';

/**
 * The deterministic executor. It runs an approved, validated plan step by
 * step; the model is not involved. Before each step it checks the stop flag
 * and the stored plan hash, and re-validates the step against the current
 * topology. After each step it waits, re-reads the verify ids and rolls back
 * the completed reversible steps (in reverse, each re-validated) if anything
 * got worse or the step failed.
 */

export interface StepResult {
  ok: boolean;
  output: string;
  error?: string;
}

export interface ExecEnv {
  /** Re-validate a step (or a rollback) against the live topology; throws if no longer allowed. */
  revalidate(node: string, s: { type: string; args: Record<string, string> }): OpsStep;
  run(step: OpsStep): Promise<StepResult>;
  verify(ids: string[]): Promise<Map<string, Health>>;
  settle(): Promise<void>;
  /** true while the stored plan still has the approved hash. */
  hashOk(): Promise<boolean>;
  onLog(l: OpsStepLog): Promise<void>;
  onProgress(text: string): Promise<void>;
  stopped(): boolean;
  now?: () => number;
}

export interface ExecOutcome {
  status: 'done' | 'failed' | 'rolled-back' | 'cancelled';
  outcome: string;
  verify: { id: string; before: Health; after: Health }[];
  log: OpsStepLog[];
}

const RANK: Record<string, number> = { ok: 0, unknown: 1, degraded: 2, down: 3 };

/** after is worse than before (an unknown or missing baseline never counts). */
export function worse(before: Health | undefined, after: Health | undefined): boolean {
  if (!before || before === 'unknown' || !after) return false;
  return (RANK[after] ?? 1) > (RANK[before] ?? 1);
}

export async function runPlan(plan: OpsPlan, env: ExecEnv): Promise<ExecOutcome> {
  const now = env.now ?? Date.now;
  const log: OpsStepLog[] = [];
  const add = async (l: Omit<OpsStepLog, 'at'>) => {
    const e = { ...l, output: l.output.slice(0, 4000), at: new Date(now()).toISOString() };
    log.push(e);
    await env.onLog(e);
  };
  const baseline = await env.verify(plan.verify);
  const done: { index: number; step: OpsStep; rollback: OpsStep | null }[] = [];

  const rollBack = async (why: string): Promise<ExecOutcome['status']> => {
    let any = false;
    for (const d of [...done].reverse()) {
      if (!d.rollback) continue;
      any = true;
      await env.onProgress(`بازگرداندن مرحله‌ی ${d.index + 1}…`);
      let rb: OpsStep;
      try {
        rb = env.revalidate(d.step.node, d.rollback);
      } catch (e: any) {
        await add({ index: d.index, node: d.step.node, line: d.rollback.line, status: 'failed', output: `rollback refused: ${e?.message ?? e}` });
        continue;
      }
      const r = await env.run(rb).catch((e: any): StepResult => ({ ok: false, output: '', error: String(e?.message ?? e) }));
      await add({ index: d.index, node: rb.node, line: rb.line, status: r.ok ? 'rolled-back' : 'failed', output: r.ok ? r.output : r.error ?? r.output });
    }
    void why;
    return any ? 'rolled-back' : 'failed';
  };

  for (let i = 0; i < plan.steps.length; i++) {
    const planned = plan.steps[i];
    if (env.stopped()) {
      await add({ index: i, node: planned.node, line: planned.line, status: 'skipped', output: 'stopped' });
      return { status: 'cancelled', outcome: `متوقف شد پیش از مرحله‌ی ${i + 1}؛ مرحله‌های انجام‌شده برگردانده نشدند.`, verify: [], log };
    }
    if (!(await env.hashOk())) {
      const st = await rollBack('hash');
      return { status: st, outcome: 'پلن ذخیره‌شده با پلن تأییدشده یکی نیست؛ اجرا متوقف شد.', verify: [], log };
    }
    let step: OpsStep;
    try {
      step = env.revalidate(planned.node, planned);
      if (step.line !== planned.line) throw new Error(`the step now resolves to "${step.line}"`);
    } catch (e: any) {
      await add({ index: i, node: planned.node, line: planned.line, status: 'failed', output: `refused: ${e?.message ?? e}` });
      const st = await rollBack('refused');
      return { status: st, outcome: `مرحله‌ی ${i + 1} دیگر مجاز نیست: ${e?.message ?? e}`, verify: [], log };
    }

    // Read the state the rollback needs, before changing anything.
    let rollback: OpsStep | null = null;
    if (step.rollback) {
      try {
        let tpl = step.rollback;
        if (step.readBefore) {
          const rbStep = env.revalidate(step.node, step.readBefore);
          const r = await env.run(rbStep);
          if (!r.ok) throw new Error(r.error ?? 'read_before failed');
          tpl = fillRollback(step.rollback, r.output);
        }
        rollback = env.revalidate(step.node, tpl);
      } catch (e: any) {
        await add({ index: i, node: step.node, line: step.line, status: 'failed', output: `not run: cannot prepare the rollback: ${e?.message ?? e}` });
        const st = await rollBack('no-rollback');
        return { status: st, outcome: `مرحله‌ی ${i + 1} اجرا نشد چون بازگشت آن آماده نشد.`, verify: [], log };
      }
    }

    await env.onProgress(`مرحله‌ی ${i + 1} از ${plan.steps.length}: ${step.title}`);
    const r = await env.run(step).catch((e: any): StepResult => ({ ok: false, output: '', error: String(e?.message ?? e) }));
    await add({ index: i, node: step.node, line: step.line, status: r.ok ? 'ok' : 'failed', output: r.ok ? r.output : r.error ?? r.output });
    // A failed step may have half-applied: its own rollback runs too.
    done.push({ index: i, step, rollback });
    if (!r.ok) {
      const st = await rollBack('failed');
      return { status: st, outcome: `مرحله‌ی ${i + 1} ناموفق بود: ${(r.error ?? '').slice(0, 300)}`, verify: [], log };
    }

    await env.onProgress(`مرحله‌ی ${i + 1} انجام شد؛ بررسی سلامت…`);
    await env.settle();
    const after = await env.verify(plan.verify);
    const bad = plan.verify.filter((id) => worse(baseline.get(id), after.get(id))).map((id) => ({ id, before: baseline.get(id)!, after: after.get(id)! }));
    if (bad.length) {
      const st = await rollBack('worse');
      return { status: st, outcome: `بعد از مرحله‌ی ${i + 1} وضعیت بدتر شد (${bad.map((b) => `${b.id} ${b.before}→${b.after}`).join('، ')})${st === 'rolled-back' ? '؛ برگردانده شد' : '؛ مرحله‌ی قابل بازگشتی نبود'}.`, verify: bad, log };
    }
  }
  const final = await env.verify(plan.verify);
  const changed = plan.verify.filter((id) => baseline.get(id) !== final.get(id) && final.get(id)).map((id) => ({ id, before: baseline.get(id) ?? 'unknown', after: final.get(id)! }));
  return { status: 'done', outcome: `همه‌ی ${plan.steps.length} مرحله انجام شد.`, verify: changed, log };
}
