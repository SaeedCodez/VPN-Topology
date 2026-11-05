import { randomUUID, createHash } from 'crypto';
import { Health, OpsPlan, OpsSettingsView, OpsStep, OpsStepLog, OpsTaskView } from '../contract';
import { HubService } from '../hub.service';
import { TelegramSender } from '../notify/telegram';
import { esc, tehranTime } from '../engine/notify';
import { open, seal } from '../secretbox';
import { Vault } from '../redact';
import { ExecEnv, runPlan, summariseStep } from './executor';
import { remnawave } from './remnawave';
import { sshExec } from './ssh';
import { isOpsTool, normalizePlan, OPS_TOOLS, OpsEnv, runOpsTool } from './tools';
import { applyOpsPatch, grantCommand, opsKeyOpenSsh, opsPrivateKey, OPS_DEFAULTS, OPS_KEY, OpsPatch, publicOpsView, recordHostKey, revokeCommand, serverReachable, serverView, StoredOps } from './settings';

const VAULT_PURPOSE = 'ops-task-vault';
const APPROVE_WINDOW_MS = 10 * 60_000;

/**
 * Orchestrates the ops agent: settings (from the board), building the ops
 * tools for an agent run, turning a proposed plan into a pending Telegram
 * approval, and — only after the owner taps Approve — running it with the
 * executor (one task at a time). The Telegram polling itself lives in the
 * assistant service (one getUpdates loop for the whole bot); it calls in here.
 */
export class OpsService {
  private settings: StoredOps = { ...OPS_DEFAULTS };
  private running: string | null = null;
  private stopFlag = new Set<string>();

  constructor(private readonly hub: HubService) {}

  async init() {
    this.settings = { ...OPS_DEFAULTS, ...((await this.hub.db.getSetting<StoredOps>(OPS_KEY)) ?? {}) };
    if (this.settings.enabled && !this.settings.hubIp) this.settings.hubIp = ''; // filled from the board
  }

  // ---------------------------------------------------------------- settings

  view(): OpsSettingsView {
    return publicOpsView(this.settings);
  }

  async update(p: OpsPatch, by: string): Promise<OpsSettingsView> {
    const { next, changes } = applyOpsPatch(this.settings, p ?? {}, this.hub.cfg.sessionSecret);
    if (changes.length) {
      await this.hub.db.putSetting(OPS_KEY, next);
      this.settings = next;
      this.hub.addEvent({ ts: new Date().toISOString(), kind: 'ops', severity: 'info', entityKind: null, entityId: null, title: `Ops: ${changes.join(', ')}`, detail: `by ${by}`, data: { ops: true } });
    }
    return this.view();
  }

  /** Who may use the ops agent (an approver, with ops enabled, a model and a usable key). */
  activeFor(userId: string): boolean {
    return this.settings.enabled && !!this.settings.actionModel && this.settings.approverUserIds.includes(String(userId)) && !!opsPrivateKey(this.settings, this.hub.cfg.sessionSecret);
  }

  isApprover(userId: string): boolean {
    return this.settings.approverUserIds.includes(String(userId));
  }

  /** Halt the running task after its current step (the /stop command or the kill switch). */
  stopCurrent(): string {
    if (!this.running) return 'کاری در حال اجرا نیست.';
    this.stopFlag.add(this.running);
    return 'درخواست توقف ثبت شد؛ بعد از مرحله‌ی جاری متوقف می‌شود.';
  }

  get actionModel(): string {
    return this.settings.actionModel;
  }

  get enabled(): boolean {
    return this.settings.enabled;
  }

  private persistSettings() {
    return this.hub.db.putSetting(OPS_KEY, this.settings);
  }

  // ---------------------------------------------------------------- agent run

  /** The ops tools for one agent run; the vault collects secrets seen in output. */
  agentRuntime(vault: Vault, requestedBy: string, chatId: string) {
    const env: OpsEnv = {
      settings: this.settings,
      privateKey: opsPrivateKey(this.settings, this.hub.cfg.sessionSecret),
      sshExec,
      rw: remnawave(this.hub.cfg),
      vault,
      validIds: this.validIds(),
      onHostKey: async (serverId, fp) => {
        const srv = this.settings.servers.find((s) => s.id === serverId);
        if (!srv) return { ok: false, changed: false };
        const r = recordHostKey(srv, fp);
        if (r.ok) {
          srv.lastOkAt = Date.now();
          await this.persistSettings();
        }
        return r;
      },
      createTask: (plan) => this.createTask(plan, vault, requestedBy, chatId),
    };
    return { defs: OPS_TOOLS, isOpsTool, run: (name: string, args: string) => runOpsTool(name, args, env) };
  }

