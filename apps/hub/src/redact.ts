/**
 * Masks secrets in free-form text. A port of pkg/redact/redact.go: both are
 * tested against pkg/redact/testdata/cases.json, so keep them identical.
 *
 * The hub needs it for the ops agent: command output from a server and
 * Remnawave answers go to the model (OpenRouter) only after this. With a
 * Vault, each secret becomes a numbered placeholder («s3») instead of
 * [REDACTED], so the model can still refer to it in a plan ("keep «s3» as
 * PrivateKey") and the executor puts the real value back on the server.
 * The model never sees the value.
 */

export const MASK = '[REDACTED]';

const SENSITIVE_SUBSTRINGS = ['password', 'passwd', 'secret', 'token', 'apikey', 'privatekey', 'shortid', 'jwt', 'authorization', 'cookie', 'credential', 'presharedkey', 'passphrase'];
const SENSITIVE_EXACT = new Set(['pass', 'pwd', 'sid', 'psk', 'auth']);

function normKey(k: string): string {
  return k.toLowerCase().replace(/[_\-. ]/g, '');
}

export function isSensitiveKey(k: string): boolean {
  const n = normKey(k);
  if (!n) return false;
  if (SENSITIVE_EXACT.has(n)) return true;
  return SENSITIVE_SUBSTRINGS.some((s) => n.includes(s));
}

