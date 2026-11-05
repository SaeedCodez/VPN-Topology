import { createHash, randomBytes, randomUUID } from 'crypto';
import * as fs from 'fs';
import * as path from 'path';
import { Bundle, Health, OpsPlan, OpsPlanInput, OpsSettingsView, OpsStep, OpsStepInput, OpsStepLog, OpsTaskView } from '../contract';
import { HubService } from '../hub.service';
import { typeByName } from './catalog';
import { ExecEnv, runPlan, StepResult } from './executor';
import { planHash, policyFor, validatePlan, validateStep } from './plan';
import { remnawave, RwClient } from './remnawave';
import { applyOpsPatch, grantCommands, OPS_DEFAULTS, OPS_KEY, OpsPatch, opsKeyOpenSsh, opsPrivateKey, serverReachable, serverView, StoredOps } from './settings';
import { OPS_USER, sshSend, SshSend } from './ssh';
import { approvalMessages, changelogLine, closedText, confirmCutText, resultText, runningText, statusFa } from './text';
import { isOpsTool, OPS_TOOLS, OpsToolEnv, runOpsTool } from './tools';

export const APPROVE_WINDOW_MS = 10 * 60_000;

/**
 * Ops actions: settings from the board, the model's read tool and plan
 * proposals, the Telegram approval, and the deterministic executor. Nothing
 * here builds a shell command: server actions are request lines checked
 * against the catalog (here and again by the runner on the server), and
 * Remnawave actions are fixed API calls.
 *
 * In demo mode every ops action is refused (the demo hub has no auth).
 */
export class OpsService {
  private settings: StoredOps = { ...OPS_DEFAULTS, servers: {} };
  private running: string | null = null;
  private stopFlag = new Set<string>();

  constructor(
    private readonly hub: HubService,
    private readonly ssh: SshSend = sshSend,
    private readonly fetchFn: typeof fetch = fetch,
    private readonly opts: { settleMs?: number; now?: () => number } = {},
  ) {}

  private now(): number {
    return this.opts.now ? this.opts.now() : Date.now();
  }

  private get demo(): boolean {
    return this.hub.cfg.mode === 'demo';
  }

  private assertLive() {
    if (this.demo) throw new Error('ops actions are disabled in demo mode (the demo hub has no login)');
  }

  private rw(): RwClient {
    return remnawave(this.hub.cfg, this.fetchFn);
  }

  private bundle(): Bundle {
    return this.hub.bundle;
  }

  async init() {
    const stored = (await this.hub.db.getSetting<StoredOps>(OPS_KEY)) ?? null;
    this.settings = { ...OPS_DEFAULTS, ...(stored ?? {}), servers: { ...((stored?.servers as any) ?? {}) } };
    if (Array.isArray((stored as any)?.servers)) this.settings.servers = {}; // old registry format
  }

  private persist() {
    return this.hub.db.putSetting(OPS_KEY, this.settings);
  }

  private event(title: string, detail: string, severity: 'info' | 'warning' = 'info', taskId?: string) {
    this.hub.addEvent({ ts: new Date(this.now()).toISOString(), kind: 'ops', severity, entityKind: null, entityId: null, title, detail, data: { ops: true, ...(taskId ? { taskId } : {}) } });
  }

  // ---------------------------------------------------------------- settings

  view(): OpsSettingsView {
    const b = this.bundle();
    return {
      enabled: this.settings.enabled,
      actionModel: this.settings.actionModel,
      approverUserIds: this.settings.approverUserIds,
      sshPublicKey: opsKeyOpenSsh(this.settings),
      servers: (b.ops ?? []).filter((p) => p.runner).map((p) => serverView(this.settings, b, p.node, false, this.now())),
      remnawave: (b.ops ?? []).filter((p) => p.remnawave).map((p) => ({ node: p.node, name: p.remnawave!.name, actions: p.remnawave!.actions })),
      running: this.running,
      demo: this.demo,
    };
  }