  private validIds(): Set<string> {
    const b = this.hub.bundle;
    return new Set<string>([...b.nodes.map((n) => n.id), ...b.links.map((l) => l.id), ...b.routes.map((r) => r.id)]);
  }

  // ---------------------------------------------------------------- approval

  private async createTask(plan: OpsPlan, vault: Vault, requestedBy: string, chatId: string): Promise<{ taskId: string }> {
    const id = randomUUID();
    const hash = createHash('sha256').update(JSON.stringify(plan)).digest('hex').slice(0, 16);
    const sealedVault = vault.size ? seal(JSON.stringify(vault.entries()), this.hub.cfg.sessionSecret, VAULT_PURPOSE) : null;
    const tg = this.hub.telegram;
    let messageId: number | null = null;
    if (tg) {
      const r = await tg.call('sendMessage', {
        chat_id: chatId,
        text: approvalText(plan, requestedBy),
        parse_mode: 'HTML',
        link_preview_options: { is_disabled: true },
        reply_markup: { inline_keyboard: [[{ text: '✅ انجام بده', callback_data: `ops:${id}:a` }, { text: '❌ لغو', callback_data: `ops:${id}:c` }]] },
      });
      if (r.ok && typeof r.result?.message_id === 'number') messageId = r.result.message_id;
    }
    await this.hub.db.addOpsTask({ id, status: 'pending', plan, hash, vault: sealedVault, requestedBy, chatId, messageId, createdAt: Date.now(), log: [] });
    this.hub.addEvent({ ts: new Date().toISOString(), kind: 'ops', severity: 'info', entityKind: null, entityId: null, title: `Ops plan proposed: ${plan.title}`, detail: `by ${requestedBy}, ${plan.steps.length} step(s)`, data: { ops: true, taskId: id } });
    return { taskId: id };
  }

  /** callback_data "ops:<id>:a|c|stop". Returns a short answer for answerCallbackQuery. */
  async handleCallback(data: string, fromId: string, messageId?: number): Promise<string> {
    const m = /^ops:([0-9a-f-]+):(a|c|stop)$/.exec(data);
    if (!m) return '';
    const [, id, action] = m;
    if (!this.isApprover(fromId)) return 'شما اجازه‌ی تأیید ندارید.';
    const task = await this.hub.db.opsTask(id);
    if (!task) return 'این درخواست پیدا نشد.';
    if (action === 'stop') return this.stopCurrent();
    if (task.status !== 'pending') return `این درخواست قبلاً ${statusFa(task.status)}.`;
    if (Date.now() - task.createdAt > APPROVE_WINDOW_MS) {
      await this.hub.db.updateOpsTask(id, { status: 'expired', finishedAt: Date.now(), outcome: 'بیش از ۱۰ دقیقه بدون تأیید ماند' });
      await this.editTaskMessage(task.chatId, task.messageId, approvalDone(task.plan, 'expired', 'منقضی شد (بیش از ۱۰ دقیقه).'));
      return 'منقضی شده بود.';
    }
    if (action === 'c') {
      await this.hub.db.updateOpsTask(id, { status: 'cancelled', approvedBy: fromId, finishedAt: Date.now(), outcome: 'لغو شد' });
      await this.editTaskMessage(task.chatId, task.messageId, approvalDone(task.plan, 'cancelled', `لغو شد توسط ${esc(fromId)}.`));
      this.hub.addEvent({ ts: new Date().toISOString(), kind: 'ops', severity: 'info', entityKind: null, entityId: null, title: `Ops plan cancelled: ${task.plan.title}`, detail: `by ${fromId}`, data: { ops: true, taskId: id } });
      return 'لغو شد.';
    }
    // Approve.
    if (this.running) return 'یک کار دیگر در حال اجراست؛ کمی بعد دوباره تأیید کنید.';
    await this.hub.db.updateOpsTask(id, { status: 'approved', approvedBy: fromId });
    void this.runTask(id, fromId); // async; progress goes to Telegram
    return 'تأیید شد؛ در حال اجرا…';
  }

  // ---------------------------------------------------------------- executor

