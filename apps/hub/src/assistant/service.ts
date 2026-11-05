import { AssistantModel, AssistantSettingsView, AssistantTestResult } from '../contract';
import { HubService } from '../hub.service';
import { TelegramSender } from '../notify/telegram';
import { OpsService } from '../ops/service';
import { Vault } from '../redact';
import { parseAt, parseDuration, Queries } from '../queries';
import { answer, OpsRuntime, splitMessage } from './agent';
import { ChatFn, listModels, openRouterChat } from './openrouter';
import { AssistantPatch, applyPatch, decrypt, DEFAULTS, publicView, SETTINGS_KEY, StoredAssistant } from './settings';
import { ReadApi, ToolContext } from './tools';
import { decide, TgCallback, TgMessage } from './trigger';

const OFFSET_KEY = 'telegram_offset';
const MAX_PARALLEL = 2;
/** Questions older than this when the hub sees them (hub was down) are skipped. */
const STALE_MS = 10 * 60_000;

/**
 * The Telegram assistant: settings (from the board), the getUpdates loop
 * (through TELEGRAM_API_ROOT, i.e. the Cloudflare Worker), and one agent run
 * per question. Polling runs only in live mode, with Telegram configured and
 * the assistant enabled with a key and a model. It only reads (see tools.ts).
 */
export class AssistantService {
  private settings: StoredAssistant = { ...DEFAULTS };
  private readonly queries: Queries;
  private polling = false;
  private stopRequested = false;
  private lastPollAt: number | null = null;
  private pollError: string | null = null;
  private offset = 0;
  private inFlight = 0;
  private usage = { day: '', questions: 0, promptTokens: 0, completionTokens: 0, costUsd: 0 };
  private lastError: { at: string; error: string } | null = null;
  private lastIgnored: { at: string; fromId: string; fromName: string } | null = null;
  private modelsCache: { at: number; list: AssistantModel[] } | null = null;

  constructor(
    private readonly hub: HubService,
    private readonly telegram: TelegramSender | null,
    private readonly ops: OpsService,
    private readonly chatFactory: (key: string, model: string) => ChatFn = (key, model) => openRouterChat({ key, model, publicUrl: hub.cfg.publicUrl }),
    private readonly fetchFn: typeof fetch = fetch,
  ) {
    this.queries = new Queries(hub);
  }

  async init() {
    this.settings = { ...DEFAULTS, ...((await this.hub.db.getSetting<StoredAssistant>(SETTINGS_KEY)) ?? {}) };
    this.offset = (await this.hub.db.getSetting<number>(OFFSET_KEY)) ?? 0;
    this.reconcile();
  }

  stop() {
    this.stopRequested = true;
  }

  /** Re-evaluate whether to poll (called when the ops settings change). */
  refresh() {
    this.reconcile();
  }

  // ------------------------------------------------------------------ settings

  view(): AssistantSettingsView {
    const tg = this.hub.telegramStatus();
    this.rollDay();
    return {
      ...publicView(this.settings, this.hub.cfg.sessionSecret),
      telegram: { enabled: tg.enabled, reason: tg.enabled ? undefined : tg.reason },
      polling: { running: this.polling, lastPollAt: this.lastPollAt ? new Date(this.lastPollAt).toISOString() : null, error: this.pollError },
      usage: { ...this.usage, costUsd: Math.round(this.usage.costUsd * 10000) / 10000 },
      lastError: this.lastError,
      lastIgnored: this.lastIgnored,
    };
  }

  async update(p: AssistantPatch, by: string): Promise<AssistantSettingsView> {
    const { next, changes } = applyPatch(this.settings, p ?? {}, this.hub.cfg.sessionSecret);
    if (changes.length) {
      await this.hub.db.putSetting(SETTINGS_KEY, next);
      this.settings = next;
      this.hub.addEvent({ ts: new Date().toISOString(), kind: 'topology', severity: 'info', entityKind: null, entityId: null, title: `Assistant: ${changes.join(', ')}`, detail: `by ${by}`, data: { assistant: true } });
      this.reconcile();
    }
    return this.view();
  }

  async models(): Promise<AssistantModel[]> {
    if (this.modelsCache && Date.now() - this.modelsCache.at < 3_600_000) return this.modelsCache.list;
    const list = await listModels(this.fetchFn);
    this.modelsCache = { at: Date.now(), list };
    return list;
  }

