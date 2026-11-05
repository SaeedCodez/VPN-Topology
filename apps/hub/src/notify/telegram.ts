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
}

const MAX_ATTEMPTS = 6;
const MAX_QUEUE = 200;

export class TelegramSender {
  private queue: string[] = [];
  private busy = false;
  lastError: { at: string; error: string } | null = null;
  sent = 0;

  constructor(
    private readonly cfg: TelegramConfig,
    private readonly fetchFn: typeof fetch = fetch,
    private readonly sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
  ) {}

  /** Queue a message; returns at once. */
  enqueue(text: string) {
    if (this.queue.length >= MAX_QUEUE) this.queue.shift(); // keep the newest during a long Telegram outage
    this.queue.push(text);
    if (!this.busy) void this.drain();
  }

  /** Send now and report the result (used by the "send a test message" endpoint). */
  async sendNow(text: string): Promise<SendResult> {
    return this.post(text);
  }

  get pending(): number {
    return this.queue.length;
  }

  private async drain() {
    this.busy = true;
    try {
      while (this.queue.length) {
        const text = this.queue[0];
        let delay = 2_000;
        for (let attempt = 1; ; attempt++) {
          const r = await this.post(text);
          if (r.ok) break;
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

  private async post(text: string): Promise<SendResult> {
    const url = `${this.cfg.apiRoot.replace(/\/+$/, '')}/bot${this.cfg.token}/sendMessage`;
    try {
      const res = await this.fetchFn(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ chat_id: this.cfg.chatId, text, parse_mode: 'HTML', link_preview_options: { is_disabled: true } }),
        signal: AbortSignal.timeout(15_000),
      });
      const body: any = await res.json().catch(() => ({}));
      if (res.ok && body.ok !== false) {
        this.sent++;
        return { ok: true };
      }
      const retry = body?.parameters?.retry_after ? ` (retry after ${body.parameters.retry_after})` : '';
      return this.fail(`${res.status} ${body?.description ?? res.statusText}${retry}`);
    } catch (e: any) {
      return this.fail(`network: ${e?.cause?.code ?? e?.name ?? 'error'} ${e?.cause?.message ?? e?.message ?? ''}`.trim());
    }
  }

  private fail(error: string): SendResult {
    // Belt and braces: never let the token reach a log line or an API answer.
    const clean = error.split(this.cfg.token).join('<token>');
    this.lastError = { at: new Date().toISOString(), error: clean };
    return { ok: false, error: clean };
  }
}
