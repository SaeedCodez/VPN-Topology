import { OpsParam, OpsRunnerConfig, OpsType } from '../contract';
import gen from './types.gen.json';

/**
 * The hub's copy of the closed ops catalog (pkg/ops). types.gen.json is
 * generated from pkg/ops/types.go (`vpnmap ops types --write`, checked in
 * CI); parseRequest/allow mirror pkg/ops exactly and replay the same shared
 * cases (pkg/ops/testdata/requests.json). The hub refuses early; the runner
 * on the server refuses again, whatever the hub sent.
 */

export const TYPES: OpsType[] = (gen as any).types;
export const MAX_TOKEN: number = (gen as any).max_token;
export const MAX_LINE: number = (gen as any).max_line;

export function typeByName(name: string): OpsType | undefined {
  return TYPES.find((t) => t.name === name);
}

const RE_TOKEN = /^[A-Za-z0-9][A-Za-z0-9._@:-]{0,63}$/;
const RE_KEY = /^[a-z][a-z0-9_]{0,31}$/;
const RE_REQ = /^[a-f0-9]{8,32}$/;

export const RESERVED_KEYS = new Set(['script', 'req', 'type']);

export function validToken(s: unknown): s is string {
  return typeof s === 'string' && RE_TOKEN.test(s) && !s.includes('..');
}

export function validKey(s: string): boolean {
  return RE_KEY.test(s);
}

export interface Request {
  type: string;
  args: Record<string, string>;
  id: string;
}

export function parseRequest(line: string): Request {
  if (line === '') throw new Error('empty request');
  if (line.length > MAX_LINE) throw new Error(`request longer than ${MAX_LINE} bytes`);
  for (let i = 0; i < line.length; i++) {
    const c = line.charCodeAt(i);
    if ((c < 0x21 && c !== 0x20) || c > 0x7e) throw new Error(`request contains a control or non-ASCII byte at ${i}`);
  }
  const parts = line.split(' ');
  const r: Request = { type: parts[0], args: {}, id: '' };
  if (!typeByName(r.type)) throw new Error(`unknown action type "${r.type}"`);
  for (const p of parts.slice(1)) {
    const eq = p.indexOf('=');
    const k = eq < 0 ? '' : p.slice(0, eq);
    const v = eq < 0 ? '' : p.slice(eq + 1);
    if (eq < 0 || !validKey(k)) throw new Error(`bad argument "${p}" (want key=value)`);
    if (k === 'req') {
      if (!RE_REQ.test(v) || r.id) throw new Error('bad request id');
      r.id = v;
      continue;
    }
    if (!validToken(v)) throw new Error(`argument ${k}: value is not allowed (letters, digits and . _ @ : - only, at most ${MAX_TOKEN}, not starting with -)`);
    if (k in r.args) throw new Error(`argument ${k} given twice`);
    r.args[k] = v;
  }
  return r;
}

/** Canonical form (sorted args, id last): byte-identical to pkg/ops Request.String. */
export function formatRequest(r: Request): string {
  let s = r.type;
  for (const k of Object.keys(r.args).sort()) s += ` ${k}=${r.args[k]}`;
  if (r.id) s += ` req=${r.id}`;
  return s;
}

export function paramAccepts(p: OpsParam, v: string): boolean {
  if (!validToken(v)) return false;
  if (p.enum && p.enum.length) return p.enum.includes(v);
  if (p.pattern) {
    try {
      return new RegExp(p.pattern).test(v);
    } catch {
      return false;
    }
  }
  if (p.min !== undefined && p.max !== undefined) {
    const n = Number(v);
    return Number.isInteger(n) && String(n) === v && n >= p.min && n <= p.max;
  }
  return false;
}

export interface Allowed {
  type: OpsType;
  request: Request;
  script?: NonNullable<OpsRunnerConfig['scripts']>[number];
  cooldownS: number;
}

/** pkg/ops RunnerConfig.Allow. */
export function allow(cfg: OpsRunnerConfig, r: Request): Allowed {
  const t = typeByName(r.type);
  if (!t) throw new Error(`unknown action type "${r.type}"`);
  if (t.where !== 'server') throw new Error(`${t.name} does not run on a server`);
  const used = new Set<string>();
  const arg = (name: string) => {
    if (!(name in r.args)) throw new Error(`${t.name}: missing argument ${name}`);
    used.add(name);
    return r.args[name];
  };
  const out: Allowed = { type: t, request: r, cooldownS: 0 };
  let cd = t.cooldown ?? '';
  for (const p of t.params ?? []) {
    const v = arg(p.name);
    const notAllowed = new Error(`${t.name}: ${p.name}=${v} is not allowed on ${cfg.node}`);
    switch (p.source) {
      case 'fixed':
        if (!(p.enum ?? []).includes(v)) throw new Error(`${t.name}: ${p.name} must be one of ${(p.enum ?? []).join(', ')}`);
        break;
      case 'containers':
      case 'restartable-containers': {
        const c = (cfg.containers ?? []).find((x) => x.name === v && (p.source === 'containers' || x.restart));
        if (!c) throw notAllowed;
        if (c.cooldown) cd = c.cooldown;
        break;
      }
      case 'units':
      case 'restartable-units': {
        const u = (cfg.units ?? []).find((x) => x.name === v && (p.source === 'units' || x.restart));
        if (!u) throw notAllowed;
        if (u.cooldown) cd = u.cooldown;
        break;
      }
      case 'logs':
        if (!(cfg.logs ?? []).some((x) => x.id === v)) throw notAllowed;
        break;
      case 'tcp-targets':
        if (!(cfg.tcp_targets ?? []).includes(v)) throw notAllowed;
        break;
      case 'ports': {
        const n = Number(v);
        if (!Number.isInteger(n) || String(n) !== v || !(cfg.ports ?? []).includes(n)) throw notAllowed;
        break;
      }
      case 'wg':
        if (!(cfg.wg ?? []).some((x) => x.iface === v)) throw notAllowed;
        break;
      case 'read-scripts':
      case 'change-scripts': {
        const want = p.source === 'change-scripts' ? 'change' : 'read';
        const s = (cfg.scripts ?? []).find((x) => x.id === v && x.kind === want);
        if (!s) throw notAllowed;
        out.script = s;
        for (const sp of s.params ?? []) {
          const sv = arg(sp.name);
          if (!paramAccepts(sp, sv)) throw new Error(`${t.name} ${s.id}: ${sp.name}=${sv} is not allowed`);
        }
        if (s.cooldown) cd = s.cooldown;
        break;
      }
      default:
        throw new Error(`${t.name}: parameter ${p.name} has no source`);
    }
  }
  for (const k of Object.keys(r.args)) if (!used.has(k)) throw new Error(`${t.name}: unexpected argument ${k}`);
  if (t.kind === 'change') out.cooldownS = parseGoDuration(cd);
  return out;
}

/** Go time.ParseDuration subset used in configs: "90s", "10m", "1h30m", "500ms". */
export function parseGoDuration(s: string): number {
  if (!s) return 0;
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
  let total = 0;
  let m: RegExpExecArray | null;
  let consumed = 0;
  while ((m = re.exec(s))) {
    consumed += m[0].length;
    const n = Number(m[1]);
    total += m[2] === 'ms' ? n / 1000 : m[2] === 's' ? n : m[2] === 'm' ? n * 60 : n * 3600;
  }
  return consumed === s.length ? total : 0;
}