  /** One tiny completion with the saved key and model. */
  async test(): Promise<AssistantTestResult> {
    const key = this.key();
    const model = this.settings.model;
    if (!key) return { ok: false, model, ms: 0, error: this.settings.key ? 'the stored key cannot be read (SESSION_SECRET changed): enter it again' : 'no OpenRouter key' };
    if (!model) return { ok: false, model, ms: 0, error: 'no model' };
    const t0 = Date.now();
    try {
      const r = await this.chatFactory(key, model)([{ role: 'user', content: 'Reply with exactly: OK' }], []);
      return { ok: true, model, ms: Date.now() - t0, reply: (r.content ?? '').slice(0, 200), costUsd: r.usage.costUsd };
    } catch (e: any) {
      return { ok: false, model, ms: Date.now() - t0, error: e?.message ?? String(e) };
    }
  }

  private key(): string | null {
    return this.settings.key ? decrypt(this.settings.key, this.hub.cfg.sessionSecret) : null;
  }

  private ready(): boolean {
    if (!this.telegram || this.hub.cfg.mode !== 'live') return false;
    const assistantReady = this.settings.enabled && !!this.settings.model && !!this.key();
    const opsReady = this.ops.enabled && !!this.key() && !!this.ops.actionModel;
    return assistantReady || opsReady;
  }

  /** Start or stop polling to match the settings. */
  private reconcile() {
    if (!this.ready()) this.stopRequested = true;
    else if (this.polling) this.stopRequested = false; // re-enabled before the running loop noticed
    else void this.pollLoop();
  }

  // ------------------------------------------------------------------ polling

  private async pollLoop() {
    const tg = this.telegram!;
    this.polling = true;
    this.stopRequested = false;
    let backoff = 5_000;
    console.log('assistant: listening for Telegram questions');
    try {
      while (!this.stopRequested) {
        const r = await tg.call('getUpdates', { offset: this.offset, timeout: 25, allowed_updates: ['message', 'callback_query'] }, 40_000);
        this.lastPollAt = Date.now();
        if (!r.ok) {
          this.pollError = r.status === 409 ? 'another getUpdates client or a webhook is using this bot (409): remove the webhook (deleteWebhook) or stop the other client' : r.error ?? 'error';
          await sleep(backoff);
          backoff = Math.min(backoff * 2, 120_000);
          continue;
        }
        this.pollError = null;
        backoff = 5_000;
        for (const u of (r.result ?? []) as { update_id: number; message?: TgMessage; callback_query?: TgCallback }[]) {
          this.offset = u.update_id + 1;
          if (u.message) void this.handle(u.message);
          else if (u.callback_query) void this.handleCallback(u.callback_query);
        }
        if (r.result?.length) await this.hub.db.putSetting(OFFSET_KEY, this.offset).catch(() => undefined);
      }
    } finally {
      this.polling = false;
      console.log('assistant: stopped listening');
    }
  }

  // ------------------------------------------------------------------ one question

  /** An inline-button tap (ops plan approve/cancel/stop). Only the configured chat. */
  private async handleCallback(cb: TgCallback) {
    const tg = this.telegram;
    if (!tg || !cb.data || String(cb.message?.chat?.id ?? '') !== String(tg.chatId)) return;
    const fromId = String(cb.from?.id ?? '');
    let text = '';
    try {
      text = await this.ops.handleCallback(cb.data, fromId, cb.message?.message_id);
    } catch (e: any) {
      text = `خطا: ${e?.message ?? e}`.slice(0, 180);
    }
    await tg.call('answerCallbackQuery', { callback_query_id: cb.id, ...(text ? { text, show_alert: false } : {}) });
  }

