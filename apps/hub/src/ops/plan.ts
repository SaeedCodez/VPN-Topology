import { createHash } from 'crypto';
import { Bundle, OpsNodePolicy, OpsPlan, OpsPlanInput, OpsStep, OpsStepInput } from '../contract';
import { allow, formatRequest, parseGoDuration, typeByName, validKey, validToken } from './catalog';

/**
 * Turns what the model proposes into a validated plan, or refuses it whole.
 * Every value comes from the closed types (catalog.ts) and the node policies
 * in the applied bundle (the ops: blocks); nothing the model writes becomes
 * a command. The same function re-validates each step (and each rollback)
 * right before the executor runs it.
 */

export const MAX_STEPS = 5;
const MAX_TEXT = 400;

export function policyFor(bundle: Bundle, node: string): OpsNodePolicy | undefined {
  return (bundle.ops ?? []).find((p) => p.node === node);
}

/** Model-written text shown in Telegram: one line-ish, no control chars, capped. */
export function cleanText(v: unknown, what: string, required = true): string {
  const s = typeof v === 'string' ? v.replace(/[\u0000-\u0008\u000b-\u001f\u007f​-‏‪-‮⁦-⁩]/g, '').trim() : '';
  if (required && !s) throw new Error(`${what} is required`);
  if (s.length > MAX_TEXT) throw new Error(`${what} is longer than ${MAX_TEXT} characters`);
  return s;
}

function cleanArgs(v: unknown, where: string): Record<string, string> {
  if (v === undefined || v === null) return {};
  if (typeof v !== 'object' || Array.isArray(v)) throw new Error(`${where}: args must be an object of strings`);
  const out: Record<string, string> = {};
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    const s = typeof val === 'number' && Number.isInteger(val) ? String(val) : val;
    if (!validKey(k)) throw new Error(`${where}: bad argument name "${k}"`);
    if (!validToken(s)) throw new Error(`${where}: ${k} is not an allowed value (letters, digits and . _ @ : - only)`);
    out[k] = s;
  }
  return out;
}

function substitute(tpl: Record<string, string> | undefined, args: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(tpl ?? {})) {
    const m = /^\{([a-z][a-z0-9_]*)\}$/.exec(v);
    out[k] = m && m[1] in args ? args[m[1]] : v;
  }
  return out;
}

/**
 * Validates one action on one node. `kind` restricts it to reads (the read
 * tool) or changes (plan steps). Returns the canonical step.
 */