const RE_URL_USERINFO = /([A-Za-z][A-Za-z0-9+.-]*:\/\/)([^\s/@:"']+:[^\s/"']*)@/g;
const RE_AUTH_HEADER = /(["']?(?:proxy-)?authorization["']?\s*[:=]\s*["']?)(?:(bearer|basic|token|digest)\s+)?([^\s"',;]+)/gi;
const RE_BEARER = /\b(bearer)\s+([A-Za-z0-9._~+/=-]{6,})/gi;
const RE_KEY_VALUE = /(["']?)([A-Za-z0-9_.-]{2,64})(["']?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|\[[^\]]*\]|[^\s,;&}\]"']+)/g;
const RE_JWT = /\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*/g;
const RE_UUID = /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi;
const RE_BLOB = /[A-Za-z0-9+/_-]{32,}={0,2}/g;
const RE_PLACEHOLDER = /«s\d+»/;

/** Secret value → placeholder, per agent run / ops task. */
export class Vault {
  private byValue = new Map<string, string>();
  private byName = new Map<string, { value: string; label: string }>();

  constructor(entries: { name: string; value: string; label: string }[] = []) {
    for (const e of entries) {
      this.byValue.set(e.value, e.name);
      this.byName.set(e.name, { value: e.value, label: e.label });
    }
  }

  put(value: string, label: string): string {
    const hit = this.byValue.get(value);
    if (hit) return hit;
    const name = `«s${this.byName.size + 1}»`;
    this.byValue.set(value, name);
    this.byName.set(name, { value, label });
    return name;
  }

  /** Put the real values back (only the executor does this, right before running on a server). */
  reveal(s: string): string {
    return s.replace(/«s\d+»/g, (m) => this.byName.get(m)?.value ?? m);
  }

  /** Placeholders used in s with what they stand for (never the value). */
  labels(s: string): { name: string; label: string }[] {
    const seen = new Set<string>();
    const out: { name: string; label: string }[] = [];
    for (const m of s.match(/«s\d+»/g) ?? []) {
      if (seen.has(m) || !this.byName.has(m)) continue;
      seen.add(m);
      out.push({ name: m, label: this.byName.get(m)!.label });
    }
    return out;
  }

  get size(): number {
    return this.byName.size;
  }

  /** For storage (encrypted by the caller). */
  entries(): { name: string; value: string; label: string }[] {
    return [...this.byName].map(([name, e]) => ({ name, value: e.value, label: e.label }));
  }
}

type Masker = (value: string, label: string) => string;

const plainMask: Masker = () => MASK;

/** s with every secret-looking value replaced by [REDACTED], or by a vault placeholder. */
export function redact(s: string, vault?: Vault): string {
  return redactWith(s, vault ? (v, l) => vault.put(v, l) : plainMask);
}

function isMasked(v: string): boolean {
  return v.includes(MASK) || RE_PLACEHOLDER.test(v);
}

function redactWith(s: string, mask: Masker): string {
  if (!s) return s;
  s = s.replace(RE_URL_USERINFO, (_m, scheme: string, info: string) => `${scheme}${isMasked(info) ? info : mask(info, 'url credentials')}@`);
  s = s.replace(RE_AUTH_HEADER, (m, head: string, scheme: string | undefined, val: string) => {
    if (isMasked(val)) return m;
    return head + (scheme ? `${scheme} ` : '') + mask(val, 'authorization');
  });
  s = s.replace(RE_BEARER, (m, word: string, val: string) => (isMasked(val) ? m : `${word} ${mask(val, 'bearer token')}`));
  s = s.replace(RE_KEY_VALUE, (m, q1: string, key: string, sep: string, val: string) => {
    if (!isSensitiveKey(key)) return q1 + key + sep + redactWith(val, mask);
    if (isMasked(val) || normKey(key).includes('authorization')) return m;
    if (val.startsWith('"')) return `${q1}${key}${sep}"${mask(val.slice(1, -1), key)}"`;
    if (val.startsWith("'")) return `${q1}${key}${sep}'${mask(val.slice(1, -1), key)}'`;
    return q1 + key + sep + mask(val, key);
  });
  s = s.replace(RE_JWT, (m) => mask(m, 'jwt'));
  s = s.replace(RE_UUID, (m) => mask(m, 'uuid'));
  s = s.replace(RE_BLOB, (m) => (looksLikeBlob(m) ? mask(m, 'key') : m));
  return s;
}

function looksLikeBlob(m: string): boolean {
  const core = m.replace(/=+$/, '');
  if (core.length < 32) return false;
  let upper = 0;
  let lower = 0;
  let digit = 0;
  let seps = 0;
  let hexOnly = true;
  for (const r of core) {
    if (r >= 'A' && r <= 'Z') {
      upper++;
      if (r > 'F') hexOnly = false;
    } else if (r >= 'a' && r <= 'z') {
      lower++;
      if (r > 'f') hexOnly = false;
    } else if (r >= '0' && r <= '9') digit++;
    else if (r === '/' || r === '-' || r === '_') {
      seps++;
      hexOnly = false;
    } else hexOnly = false;
  }
  if (hexOnly && digit > 0) return true;
  if (seps * 8 > core.length) return false;
  const padded = m.length !== core.length;
  if (padded && core.length >= 40) return true;
  return upper > 0 && lower > 0 && digit > 0;
}

/** Deep copy of a JSON value with secrets masked (values of sensitive keys entirely). */
export function redactValue(v: unknown, vault?: Vault): unknown {
  if (typeof v === 'string') return redact(v, vault);
  if (Array.isArray(v)) return v.map((e) => redactValue(e, vault));
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, e] of Object.entries(v)) {
      if (isSensitiveKey(k) && e !== null && e !== undefined && e !== '') {
        out[k] = vault ? vault.put(typeof e === 'string' ? e : JSON.stringify(e), k) : MASK;
      } else out[k] = redactValue(e, vault);
    }
    return out;
  }
  return v;
}

/** Reverse of redactValue for strings inside a JSON value (API bodies in a plan). */
export function revealValue(v: unknown, vault: Vault): unknown {
  if (typeof v === 'string') {
    const exact = /^«s\d+»$/.test(v) ? vault.reveal(v) : null;
    if (exact !== null && exact !== v && /^[[{]/.test(exact)) {
      try {
        return JSON.parse(exact);
      } catch {
        /* a plain string that happens to start with a bracket */
      }
    }
    return vault.reveal(v);
  }
  if (Array.isArray(v)) return v.map((e) => revealValue(e, vault));
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, e]) => [k, revealValue(e, vault)]));
  return v;
}
