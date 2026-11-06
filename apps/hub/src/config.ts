import * as fs from 'fs';
import * as path from 'path';

/**
 * Hub configuration from environment variables (names are listed in the
 * README; values never go in the repo). Secrets stay in the hub env only.
 */
export interface Config {
  mode: 'demo' | 'live';
  port: number;
  publicUrl: string;
  databaseUrl?: string;
  pgliteDir?: string;
  victoriaUrl?: string;
  repoRoot: string;
  vpnmapBin: string;
  authDisabled: boolean;
  adminPasswordHash?: string;
  adminTotpSecret?: string;
  sessionSecret: string;
  cliTokenHashes: string[];
  agentTokens: Map<string, string>; // sha256 hex → node id
  rwApiUrl?: string;
  rwApiToken?: string;
  xui: Map<string, { url: string; user: string; pass: string }>;
  telegram?: { apiRoot: string; token: string; chatId: string };
  /** Read-only Notion docs for the assistant; rootPage empty = map.yaml `docs:`. */
  notion?: { token: string; rootPage?: string };
  demoBackfillHours: number;
  tickMs: number;
  webOrigin?: string;
  agentDistDir: string;
  webDistDir?: string;
}

function findRepoRoot(start: string): string {
  let d = path.resolve(start);
  for (;;) {
    if (fs.existsSync(path.join(d, 'topology', 'map.yaml'))) return d;
    const p = path.dirname(d);
    if (p === d) return path.resolve(start);
    d = p;
  }
}

export function loadConfig(env = process.env): Config {
  const mode = (env.HUB_MODE ?? 'demo') === 'live' ? 'live' : 'demo';
  const repoRoot = env.TOPOLOGY_ROOT ? path.resolve(env.TOPOLOGY_ROOT) : findRepoRoot(process.cwd());
  const agentTokens = new Map<string, string>();
  for (const part of (env.AGENT_TOKENS ?? '').split(',').map((s) => s.trim()).filter(Boolean)) {
    const [node, hash] = part.split('=');
    if (node && hash) agentTokens.set(hash.toLowerCase(), node);
  }
  const xui = new Map<string, { url: string; user: string; pass: string }>();
  for (const [k, v] of Object.entries(env)) {
    const m = /^XUI_([A-Z0-9_]+)_URL$/.exec(k);
    if (m && v) xui.set(m[1].toLowerCase(), { url: v, user: env[`XUI_${m[1]}_USER`] ?? '', pass: env[`XUI_${m[1]}_PASS`] ?? '' });
  }
  const authDisabled = env.AUTH_DISABLED ? env.AUTH_DISABLED === 'true' : mode === 'demo';
  if (mode === 'live' && authDisabled) throw new Error('AUTH_DISABLED=true is only allowed with HUB_MODE=demo');
  if (mode === 'live' && (!env.ADMIN_PASSWORD_HASH || !env.ADMIN_TOTP_SECRET || !env.SESSION_SECRET)) {
    throw new Error('live mode needs ADMIN_PASSWORD_HASH, ADMIN_TOTP_SECRET and SESSION_SECRET (see README)');
  }
  return {
    mode,
    port: Number(env.PORT ?? 4000),
    publicUrl: env.PUBLIC_URL ?? `http://localhost:${env.PORT ?? 4000}`,
    databaseUrl: env.DATABASE_URL || undefined,
    pgliteDir: env.PGLITE_DIR || undefined,
    victoriaUrl: env.VICTORIA_URL || undefined,
    repoRoot,
    vpnmapBin: env.VPNMAP_BIN ?? 'vpnmap',
    authDisabled,
    adminPasswordHash: env.ADMIN_PASSWORD_HASH,
    adminTotpSecret: env.ADMIN_TOTP_SECRET,
    sessionSecret: env.SESSION_SECRET ?? 'demo-only-session-secret',
    cliTokenHashes: (env.CLI_TOKEN_SHA256 ?? '').split(',').map((s) => s.trim().toLowerCase()).filter(Boolean),
    agentTokens,
    rwApiUrl: env.RW_API_URL,
    rwApiToken: env.RW_API_TOKEN,
    xui,
    // Compose passes unset variables as "", so an empty TELEGRAM_API_ROOT means
    // the default (direct to api.telegram.org; the hub has open internet).
    telegram: env.TELEGRAM_BOT_TOKEN?.trim() && env.TELEGRAM_CHAT_ID?.trim() ? { apiRoot: env.TELEGRAM_API_ROOT?.trim() || 'https://api.telegram.org', token: env.TELEGRAM_BOT_TOKEN.trim(), chatId: env.TELEGRAM_CHAT_ID.trim() } : undefined,
    notion: env.NOTION_TOKEN?.trim() ? { token: env.NOTION_TOKEN.trim(), rootPage: env.NOTION_ROOT_PAGE?.trim() || undefined } : undefined,
    demoBackfillHours: Number(env.DEMO_BACKFILL_HOURS ?? 25),
    tickMs: Number(env.TICK_MS ?? 5000),
    webOrigin: env.WEB_ORIGIN,
    agentDistDir: env.AGENT_DIST_DIR ?? path.join(repoRoot, 'dist', 'agent'),
    webDistDir: env.WEB_DIST_DIR,
  };
}
