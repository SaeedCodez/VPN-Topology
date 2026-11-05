import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from 'crypto';
import { AssistantSettingsView } from '../contract';

/**
 * Assistant settings, edited from the board so the model or key can change
 * without a redeploy. The OpenRouter key is the one secret kept outside the
 * Coolify env: it is stored AES-256-GCM encrypted with a key derived from
 * SESSION_SECRET, and it is write-only — nothing returns it, only a hint.
 * If SESSION_SECRET changes, the stored key can no longer be read and has to
 * be entered again (the board says so).
 */

export const SETTINGS_KEY = 'assistant';

export interface StoredAssistant {
  enabled: boolean;
  model: string;
  allowedUserIds: string[];
  dailyLimit: number;
  key?: { iv: string; tag: string; data: string };
  keyHint?: string;
}

export const DEFAULTS: StoredAssistant = { enabled: false, model: '', allowedUserIds: [], dailyLimit: 50 };

export interface AssistantPatch {
  enabled?: boolean;
  model?: string;
  allowedUserIds?: (string | number)[] | string;
  dailyLimit?: number;
  /** New OpenRouter key; "" clears it; absent keeps it. */
  key?: string;
}

function derive(secret: string): Buffer {
  return Buffer.from(hkdfSync('sha256', secret, 'vpnmap-settings', 'assistant-openrouter-key', 32));
}

export function encrypt(plain: string, secret: string): { iv: string; tag: string; data: string } {
  const iv = randomBytes(12);
  const c = createCipheriv('aes-256-gcm', derive(secret), iv);
  const data = Buffer.concat([c.update(plain, 'utf8'), c.final()]);
  return { iv: iv.toString('base64'), tag: c.getAuthTag().toString('base64'), data: data.toString('base64') };
}

export function decrypt(box: { iv: string; tag: string; data: string }, secret: string): string | null {
  try {
    const d = createDecipheriv('aes-256-gcm', derive(secret), Buffer.from(box.iv, 'base64'));
    d.setAuthTag(Buffer.from(box.tag, 'base64'));
    return Buffer.concat([d.update(Buffer.from(box.data, 'base64')), d.final()]).toString('utf8');
  } catch {
    return null;
  }
}

export function keyHint(key: string): string {
  return key.length > 12 ? `${key.slice(0, 6)}…${key.slice(-4)}` : '…';
}

/** Telegram user ids: digits only, deduplicated. */
export function parseUserIds(v: AssistantPatch['allowedUserIds']): string[] {
  const list = Array.isArray(v) ? v.map(String) : String(v ?? '').split(/[\s,]+/);
  return [...new Set(list.map((x) => x.trim()).filter((x) => /^\d{3,20}$/.test(x)))];
}

/** Apply a patch; returns the new stored value and human-readable changes (never the key). */
export function applyPatch(cur: StoredAssistant, p: AssistantPatch, secret: string): { next: StoredAssistant; changes: string[] } {
  const next: StoredAssistant = { ...cur, allowedUserIds: [...cur.allowedUserIds] };
  const changes: string[] = [];
  if (p.enabled !== undefined && !!p.enabled !== cur.enabled) {
    next.enabled = !!p.enabled;
    changes.push(next.enabled ? 'enabled' : 'disabled');
  }
  if (p.model !== undefined) {
    const m = String(p.model).trim();
    if (m.length > 120 || /\s/.test(m)) throw new Error('model must be an OpenRouter model id like "google/gemini-2.5-flash"');
    if (m !== cur.model) {
      next.model = m;
      changes.push(`model → ${m || '(none)'}`);
    }
  }
  if (p.allowedUserIds !== undefined) {
    const ids = parseUserIds(p.allowedUserIds);
    if (ids.join(',') !== cur.allowedUserIds.join(',')) {
      next.allowedUserIds = ids;
      changes.push(`allowed users → ${ids.length}`);
    }
  }
  if (p.dailyLimit !== undefined) {
    const n = Math.round(Number(p.dailyLimit));
    if (!Number.isFinite(n) || n < 1 || n > 10_000) throw new Error('daily limit must be between 1 and 10000');
    if (n !== cur.dailyLimit) {
      next.dailyLimit = n;
      changes.push(`daily limit → ${n}`);
    }
  }
  if (p.key !== undefined) {
    const k = String(p.key).trim();
    if (!k) {
      if (cur.key) changes.push('key cleared');
      delete next.key;
      delete next.keyHint;
    } else {
      if (k.length > 300 || /\s/.test(k)) throw new Error('that does not look like an API key');
      next.key = encrypt(k, secret);
      next.keyHint = keyHint(k);
      changes.push('key replaced');
    }
  }
  return { next, changes };
}

/** What the API returns: everything but the key. */
export function publicView(s: StoredAssistant, secret: string): Pick<AssistantSettingsView, 'enabled' | 'model' | 'allowedUserIds' | 'dailyLimit' | 'hasKey' | 'keyHint' | 'keyUnreadable'> {
  return {
    enabled: s.enabled,
    model: s.model,
    allowedUserIds: s.allowedUserIds,
    dailyLimit: s.dailyLimit,
    hasKey: !!s.key,
    keyHint: s.keyHint ?? null,
    keyUnreadable: !!s.key && decrypt(s.key, secret) === null,
  };
}
