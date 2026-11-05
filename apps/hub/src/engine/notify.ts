import { RootCause } from '../contract';
import { AlertTransition } from './alerts';

/**
 * Decides which Telegram messages an alert transition produces. Pure: no I/O,
 * the caller passes the time and sends what comes back.
 *
 * - fired → one message, unless the alert (or that instance) is silenced.
 * - resolved → one message, only for alerts we announced, and only after
 *   RESOLVE_GRACE_MS: if it fires again within the grace period (flapping),
 *   neither the "resolved" nor the second "fired" is sent.
 * - after a hub restart the alert engine starts empty, so alerts that were
 *   still firing fire again; the ones the event log says were already firing
 *   are not announced a second time (their "resolved" is still sent).
 */

export const RESOLVE_GRACE_MS = 2 * 60_000;
export const RESTART_GRACE_MS = 15 * 60_000;

export interface OutMessage {
  key: string; // "<alert id>|<instance>"
  kind: 'fired' | 'resolved';
  text: string; // Telegram HTML
}

interface Announced {
  since: number;
  pendingResolveAt: number | null;
  text: string; // filled Persian message, reused for the resolved line
  severity: string;
}

export interface NotifyContext {
  boardUrl: string;
  silencedUntil: (alertId: string, instanceKey: string) => number;
  rootCauses: RootCause[];
}

export class NotifyPlanner {
  private announced = new Map<string, Announced>();
  /** Keys the event log says were firing when the hub stopped → until when they count as already announced. */
  private carried = new Map<string, number>();

  /** Alerts already firing (from the event log) before this hub process started. */
  carryOver(keys: { key: string; since: number }[], now: number) {
    for (const k of keys) this.carried.set(k.key, now + RESTART_GRACE_MS);
  }

  onTransitions(ts: AlertTransition[], ctx: NotifyContext): OutMessage[] {
    const out: OutMessage[] = [];
    for (const t of ts) {
      const key = `${t.alert.id}|${t.instance.key}`;
      const fa = fill(t.alert.message_fa, t.instance.name, t.instance.key);
      if (t.fired) {
        const prev = this.announced.get(key);
        if (prev && prev.pendingResolveAt !== null) {
          // Flapped back within the grace period: nothing changed for the reader.
          prev.pendingResolveAt = null;
          continue;
        }
        if (prev) continue;
        const carriedUntil = this.carried.get(key);
        this.carried.delete(key);
        if (carriedUntil !== undefined && t.at <= carriedUntil) {
          this.announced.set(key, { since: t.at, pendingResolveAt: null, text: fa, severity: t.alert.severity });
          continue;
        }
        if (ctx.silencedUntil(t.alert.id, t.instance.key) > t.at) continue;
        this.announced.set(key, { since: t.at, pendingResolveAt: null, text: fa, severity: t.alert.severity });
        out.push({ key, kind: 'fired', text: firedText(t, fa, ctx) });
      } else {
        const prev = this.announced.get(key);
        if (!prev) continue;
        prev.pendingResolveAt = t.at + RESOLVE_GRACE_MS;
      }
    }
    return out;
  }

  /** "Resolved" messages whose grace period is over. Call every tick. */
  due(now: number, boardUrl: string): OutMessage[] {
    const out: OutMessage[] = [];
    for (const [key, a] of this.announced) {
      if (a.pendingResolveAt === null || now < a.pendingResolveAt) continue;
      this.announced.delete(key);
      const resolvedAt = a.pendingResolveAt - RESOLVE_GRACE_MS;
      out.push({
        key,
        kind: 'resolved',
        text: [`✅ <b>برطرف شد</b>`, esc(a.text), `مدت: ${duration(resolvedAt - a.since)} · ${tehranTime(resolvedAt)}`, link(boardUrl)].filter(Boolean).join('\n'),
      });
    }
    for (const [k, until] of this.carried) if (now > until) this.carried.delete(k);
    return out;
  }
}

function firedText(t: AlertTransition, fa: string, ctx: NotifyContext): string {
  const head = t.alert.severity === 'critical' ? '🔴 <b>هشدار بحرانی</b>' : t.alert.severity === 'warning' ? '🟠 <b>هشدار</b>' : 'ℹ️ <b>اطلاع</b>';
  const lines = [head, esc(fa)];
  // The board's root cause only belongs next to a critical alert; a warning
  // (TLS expiry, a reboot) is usually unrelated to the current incident.
  const rc = t.alert.severity === 'critical' ? ctx.rootCauses[0] : undefined;
  if (rc) {
    const users = rc.usersAffected > 0 ? ` · ${faNum(rc.usersAffected)} کاربر` : '';
    lines.push(`علت اصلی: ${esc(rc.title)} — ${esc(rc.summary)}${users}`);
  }
  lines.push(`<code>${esc(t.alert.id)}${t.instance.key ? ' · ' + esc(t.instance.key) : ''}</code> · ${tehranTime(t.at)}`);
  lines.push(link(ctx.boardUrl));
  return lines.filter(Boolean).join('\n');
}

export function fill(msg: string, name: string, id: string): string {
  return msg.replace(/\{name\}/g, name).replace(/\{id\}/g, id);
}

/** Escape for Telegram's HTML parse mode (only these three are special). */
export function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function link(url: string): string {
  return url ? `<a href="${esc(url)}">نقشه</a>` : '';
}

/** Persian digits, like the message_fa texts in the component files. */
export function faNum(n: number): string {
  return n.toLocaleString('fa-IR');
}

export function duration(ms: number): string {
  const m = Math.max(0, Math.round(ms / 60_000));
  if (m < 1) return 'کمتر از یک دقیقه';
  if (m < 60) return `${faNum(m)} دقیقه`;
  const h = Math.floor(m / 60);
  const r = m % 60;
  return r ? `${faNum(h)} ساعت و ${faNum(r)} دقیقه` : `${faNum(h)} ساعت`;
}

/** "۱۴ آبان ساعت ۱۲:۲۸" (Jalali, Tehran time). */
export function tehranTime(at: number): string {
  try {
    return new Intl.DateTimeFormat('fa-IR', { timeZone: 'Asia/Tehran', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(at));
  } catch {
    return new Date(at).toISOString().slice(0, 16).replace('T', ' ') + ' UTC';
  }
}