  private async runTask(id: string, approvedBy: string) {
    const task = await this.hub.db.opsTask(id);
    if (!task) return;
    const tg = this.hub.telegram;
    const plan: OpsPlan = normalizePlan(task.plan);
    const vault = task.vault ? new Vault(JSON.parse(open(task.vault, this.hub.cfg.sessionSecret, VAULT_PURPOSE) ?? '[]')) : new Vault();
    this.running = id;
    this.stopFlag.delete(id);
    await this.hub.db.updateOpsTask(id, { status: 'running' });
    await this.editTaskMessage(task.chatId, task.messageId, runningText(plan, 'شروع شد…'));
    this.hub.addEvent({ ts: new Date().toISOString(), kind: 'ops', severity: 'info', entityKind: null, entityId: null, title: `Ops plan approved: ${plan.title}`, detail: `by ${approvedBy}`, data: { ops: true, taskId: id } });

    const log: OpsStepLog[] = [];
    const env: ExecEnv = {
      settings: this.settings,
      privateKey: opsPrivateKey(this.settings, this.hub.cfg.sessionSecret),
      sshExec,
      rw: remnawave(this.hub.cfg),
      vault,
      verify: (ids) => this.verifyStatuses(ids),
      settle: () => sleep(Math.min((this.hub.cfg.tickMs ?? 15_000) * 2, 90_000)),
      onStep: async (l) => {
        log.push(l);
        await this.hub.db.updateOpsTask(id, { log });
        this.hub.addEvent({ ts: l.at, kind: 'ops', severity: l.status === 'ok' ? 'info' : 'warning', entityKind: null, entityId: null, title: `Ops step ${l.index + 1} ${l.status}: ${plan.title}`, detail: l.summary, data: { ops: true, taskId: id } });
      },
      onProgress: (text) => this.editTaskMessage(task.chatId, task.messageId, runningText(plan, text)),
      stopped: () => this.stopFlag.has(id) || !this.settings.enabled,
    };

    let outcome;
    try {
      outcome = await runPlan(plan, id, env);
    } catch (e: any) {
      outcome = { status: 'failed' as const, outcome: `خطای غیرمنتظره: ${e?.message ?? e}`, verify: [], log };
    }
    await this.hub.db.updateOpsTask(id, { status: outcome.status, finishedAt: Date.now(), outcome: outcome.outcome, log: outcome.log });
    await this.editTaskMessage(task.chatId, task.messageId, resultText(plan, outcome, approvedBy));
    this.hub.addEvent({ ts: new Date().toISOString(), kind: 'ops', severity: outcome.status === 'done' ? 'info' : 'warning', entityKind: null, entityId: null, title: `Ops plan ${outcome.status}: ${plan.title}`, detail: outcome.outcome, data: { ops: true, taskId: id } });
    this.running = null;
    this.stopFlag.delete(id);
    if (tg && outcome.status === 'done') {
      void tg.call('sendMessage', { chat_id: task.chatId, text: changelogLine(plan, approvedBy), parse_mode: 'HTML', link_preview_options: { is_disabled: true } });
    }
  }

  private async verifyStatuses(ids: string[]): Promise<Map<string, Health>> {
    const out = new Map<string, Health>();
    if (!ids.length) return out;
    const { snap } = await this.hub.snapshotAt(null);
    for (const id of ids) {
      const st = snap.nodes[id]?.status ?? snap.links[id]?.status ?? snap.routes[id]?.status;
      if (st) out.set(id, st);
    }
    return out;
  }

  private async editTaskMessage(chatId: string | null | undefined, messageId: number | null | undefined, text: string) {
    const tg = this.hub.telegram;
    if (!tg || !chatId || !messageId) return;
    await tg.call('editMessageText', { chat_id: chatId, message_id: messageId, text, parse_mode: 'HTML', link_preview_options: { is_disabled: true } });
  }

  // ---------------------------------------------------------------- board API helpers

  async serverTest(id: string): Promise<{ ok: boolean; detail: string }> {
    const srv = this.settings.servers.find((s) => s.id === id);
    if (!srv) return { ok: false, detail: 'no such server' };
    const key = opsPrivateKey(this.settings, this.hub.cfg.sessionSecret);
    if (!key) return { ok: false, detail: 'no SSH key' };
    const r = await sshExec({ host: srv.host, port: srv.port, user: srv.user }, key, 'id && sudo -n true && echo OK', { timeoutMs: 20_000, expectHostKey: srv.hostKey });
    if (r.hostKeyFp) {
      const hk = recordHostKey(srv, r.hostKeyFp);
      if (!hk.ok) return { ok: false, detail: `host key changed (${r.hostKeyFp})` };
      if (r.ok) srv.lastOkAt = Date.now();
      await this.persistSettings();
    }
    return { ok: r.ok, detail: r.ok ? `ok (${r.hostKeyFp})` : r.error ?? r.output.slice(0, 200) };
  }