export function validateStep(bundle: Bundle, input: OpsStepInput, kind: 'read' | 'change'): OpsStep {
  if (!input || typeof input !== 'object') throw new Error('step must be an object {node, type, args}');
  const where = `step ${input.node}/${input.type}`;
  if (!validToken(input.node)) throw new Error(`${where}: bad node id`);
  const t = typeof input.type === 'string' ? typeByName(input.type) : undefined;
  if (!t) throw new Error(`${where}: unknown action type (see ops_catalog)`);
  if (t.kind !== kind) throw new Error(`${where}: ${t.name} is a ${t.kind} action; ${kind === 'read' ? 'use ops_propose for changes' : 'reads run with ops_read'}`);
  const p = policyFor(bundle, input.node);
  if (!p) throw new Error(`${where}: node ${input.node} has no ops: block in the topology`);
  const args = cleanArgs(input.args, where);

  const step: OpsStep = {
    node: p.node,
    type: t.name,
    args,
    line: '',
    where: t.where,
    title: t.title,
    verify: [...p.verify],
    cutsUsers: false,
    cutRoutes: [],
    cooldownS: 0,
    reversible: false,
    rollback: null,
    readBefore: null,
  };
  let disruptive = t.disruptive;

  if (t.where === 'remnawave') {
    if (t.params?.length) {
      if (!p.remnawave || !p.remnawave.actions.includes(t.name)) throw new Error(`${where}: ${t.name} is not allowed for ${p.node} (ops.remnawave)`);
      if (args.node !== undefined && args.node !== p.node) throw new Error(`${where}: node must be ${p.node}`);
      for (const k of Object.keys(args)) if (k !== 'node') throw new Error(`${where}: unexpected argument ${k}`);
      step.args = { node: p.node };
    } else if (Object.keys(args).length) {
      throw new Error(`${where}: ${t.name} takes no arguments`);
    }
    step.line = formatRequest({ type: t.name, args: step.args, id: '' });
    step.cooldownS = parseGoDuration(t.cooldown ?? '');
    if (t.rollback) step.rollback = { type: t.rollback, args: { ...step.args } };
  } else {
    if (!p.runner || !p.config) throw new Error(`${where}: ${p.node} has no runner (ops.runner: true)`);
    const a = allow(p.config, { type: t.name, args, id: '' });
    step.line = formatRequest(a.request);
    step.cooldownS = a.cooldownS;
    if (a.script) {
      const sp = (p.scripts ?? []).find((s) => s.id === a.script!.id);
      step.title = sp?.title || `${t.title}: ${a.script.id}`;
      if (sp) {
        disruptive = t.kind === 'change' ? sp.disruptive : false;
        for (const v of sp.verify ?? []) step.verify.push(v.replace(/\{([a-z][a-z0-9_]*)\}/g, (_, k) => args[k] ?? `{${k}}`));
        if (sp.read_before) step.readBefore = { type: 'script.read', args: { script: sp.read_before.script, ...substitute(sp.read_before.args, args) } };
        if (sp.rollback) step.rollback = { type: 'script.change', args: { script: sp.rollback.script, ...substitute(sp.rollback.args, args) } };
      }
    }
  }
  step.verify = [...new Set(step.verify)];
  step.reversible = !!step.rollback;
  if (t.kind === 'change' && disruptive && p.cuts_users) {
    step.cutsUsers = true;
    step.cutRoutes = p.cut_routes ?? [];
  }
  // A read_before must itself be an allowed read on this node.
  if (step.readBefore) validateStep(bundle, { node: p.node, ...step.readBefore }, 'read');
  // A rollback must be allowed too, except for values only known after read_before.
  if (step.rollback && !Object.values(step.rollback.args).some((v) => v.startsWith('{before.'))) {
    validateStep(bundle, { node: p.node, ...step.rollback }, 'change');
  }
  return step;
}

/** Fills {before.<field>} in a rollback from the read_before JSON output. */
export function fillRollback(rb: { type: string; args: Record<string, string> }, beforeOutput: string): { type: string; args: Record<string, string> } {
  let before: Record<string, unknown> = {};
  try {
    const j = JSON.parse(beforeOutput.trim().split('\n').pop() ?? '{}');
    if (j && typeof j === 'object' && !Array.isArray(j)) before = j;
  } catch {
    throw new Error('read_before did not print a JSON object');
  }
  const args: Record<string, string> = {};
  for (const [k, v] of Object.entries(rb.args)) {
    const m = /^\{before\.([a-z][a-z0-9_]*)\}$/.exec(v);
    if (!m) {
      args[k] = v;
      continue;
    }
    const val = before[m[1]];
    if (!validToken(val)) throw new Error(`read_before field ${m[1]} is missing or not a valid value`);
    args[k] = val;
  }
  return { type: rb.type, args };
}

export function validatePlan(bundle: Bundle, input: OpsPlanInput): OpsPlan {
  if (!input || typeof input !== 'object') throw new Error('plan must be an object');
  const title = cleanText(input.title, 'title');
  const why = cleanText(input.why, 'why');
  const impact = cleanText(input.impact, 'impact');
  if (!Array.isArray(input.steps) || input.steps.length === 0) throw new Error('a plan needs at least one step');
  if (input.steps.length > MAX_STEPS) throw new Error(`a plan has at most ${MAX_STEPS} steps`);
  const steps = input.steps.map((s) => validateStep(bundle, s, 'change'));
  const seen = new Set<string>();
  for (const s of steps) {
    const k = `${s.node} ${s.line}`;
    if (seen.has(k)) throw new Error(`step ${k} appears twice`);
    seen.add(k);
  }
  return {
    title,
    why,
    impact,
    steps,
    verify: [...new Set(steps.flatMap((s) => s.verify))],
    cutsUsers: steps.some((s) => s.cutsUsers),
  };
}

/** Stable hash of a validated plan (keys sorted at every level). */
export function planHash(plan: OpsPlan): string {
  return createHash('sha256').update(stable(plan)).digest('hex');
}

function stable(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(stable).join(',')}]`;
  if (v && typeof v === 'object') {
    return `{${Object.keys(v as object)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${stable((v as any)[k])}`)
      .join(',')}}`;
  }
  return JSON.stringify(v);
}
