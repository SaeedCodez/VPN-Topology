/**
 * Sends messages to one Telegram chat through the Bot API (sendMessage).
 * Messages go out one at a time in order; a failed send is retried with
 * backoff (and Telegram's retry_after on 429) so a short network blip does
 * not lose an alert, but nothing here can block or crash the hub's tick.
 * The token is never logged: errors are reported without the request URL.
 */
export interface TelegramConfig {
  apiRoot: string;
  token: string;
  chatId: string;
}

export interface SendResult {
  ok: boolean;
  error?: string;
  /** Telegram's message_id of the sent message. */
  messageId?: number;
}

export interface CallResult {
  ok: boolean;
  status: number;
  result?: any;
  error?: string;
}

export interface SendOptions {
  replyTo?: number;
  /** Plain text (no HTML parsing). */
  plain?: boolean;
  onSent?: (messageId: number) => void;
}

const MAX_ATTEMPTS = 6;
const MAX_QUEUE = 200;

export class TelegramSender {
  private queue: { text: string; opts: SendOptions }[] = [];
  private busy = false;
  lastError: { at: string; error: string } | null = null;
  sent = 0;

  constructor(
    private readonly cfg: TelegramConfig,
    private readonly fetchFn: typeof fetch = fetch,
    private readonly sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
  ) {}

  get chatId(): string {
    return this.cfg.chatId;
  }

  /** The bot's own user id (the number before ':' in the token). */
  get botId(): string {
    return this.cfg.token.split(':')[0];
  }

  /** Queue a message; returns at once. */
  enqueue(text: string, opts: SendOptions = {}) {
    if (this.queue.length >= MAX_QUEUE) this.queue.shift(); // keep the newest during a long Telegram outage
    this.queue.push({ text, opts });
    if (!this.busy) void this.drain();
  }

  /** Send now and report the result (test message, assistant answers). */
  async sendNow(text: string, opts: SendOptions = {}): Promise<SendResult> {
    const r = await this.post(text, opts);
    if (r.ok && r.messageId !== undefined) opts.onSent?.(r.messageId);
    return r;
  }

  /** Any Bot API method (getUpdates, sendChatAction, ...). Never throws; the token is never in the error. */
  async call(method: string, body: Record<string, unknown>, timeoutMs = 15_000): Promise<CallResult> {
    const url = `${this.cfg.apiRoot.replace(/\/+$/, '')}/bot${this.cfg.token}/${method}`;
    try {
      const res = await this.fetchFn(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(timeoutMs),
      });
      const json: any = await res.json().catch(() => ({}));
      if (res.ok && json.ok !== false) return { ok: true, status: res.status, result: json.result };
      const retry = json?.parameters?.retry_after ? ` (retry after ${json.parameters.retry_after})` : '';
      return { ok: false, status: res.status, error: this.redact(`${res.status} ${json?.description ?? res.statusText}${retry}`) };
    } catch (e: any) {
      return { ok: false, status: 0, error: this.redact(`network: ${e?.cause?.code ?? e?.name ?? 'error'} ${e?.cause?.message ?? e?.message ?? ''}`.trim()) };
    }
  }

  get pending(): number {
    return this.queue.length;
  }

  private async drain() {
    this.busy = true;
    try {
      while (this.queue.length) {
        const { text, opts } = this.queue[0];
        let delay = 2_000;
        for (let attempt = 1; ; attempt++) {
          const r = await this.post(text, opts);
          if (r.ok) {
            if (r.messageId !== undefined) opts.onSent?.(r.messageId);
            break;
          }
          const retryAfter = /retry after (\d+)/i.exec(r.error ?? '');
          const permanent = /^(400|401|403|404)\b/.test(r.error ?? '');
          if (permanent || attempt >= MAX_ATTEMPTS) {
            console.error(`telegram: dropping a message after ${attempt} attempt(s): ${r.error}`);
            break;
          }
          await this.sleep(retryAfter ? Number(retryAfter[1]) * 1000 + 250 : delay);
          delay = Math.min(delay * 2, 60_000);
        }
        this.queue.shift();
      }
    } finally {
      this.busy = false;
    }
  }

  private async post(text: string, opts: SendOptions = {}): Promise<SendResult> {
    const r = await this.call('sendMessage', {
      chat_id: this.cfg.chatId,
      text,
      ...(opts.plain ? {} : { parse_mode: 'HTML' }),
      link_preview_options: { is_disabled: true },
      ...(opts.replyTo ? { reply_parameters: { message_id: opts.replyTo, allow_sending_without_reply: true } } : {}),
    });
    if (r.ok) {
      this.sent++;
      return { ok: true, messageId: typeof r.result?.message_id === 'number' ? r.result.message_id : undefined };
    }
    return this.fail(r.error ?? 'error');
  }

  private redact(s: string): string {
    // Belt and braces: never let the token reach a log line or an API answer.
    return s.split(this.cfg.token).join('<token>');
  }

  private fail(error: string): SendResult {
    const clean = this.redact(error);
    this.lastError = { at: new Date().toISOString(), error: clean };
    return { ok: false, error: clean };
  }
}
