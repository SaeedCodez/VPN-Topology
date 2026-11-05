import { OpsPlan, OpsStep } from '../contract';
import { esc, tehranTime } from '../engine/notify';

/**
 * Telegram texts for ops plans (HTML). The approval shows every step with
 * every parameter, its rollback, what is read first and the verify ids,
 * never truncated: long plans are split over several messages, and the
 * buttons go on the last one.
 */

export const TG_LIMIT = 3800;

export function faNum(n: number): string {
  return n.toLocaleString('fa-IR');
}

export function statusFa(s: string): string {
  return (
    {
      pending: 'در انتظار تأیید',
      confirming: 'در انتظار تأیید دوم',
      approved: 'تأییدشده',
      running: 'در حال اجرا',
      done: 'انجام شد',
      failed: 'ناموفق',
      'rolled-back': 'برگردانده شد',
      cancelled: 'لغو شد',
      expired: 'منقضی شد',
    } as Record<string, string>
  )[s] ?? s;
}

function minutes(s: number): string {
  return s >= 60 ? `${faNum(Math.round(s / 60))} دقیقه` : `${faNum(s)} ثانیه`;
}

export function stepBlock(s: OpsStep, i: number): string {
  const lines = [
    `<b>${faNum(i + 1)}. ${esc(s.title)}</b> · ${esc(s.node)}`,
    `<code>${esc(s.line)}</code>`,
  ];
  if (s.cutsUsers) lines.push(`🔴 کاربران پولی قطع می‌شوند (مسیر ${esc(s.cutRoutes.join('، '))})`);
  if (s.readBefore) lines.push(`پیش از اجرا خوانده می‌شود: <code>${esc(lineOf(s.readBefore))}</code>`);
  lines.push(s.rollback ? `بازگشت: <code>${esc(lineOf(s.rollback))}</code>` : 'بازگشت: ندارد (فقط ری‌استارت/اجرا؛ چیزی برای برگرداندن نیست)');
  lines.push(`بررسی سلامت: ${esc(s.verify.join(' '))}`);
  if (s.cooldownS) lines.push(`فاصله‌ی مجاز تکرار: ${minutes(s.cooldownS)}`);
  return lines.join('\n');
}

export function lineOf(c: { type: string; args: Record<string, string> }): string {
  return [c.type, ...Object.keys(c.args).sort().map((k) => `${k}=${c.args[k]}`)].join(' ');
}

/** The approval request, as one or more messages (buttons go on the last). */
export function approvalMessages(plan: OpsPlan, requestedBy: string, taskId: string, now = Date.now()): string[] {
  const head = [
    plan.cutsUsers ? '🛠🔴 <b>درخواست تغییر روی سرور — کاربران پولی قطع می‌شوند</b>' : '🛠 <b>درخواست تغییر روی سرور</b>',
    `<b>${esc(plan.title)}</b>`,
    `چرا: ${esc(plan.why)}`,
    `اثر: ${esc(plan.impact)}`,
    `شناسه: <code>${esc(taskId.slice(0, 8))}</code> · درخواست از <code>${esc(requestedBy)}</code> · ${tehranTime(now)}`,
  ].join('\n');
  const tail = [
    `اگر بعد از هر مرحله یکی از این‌ها بدتر شود، مرحله‌های قابل بازگشت خودکار برگردانده می‌شوند: ${esc(plan.verify.join(' '))}`,
    plan.cutsUsers ? 'بعد از «✅ انجام بده» یک تأیید دوم هم لازم است.' : '',
    'این درخواست ۱۰ دقیقه معتبر است. /stop اجرا را پیش از مرحله‌ی بعد متوقف می‌کند.',
  ]
    .filter(Boolean)
    .join('\n');
  const blocks = [head, ...plan.steps.map(stepBlock), tail];
  const out: string[] = [];
  let cur = '';
  for (const b of blocks) {
    if (cur && cur.length + b.length + 2 > TG_LIMIT) {
      out.push(cur);
      cur = '';
    }
    // A single block is never longer than the limit (text fields are capped), but never cut one.
    cur = cur ? `${cur}\n\n${b}` : b;
  }
  if (cur) out.push(cur);
  if (out.length > 1) return out.map((m, i) => `${m}\n\n(${faNum(i + 1)}/${faNum(out.length)})`);
  return out;
}

export function confirmCutText(plan: OpsPlan): string {
  const routes = [...new Set(plan.steps.flatMap((s) => s.cutRoutes))];
  return [
    '🔴 <b>تأیید دوم</b>',
    `این پلن کاربران پولی مسیر ${esc(routes.join('، '))} را قطع می‌کند:`,
    ...plan.steps.filter((s) => s.cutsUsers).map((s) => `- <code>${esc(s.node)} ${esc(s.line)}</code>`),
    'مطمئنید؟',
  ].join('\n');
}

export function runningText(plan: OpsPlan, status: string): string {
  return ['🛠 <b>در حال اجرا</b>', esc(plan.title), '', esc(status)].join('\n');
}

export function resultText(plan: OpsPlan, outcome: { status: string; outcome: string; verify: { id: string; before: string; after: string }[] }, by: string, now = Date.now()): string {
  const icon = outcome.status === 'done' ? '✅' : outcome.status === 'rolled-back' ? '↩️' : outcome.status === 'cancelled' ? '✖️' : '⚠️';
  const v = outcome.verify.length ? '\nسلامت: ' + outcome.verify.map((x) => `${esc(x.id)} ${x.before}→${x.after}`).join('، ') : '';
  return [`${icon} <b>${statusFa(outcome.status)}</b>`, esc(plan.title), '', esc(outcome.outcome) + v, `${tehranTime(now)} · تأیید: <code>${esc(by)}</code>`].join('\n');
}

export function closedText(plan: OpsPlan, status: string, note: string): string {
  return [`<b>${statusFa(status)}</b>`, esc(plan.title), esc(note)].join('\n');
}

export function changelogLine(plan: OpsPlan, by: string, now = Date.now()): string {
  const d = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Tehran' }).format(new Date(now));
  return ['📝 <b>برای Changelog:</b>', `<code>${esc(d)} — ${esc(plan.title)}: ${esc(plan.steps.map((s) => `${s.node} ${s.line}`).join('; '))} (ops, تأیید ${esc(by)})</code>`].join('\n');
}