  async update(p: OpsPatch, by: string): Promise<OpsSettingsView> {
    // The kill switch always works, even in demo mode.
    if (this.demo && !(p && Object.keys(p).length === 1 && p.enabled === false)) this.assertLive();
    const { next, changes } = applyOpsPatch(this.settings, p ?? {}, this.hub.cfg.sessionSecret, this.bundle(), this.now());
    if (changes.length) {
      this.settings = next;
      await this.persist();
      this.event(`Ops: ${changes.join(', ')}`, `by ${by}`);
    }
    return this.view();
  }

  get enabled(): boolean {
    return this.settings.enabled && !this.demo;
  }

  get actionModel(): string {
    return this.settings.actionModel;
  }

  get approverUserIds(): string[] {
    return this.settings.approverUserIds;
  }

  isApprover(userId: string): boolean {
    return this.settings.approverUserIds.includes(String(userId));
  }

  /** The asker gets the ops tools: an approver, ops on, a model and a key. */
  activeFor(userId: string): boolean {
    return this.enabled && !!this.settings.actionModel && this.isApprover(userId) && !!opsPrivateKey(this.settings, this.hub.cfg.sessionSecret);
  }

  stopCurrent(): string {
    if (!this.running) return 'کاری در حال اجرا نیست.';
    this.stopFlag.add(this.running);
    return 'درخواست توقف ثبت شد؛ پیش از مرحله‌ی بعد متوقف می‌شود.';
  }

  // ---------------------------------------------------------------- model tools

  agentRuntime(requestedBy: string, chatId: string) {
    const env: OpsToolEnv = {
      bundle: () => this.bundle(),
      read: (s) => this.read(s),
      propose: (plan) => this.propose(plan, requestedBy, chatId),
    };
    return { defs: OPS_TOOLS, isOpsTool, run: (name: string, args: string) => runOpsTool(name, args, env) };
  }

  /** One catalog read (no approval). */
  async read(input: OpsStepInput): Promise<StepResult> {
    this.assertLive();
    if (!this.settings.enabled) return { ok: false, output: '', error: 'ops is switched off' };
    const step = validateStep(this.bundle(), input, 'read');
    return this.execute(step);
  }

  // ---------------------------------------------------------------- running one action

  private async execute(step: OpsStep): Promise<StepResult> {
    if (step.where === 'remnawave') return this.executeRemnawave(step);
    const p = policyFor(this.bundle(), step.node);
    if (!p?.runner || !p.config_hash) return { ok: false, output: '', error: `${step.node} has no runner` };
    if (!serverReachable(this.settings, step.node, this.now())) return { ok: false, output: '', error: `access to ${step.node} is off (Ops panel)` };
    const t = typeByName(step.type)!;
    if (t.kind === 'change') {
      // Never change a server whose runner config differs from the map.
      const ping = await this.send(step.node, 'ping');
      if (!ping.ok) return ping;
      const srv = this.settings.servers[step.node];
      if (srv?.reportedConfig !== p.config_hash) {
        return { ok: false, output: '', error: `runner config on ${step.node} is ${srv?.reportedConfig ?? 'unknown'}, the map expects ${p.config_hash}: install the new ops.yaml first` };
      }
      const left = await this.cooldownLeft(step);
      if (left > 0) return { ok: false, output: '', error: `cooldown: ${step.line} ran recently; wait ${Math.ceil(left / 60)} min` };
    }
    return this.send(step.node, step.line);
  }

