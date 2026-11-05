import { redactValue, Vault } from '../redact';

/**
 * Minimal Remnawave panel API client for the ops agent, using the hub's
 * existing RW_API_URL / RW_API_TOKEN. GET is a read (offered to the agent as
 * `rw_get`); other methods change the panel and run only from an approved
 * plan. Read answers are redacted (user ids, keys → placeholders) before they
 * reach the model.
 */

export interface RwResult {
  ok: boolean;
  status: number;
  /** Redacted JSON (reads) — safe for the model and the log. */
  body: unknown;
  error?: string;
}

export interface RemnawaveClient {
  request(method: string, path: string, body: unknown, vault?: Vault): Promise<RwResult>;
}

const WRITE_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

export function remnawave(cfg: { rwApiUrl?: string; rwApiToken?: string }, fetchFn: typeof fetch = fetch): RemnawaveClient {
  return {
    async request(method, path, body, vault) {
      if (!cfg.rwApiUrl || !cfg.rwApiToken) return { ok: false, status: 0, body: null, error: 'Remnawave API is not configured (RW_API_URL / RW_API_TOKEN)' };
      const m = method.toUpperCase();
      const url = `${cfg.rwApiUrl.replace(/\/+$/, '')}/${String(path).replace(/^\/+/, '')}`;
      try {
        const res = await fetchFn(url, {
          method: m,
          headers: { Authorization: `Bearer ${cfg.rwApiToken}`, ...(WRITE_METHODS.has(m) ? { 'content-type': 'application/json' } : {}) },
          body: WRITE_METHODS.has(m) && body !== undefined ? JSON.stringify(body) : undefined,
          signal: AbortSignal.timeout(20_000),
        });
        const json = await res.json().catch(() => null);
        return { ok: res.ok, status: res.status, body: redactValue(json, vault), error: res.ok ? undefined : `${res.status} ${res.statusText}` };
      } catch (e: any) {
        return { ok: false, status: 0, body: null, error: `network: ${e?.cause?.code ?? e?.name ?? 'error'}` };
      }
    },
  };
}
