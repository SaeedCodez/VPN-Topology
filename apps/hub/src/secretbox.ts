import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from 'crypto';

/**
 * AES-256-GCM for the few secrets the hub keeps in its own database (entered
 * on the board, not in the Coolify env): the assistant's OpenRouter key, the
 * ops agent's SSH key, and the secrets of a pending ops task. Each purpose
 * gets its own key, derived with HKDF from SESSION_SECRET. If SESSION_SECRET
 * changes, the stored values can no longer be read (open returns null).
 */

export interface Box {
  iv: string;
  tag: string;
  data: string;
}

function derive(secret: string, purpose: string): Buffer {
  return Buffer.from(hkdfSync('sha256', secret, 'vpnmap-settings', purpose, 32));
}

export function seal(plain: string, secret: string, purpose: string): Box {
  const iv = randomBytes(12);
  const c = createCipheriv('aes-256-gcm', derive(secret, purpose), iv);
  const data = Buffer.concat([c.update(plain, 'utf8'), c.final()]);
  return { iv: iv.toString('base64'), tag: c.getAuthTag().toString('base64'), data: data.toString('base64') };
}

export function open(box: Box, secret: string, purpose: string): string | null {
  try {
    const d = createDecipheriv('aes-256-gcm', derive(secret, purpose), Buffer.from(box.iv, 'base64'));
    d.setAuthTag(Buffer.from(box.tag, 'base64'));
    return Buffer.concat([d.update(Buffer.from(box.data, 'base64')), d.final()]).toString('utf8');
  } catch {
    return null;
  }
}