  private async send(node: string, line: string): Promise<StepResult> {
    const p = policyFor(this.bundle(), node)!;
    const srv = this.settings.servers[node] ?? { access: 'off' as const };
    const key = opsPrivateKey(this.settings, this.hub.cfg.sessionSecret);
    if (!key) return { ok: false, output: '', error: 'no SSH key (switch ops on in the board)' };
    const host = srv.host || p.address || '';
    if (!host) return { ok: false, output: '', error: `no SSH host for ${node}` };
    const req = randomBytes(8).toString('hex');
    const r = await this.ssh({ host, port: srv.port ?? 22, user: OPS_USER, hostKey: srv.hostKey ?? '' }, key, `${line} req=${req}`, { timeoutMs: 180_000 });
    const state = (this.settings.servers[node] ??= { access: 'off' });
    if (!r.answered || !r.response) {
      state.lastError = r.hostKeyFp && r.hostKeyFp !== srv.hostKey ? `host key mismatch: server shows ${r.hostKeyFp}` : r.error ?? 'no answer';
      await this.persist();
      return { ok: false, output: '', error: state.lastError };
    }
    const resp = r.response;
    state.lastOkAt = this.now();
    state.lastError = resp.ok ? undefined : resp.error;
    if (resp.config_hash) state.reportedConfig = resp.config_hash;
    await this.persist();
    return { ok: resp.ok, output: resp.output ?? '', error: resp.ok ? undefined : resp.error ?? `exit ${resp.exit_code}` };
  }

  private async executeRemnawave(step: OpsStep): Promise<StepResult> {
    try {
      const rw = this.rw();
      if (step.type === 'rw.nodes') {
        const nodes = await rw.nodes();
        return { ok: true, output: nodes.map((n) => `${n.name}: connected=${n.isConnected} disabled=${n.isDisabled} xrayUptime=${n.xrayUptime ?? '?'} usersOnline=${n.usersOnline ?? '?'}`).join('\n') };
      }
      if (step.type === 'rw.system') return { ok: true, output: JSON.stringify(await rw.systemStats()) };
      const p = policyFor(this.bundle(), step.node);
      const verb = step.type.replace('rw.node-', '') as 'restart' | 'enable' | 'disable';
      if (!p?.remnawave?.actions.includes(step.type)) return { ok: false, output: '', error: `${step.type} is not allowed for ${step.node}` };
      const left = await this.cooldownLeft(step);
      if (left > 0) return { ok: false, output: '', error: `cooldown: ${step.line} ran recently; wait ${Math.ceil(left / 60)} min` };
      const r = await rw.nodeAction(p.remnawave.name, verb);
      return { ok: r.ok, output: r.detail };
    } catch (e: any) {
      return { ok: false, output: '', error: String(e?.message ?? e) };
    }
  }

  /** Seconds left of the step's cooldown, from the steps the hub ran (the runner checks too). */
  private async cooldownLeft(step: OpsStep): Promise<number> {
    if (!step.cooldownS) return 0;
    const since = this.now() - step.cooldownS * 1000;
    let last = 0;
    for (const t of await this.hub.db.opsTasksSince(this.now() - 24 * 3_600_000)) {
      for (const l of (t.log ?? []) as OpsStepLog[]) {
        if (l.node === step.node && l.line === step.line && (l.status === 'ok' || l.status === 'failed' || l.status === 'rolled-back')) {
          last = Math.max(last, Date.parse(l.at));
        }
      }
    }
    return last > since ? Math.ceil((last - since) / 1000) : 0;
  }

  // ---------------------------------------------------------------- plans and approval

  async propose(input: OpsPlanInput, requestedBy: string, chatId: string): Promise<{ taskId: string }> {
    this.assertLive();
    if (!this.settings.enabled) throw new Error('ops is switched off');
    const plan = validatePlan(this.bundle(), input);
    for (const s of plan.steps) {
      const left = await this.cooldownLeft(s);
      if (left > 0) throw new Error(`cooldown: ${s.node} ${s.line} ran recently; wait ${Math.ceil(left / 60)} min`);
      if (s.where === 'server' && !serverReachable(this.settings, s.node, this.now())) throw new Error(`access to ${s.node} is off; ask the owner to switch it on in the Ops panel`);
    }
    const id = randomUUID();
    const hash = planHash(plan);
    await this.hub.db.addOpsTask({ id, status: 'pending', plan, hash, requestedBy, chatId, createdAt: this.now(), log: [] });
    const messageIds: number[] = [];
    const tg = this.hub.telegram;
    if (tg) {
      const parts = approvalMessages(plan, requestedBy, id, this.now());
      for (let i = 0; i < parts.length; i++) {
        const last = i === parts.length - 1;
        const r = await tg.call('sendMessage', {
          chat_id: chatId,
          text: parts[i],
          parse_mode: 'HTML',
          link_preview_options: { is_disabled: true },
          ...(last ? { reply_markup: { inline_keyboard: [[{ text: '✅ انجام بده', callback_data: `ops:${id}:a` }, { text: '❌ لغو', callback_data: `ops:${id}:c` }]] } } : {}),
        });
        if (r.ok && typeof r.result?.message_id === 'number') messageIds.push(r.result.message_id);
      }
      await this.hub.db.updateOpsTask(id, { messageIds });
    }
    this.event(`Ops plan proposed: ${plan.title}`, `by ${requestedBy}: ${plan.steps.map((s) => `${s.node} ${s.line}`).join('; ')}`, 'info', id);
    return { taskId: id };
  }