  async handle(m: TgMessage) {
    const tg = this.telegram;
    if (!tg) return;
    // An approver can stop a running ops task with /stop.
    if (/^\/stop(?:@\w+)?\s*$/i.test((m.text ?? '').trim()) && this.ops.isApprover(String(m.from?.id ?? ''))) {
      void tg.sendNow(this.ops.stopCurrent(), { plain: true, replyTo: m.message_id });
      return;
    }
    const d = decide(m, { chatId: tg.chatId, botId: tg.botId, allowedUserIds: this.settings.allowedUserIds });
    if (!d.answer) {
      if (d.reason === 'not-allowed') {
        this.lastIgnored = { at: new Date().toISOString(), fromId: d.fromId ?? '', fromName: m.from?.first_name ?? m.from?.username ?? '' };
        console.log(`assistant: ignored a question from Telegram user ${d.fromId} (not in the allowed list)`);
      }
      if (d.help) void tg.sendNow('سؤالت رو بعد از /ask بنویس، یا روی یکی از پیام‌های ربات ریپلای کن.', { plain: true, replyTo: m.message_id });
      return;
    }
    if (Date.now() - m.date * 1000 > STALE_MS) return;
    this.rollDay();
    if (this.usage.questions >= this.settings.dailyLimit) {
      void tg.sendNow(`سقف ${this.settings.dailyLimit} سؤال امروز پر شده. از صفحه‌ی Assistant در نقشه می‌شه بیشترش کرد.`, { plain: true, replyTo: m.message_id });
      return;
    }
    if (this.inFlight >= MAX_PARALLEL) {
      void tg.sendNow('الان دارم به سؤال دیگه‌ای جواب می‌دم؛ چند لحظه بعد دوباره بپرس.', { plain: true, replyTo: m.message_id });
      return;
    }
    const key = this.key();
    const opsMode = this.ops.activeFor(d.fromId);
    const model = opsMode ? this.ops.actionModel : this.settings.model;
    if (!key || !model) return;
    this.inFlight++;
    this.usage.questions++;
    const typing = setInterval(() => void tg.call('sendChatAction', { chat_id: tg.chatId, action: 'typing' }), 4_500);
    void tg.call('sendChatAction', { chat_id: tg.chatId, action: 'typing' });
    try {
      const prev = d.replyToBotMessage !== null ? await this.hub.db.telegramMessage(tg.chatId, d.replyToBotMessage) : null;
      const thread = prev?.kind === 'answer' ? prev.thread ?? [] : [];
      const replyTo = prev && prev.kind !== 'answer' ? { kind: prev.kind, alertKey: prev.alertKey, at: prev.at, text: prev.text } : undefined;
      const tools: ToolContext = {
        api: this.readApi(),
        defaultAt: replyTo && (replyTo.kind === 'alert' || replyTo.kind === 'resolved') ? replyTo.at : null,
        parseAt,
        parseDuration,
      };
      const opsRuntime: OpsRuntime | undefined = opsMode ? this.ops.agentRuntime(new Vault(), d.fromId, tg.chatId) : undefined;
      const res = await answer({ question: d.question, thread, replyTo, now: Date.now(), boardUrl: this.hub.cfg.publicUrl, ops: opsRuntime }, this.chatFactory(key, model), tools);
      this.usage.promptTokens += res.usage.promptTokens;
      this.usage.completionTokens += res.usage.completionTokens;
      this.usage.costUsd += res.usage.costUsd;
      if (res.proposedTaskId) {
        // The approval message is the reply; send any closing line the model added, then stop.
        if (res.text) void tg.sendNow(res.text, { plain: true, replyTo: m.message_id });
        console.log(`ops: ${d.fromId} proposed plan ${res.proposedTaskId} in ${res.rounds} round(s)`);
        return;
      }
      console.log(`assistant: answered ${d.fromId} in ${res.rounds} round(s), tools: ${res.toolCalls.map((c) => c.name).join(',') || '-'}`);
      const parts = splitMessage(res.text);
      const nextThread = [...thread, { role: 'user' as const, content: d.question }, { role: 'assistant' as const, content: res.text }].slice(-12);
      for (const [i, part] of parts.entries()) {
        const sent = await tg.sendNow(part, { plain: true, replyTo: m.message_id });
        if (!sent.ok) throw new Error(`Telegram: ${sent.error}`);
        if (i === parts.length - 1 && sent.messageId !== undefined) {
          await this.hub.db.addTelegramMessage({ chatId: tg.chatId, messageId: sent.messageId, kind: 'answer', at: Date.now(), text: part, thread: nextThread });
        }
      }
    } catch (e: any) {
      const msg = e?.message ?? String(e);
      this.lastError = { at: new Date().toISOString(), error: msg };
      console.error(`assistant: ${msg}`);
      void tg.sendNow(`نتونستم جواب بدم: ${msg.slice(0, 300)}`, { plain: true, replyTo: m.message_id });
    } finally {
      clearInterval(typing);
      this.inFlight--;
    }
  }

  private readApi(): ReadApi {
    const q = this.queries;
    return {
      bundle: () => this.hub.bundle,
      status: (at) => q.status(at),
      diagnose: (at) => q.diagnose(at),
      describe: (id, at) => q.describe(id, at),
      why: (id, at) => q.why(id, at),
      path: (from, to, route, at) => q.path(from, to, route, at),
      events: (o) => q.events(o),
      metrics: (o) => q.metrics(o),
    };
  }

  private rollDay() {
    const day = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Tehran' }).format(new Date());
    if (this.usage.day !== day) this.usage = { day, questions: 0, promptTokens: 0, completionTokens: 0, costUsd: 0 };
  }
}

function sleep(ms: number) {
  return new Promise((r) => setTimeout(r, ms));
}