  grantFor(id: string): { grant: string; revoke: string } | null {
    const srv = this.settings.servers.find((s) => s.id === id);
    const pub = opsKeyOpenSsh(this.settings);
    if (!srv || !pub) return null;
    return { grant: grantCommand(srv.user, pub, this.settings.hubIp), revoke: revokeCommand(srv.user) };
  }

  async tasks(): Promise<OpsTaskView[]> {
    const rows = await this.hub.db.recentOpsTasks(30);
    return rows.map((t) => taskView(t));
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

// ---------------------------------------------------------------- Telegram text

function stepLines(plan: OpsPlan): string {
  return plan.steps
    .map((s, i) => {
      const rb = s.rollback === 'restore' ? 'بازگردانی: از نسخه‌ی پشتیبان' : s.rollback ? 'بازگردانی: دارد' : '⚠️ بدون بازگردانی';
      return `${faNum(i + 1)}. <code>${esc(summariseStep(s))}</code>\n   ${s.purpose ? esc(s.purpose) + ' · ' : ''}${rb}`;
    })
    .join('\n');
}

function approvalText(plan: OpsPlan, requestedBy: string): string {
  return [
    '🛠 <b>درخواست اجرا روی سرور</b>',
    esc(plan.title),
    plan.why ? `چرا: ${esc(plan.why)}` : '',
    plan.impact ? `اثر: ${esc(plan.impact)}` : '',
    '',
    stepLines(plan),
    '',
    plan.verify.length ? `بررسی سلامت: ${esc(plan.verify.join('، '))}` : 'بدون بررسی خودکار سلامت',
    `درخواست از <code>${esc(requestedBy)}</code> · ${tehranTime(Date.now())}`,
    'برای اجرا «✅ انجام بده» را بزنید. هر مرحله بعد از اجرا بررسی می‌شود و اگر چیزی بدتر شود، خودکار برگردانده می‌شود.',
  ]
    .filter(Boolean)
    .join('\n');
}

function runningText(plan: OpsPlan, status: string): string {
  return ['🛠 <b>در حال اجرا</b>', esc(plan.title), '', esc(status)].join('\n');
}

function resultText(plan: OpsPlan, outcome: { status: string; outcome: string; verify: { id: string; before: Health; after: Health }[] }, by: string): string {
  const icon = outcome.status === 'done' ? '✅' : outcome.status === 'rolled-back' ? '↩️' : outcome.status === 'cancelled' ? '✖️' : '⚠️';
  const v = outcome.verify.length ? '\nسلامت: ' + outcome.verify.map((x) => `${x.id} ${x.before}→${x.after}`).join('، ') : '';
  return [`${icon} <b>${statusFa(outcome.status)}</b>`, esc(plan.title), '', esc(outcome.outcome) + v, `${tehranTime(Date.now())} · تأیید: <code>${esc(by)}</code>`].join('\n');
}

function approvalDone(plan: OpsPlan, status: string, note: string): string {
  return [`<b>${statusFa(status)}</b>`, esc(plan.title), esc(note)].join('\n');
}

function changelogLine(plan: OpsPlan, by: string): string {
  const d = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Tehran' }).format(new Date());
  return ['📝 <b>برای Changelog نوشن:</b>', `<code>${esc(d)} — ${esc(plan.title)} (ops agent, تأیید ${esc(by)})</code>`].join('\n');
}

function statusFa(s: string): string {
  return { pending: 'در انتظار تأیید', approved: 'تأییدشده', running: 'در حال اجرا', done: 'انجام شد', failed: 'ناموفق', 'rolled-back': 'برگردانده شد', cancelled: 'لغو شد', expired: 'منقضی شد' }[s] ?? s;
}

function faNum(n: number): string {
  return n.toLocaleString('fa-IR');
}

export function taskView(t: { id: string; status: string; plan: any; requestedBy: string; approvedBy?: string | null; createdAt: number; finishedAt?: number | null; log?: any[]; outcome?: string | null }): OpsTaskView {
  const plan: OpsPlan = normalizePlan(t.plan);
  return {
    id: t.id,
    status: t.status as any,
    title: plan.title,
    why: plan.why,
    impact: plan.impact,
    steps: plan.steps.map((s: OpsStep) => ({ target: s.target, summary: summariseStep(s), rollback: s.rollback === 'restore' ? 'restore' : s.rollback ? 'yes' : 'none' })),
    verify: plan.verify,
    requestedBy: t.requestedBy,
    approvedBy: t.approvedBy ?? null,
    createdAt: new Date(t.createdAt).toISOString(),
    finishedAt: t.finishedAt ? new Date(t.finishedAt).toISOString() : null,
    log: (t.log as OpsStepLog[]) ?? [],
    outcome: t.outcome ?? null,
  };
}

export { serverView };