  /** callback_data "ops:<uuid>:a|c|y". Returns the answerCallbackQuery text. */
  async handleCallback(data: string, fromId: string): Promise<string> {
    const m = /^ops:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}):(a|c|y)$/.exec(data);
    if (!m) return '';
    const [, id, action] = m;
    if (this.demo) return 'در حالت دمو غیرفعال است.';
    if (!this.isApprover(fromId)) return 'شما اجازه‌ی تأیید ندارید.';
    const task = await this.hub.db.opsTask(id);
    if (!task) return 'این درخواست پیدا نشد.';
    const plan = task.plan as OpsPlan;
    if (task.status !== 'pending' && task.status !== 'confirming') return `این درخواست ${statusFa(task.status)}.`;
    if (this.now() - task.createdAt > APPROVE_WINDOW_MS) {
      if (await this.hub.db.transitionOpsTask(id, task.status, 'expired', { outcome: 'بیش از ۱۰ دقیقه بدون تأیید ماند' })) {
        await this.editLast(task.chatId, task.messageIds, closedText(plan, 'expired', 'منقضی شد (بیش از ۱۰ دقیقه).'));
      }
      return 'منقضی شده بود.';
    }
    if (action === 'c') {
      if (!(await this.hub.db.transitionOpsTask(id, task.status, 'cancelled', { outcome: `لغو شد توسط ${fromId}` }))) return 'وضعیت عوض شده بود.';
      await this.editLast(task.chatId, task.messageIds, closedText(plan, 'cancelled', `لغو شد توسط ${fromId}.`));
      this.event(`Ops plan cancelled: ${plan.title}`, `by ${fromId}`, 'info', id);
      return 'لغو شد.';
    }
    if (!this.settings.enabled) return 'Ops خاموش است.';
    if (planHash(plan) !== task.hash) return 'پلن ذخیره‌شده تغییر کرده؛ اجرا نمی‌شود.';
    if (action === 'a' && task.status === 'pending' && plan.cutsUsers) {
      // Second tap from the same approver for plans that cut paying users.
      if (!(await this.hub.db.transitionOpsTask(id, 'pending', 'confirming', { approvedBy: fromId, hash: task.hash }))) return 'وضعیت عوض شده بود.';
      const tg = this.hub.telegram;
      if (tg && task.chatId) {
        const r = await tg.call('sendMessage', {
          chat_id: task.chatId,
          text: confirmCutText(plan),
          parse_mode: 'HTML',
          reply_markup: { inline_keyboard: [[{ text: '🔴 بله، قطع شود', callback_data: `ops:${id}:y` }, { text: '❌ لغو', callback_data: `ops:${id}:c` }]] },
        });
        if (r.ok && typeof r.result?.message_id === 'number') await this.hub.db.updateOpsTask(id, { messageIds: [...(task.messageIds ?? []), r.result.message_id] });
      }
      return 'تأیید دوم لازم است.';
    }
    const from = plan.cutsUsers ? 'confirming' : 'pending';
    if (action === 'y' && (task.status !== 'confirming' || task.approvedBy !== fromId)) return 'تأیید دوم باید از همان تأییدکننده باشد.';
    if (action === 'a' && plan.cutsUsers) return 'برای این پلن «بله، قطع شود» را بزنید.';
    if (this.running) return 'یک کار دیگر در حال اجراست؛ کمی بعد دوباره بزنید.';
    // Atomic: exactly one tap wins, and only for the hash that was shown.
    if (!(await this.hub.db.transitionOpsTask(id, from, 'approved', { approvedBy: fromId, hash: task.hash }))) return 'قبلاً تأیید یا بسته شده است.';
    this.running = id;
    void this.runTask(id, fromId, task.hash);
    return 'تأیید شد؛ در حال اجرا…';
  }

  private async runTask(id: string, approvedBy: string, hash: string) {
    const task = await this.hub.db.opsTask(id);
    if (!task) {
      this.running = null;
      return;
    }
    const plan = task.plan as OpsPlan;
    this.stopFlag.delete(id);
    try {
      if (!(await this.hub.db.transitionOpsTask(id, 'approved', 'running', { hash }))) return;
      await this.editLast(task.chatId, task.messageIds, runningText(plan, 'شروع شد…'));
      this.event(`Ops plan approved: ${plan.title}`, `by ${approvedBy}`, 'info', id);
      const log: OpsStepLog[] = [];
      const env: ExecEnv = {
        revalidate: (node, s) => validateStep(this.bundle(), { node, type: s.type, args: s.args }, typeByName(s.type)?.kind === 'read' ? 'read' : 'change'),
        run: (step) => this.execute(step),
        verify: (ids) => this.verifyStatuses(ids),
        settle: () => sleep(this.opts.settleMs ?? Math.min((this.hub.cfg.tickMs ?? 15_000) * 2, 90_000)),
        hashOk: async () => {
          const t = await this.hub.db.opsTask(id);
          return !!t && t.hash === hash && planHash(t.plan as OpsPlan) === hash;
        },
        onLog: async (l) => {
          log.push(l);
          await this.hub.db.updateOpsTask(id, { log });
          this.event(`Ops step ${l.index + 1} ${l.status}: ${plan.title}`, `${l.node} ${l.line}`, l.status === 'ok' || l.status === 'rolled-back' ? 'info' : 'warning', id);
        },
        onProgress: (text) => this.editLast(task.chatId, task.messageIds, runningText(plan, text)),
        stopped: () => this.stopFlag.has(id) || !this.settings.enabled,
        now: () => this.now(),
      };
      let outcome;
      try {
        outcome = await runPlan(plan, env);
      } catch (e: any) {
        outcome = { status: 'failed' as const, outcome: `خطای غیرمنتظره: ${e?.message ?? e}`, verify: [], log };
      }
      await this.hub.db.updateOpsTask(id, { log: outcome.log });
      await this.hub.db.transitionOpsTask(id, 'running', outcome.status, { outcome: outcome.outcome });
      await this.editLast(task.chatId, task.messageIds, resultText(plan, outcome, approvedBy, this.now()));
      this.event(`Ops plan ${outcome.status}: ${plan.title}`, outcome.outcome, outcome.status === 'done' ? 'info' : 'warning', id);
      const tg = this.hub.telegram;
      if (tg && task.chatId && outcome.status === 'done') {
        await tg.call('sendMessage', { chat_id: task.chatId, text: changelogLine(plan, approvedBy, this.now()), parse_mode: 'HTML', link_preview_options: { is_disabled: true } });
      }
    } finally {
      this.running = null;
      this.stopFlag.delete(id);
    }
  }

  /** Waits for the running task (tests). */
  async idle(timeoutMs = 10_000): Promise<void> {
    const end = Date.now() + timeoutMs;
    while (this.running && Date.now() < end) await sleep(10);
  }

  private async verifyStatuses(ids: string[]): Promise<Map<string, Health>> {
    const out = new Map<string, Health>();
    if (!ids.length) return out;
    const { snap } = await this.hub.snapshotAt(null);
    for (const ref of ids) {
      const [kind, id] = ref.split('.', 2) as [string, string];
      const st = kind === 'node' ? snap.nodes[id]?.status : kind === 'link' ? snap.links[id]?.status : kind === 'route' ? snap.routes[id]?.status : undefined;
      if (st) out.set(ref, st);
    }
    return out;
  }

  private async editLast(chatId: string | null | undefined, messageIds: number[] | undefined, text: string) {
    const tg = this.hub.telegram;
    const mid = messageIds?.[messageIds.length - 1];
    if (!tg || !chatId || !mid) return;
    await tg.call('editMessageText', { chat_id: chatId, message_id: mid, text, parse_mode: 'HTML', link_preview_options: { is_disabled: true } });
  }

  // ---------------------------------------------------------------- board helpers

  /** `ping` through the runner: checks key, host key and config hash. */
  async serverTest(node: string): Promise<{ ok: boolean; detail: string }> {
    this.assertLive();
    const p = policyFor(this.bundle(), node);
    if (!p?.runner) return { ok: false, detail: `${node} has no runner` };
    if (!this.settings.servers[node]?.hostKey) return { ok: false, detail: 'enter the host-key fingerprint first' };
    const r = await this.send(node, 'ping');
    if (!r.ok) return { ok: false, detail: r.error ?? 'failed' };
    const reported = this.settings.servers[node]?.reportedConfig;
    return { ok: reported === p.config_hash, detail: reported === p.config_hash ? `ok · config ${reported}` : `runner answers, but its config is ${reported}; the map expects ${p.config_hash}` };
  }

  grantFor(node: string): ReturnType<typeof grantCommands> | null {
    const p = policyFor(this.bundle(), node);
    const pub = opsKeyOpenSsh(this.settings);
    if (!p?.runner || !p.config || !pub) return null;
    const dist = this.hub.cfg.agentDistDir;
    const sha = (f: string) => {
      try {
        const s = fs.readFileSync(path.join(dist, f), 'utf8').trim();
        return /^[0-9a-f]{64}$/.test(s) ? s : null;
      } catch {
        return null;
      }
    };
    return grantCommands({
      hubUrl: this.hub.cfg.publicUrl,
      node,
      config: p.config,
      publicKey: pub,
      binarySha: { amd64: sha('vpnmap-ops-linux-amd64.sha256'), arm64: sha('vpnmap-ops-linux-arm64.sha256') },
      actions: (p.config.scripts ?? []).map((s) => ({ id: s.id, sha: sha(`ops-action-${s.id}.sha256`) })),
    });
  }

  async tasks(): Promise<OpsTaskView[]> {
    return (await this.hub.db.recentOpsTasks(30)).map((t) => taskView(t, this.now()));
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

export function taskView(t: { id: string; status: string; plan: any; requestedBy: string; approvedBy?: string | null; createdAt: number; finishedAt?: number | null; log?: any[]; outcome?: string | null }, now = Date.now()): OpsTaskView {
  const plan = t.plan as OpsPlan;
  const stale = (t.status === 'pending' || t.status === 'confirming') && now - t.createdAt > APPROVE_WINDOW_MS;
  return {
    id: t.id,
    status: (stale ? 'expired' : t.status) as OpsTaskView['status'],
    title: plan.title,
    why: plan.why,
    impact: plan.impact,
    steps: plan.steps,
    verify: plan.verify,
    cutsUsers: plan.cutsUsers,
    requestedBy: t.requestedBy,
    approvedBy: t.approvedBy ?? null,
    createdAt: new Date(t.createdAt).toISOString(),
    finishedAt: t.finishedAt ? new Date(t.finishedAt).toISOString() : null,
    log: (t.log as OpsStepLog[]) ?? [],
    outcome: t.outcome ?? null,
  };
}

/** For tests: the sha256 a grant would show. */
export function sha256(s: string): string {
  return createHash('sha256').update(s).digest('hex');
}
