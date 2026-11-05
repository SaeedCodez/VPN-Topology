import { redactValue } from '../redact';

/**
 * Remnawave panel calls for the ops catalog: named operations with fixed
 * methods, paths and bodies. There is no generic request. A node is found by
 * its exact name (from the topology's remnawave-node check) in GET /api/nodes;
 * its uuid never comes from the model.
 *
 * Paths from remnawave/backend libs/contract (nodes and system controllers):
 *   GET  /api/nodes                         list nodes
 *   GET  /api/system/stats                  system stats
 *   POST /api/nodes/{uuid}/actions/restart  body {forceRestart:false}
 *   POST /api/nodes/{uuid}/actions/enable   no body
 *   POST /api/nodes/{uuid}/actions/disable  no body
 * Check them against the panel's version before switching ops on.
 */

export type RwNodeAction = 'restart' | 'enable' | 'disable';

export interface RwNode {
  uuid: string;
  name: string;
  isConnected: boolean;
  isDisabled: boolean;
  xrayUptime: number | null;
  usersOnline: number | null;
}

export interface RwClient {
  nodes(): Promise<RwNode[]>;
  systemStats(): Promise<Record<string, unknown>>;
  nodeAction(name: string, action: RwNodeAction): Promise<{ ok: boolean; detail: string }>;
}

const RE_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function remnawave(cfg: { rwApiUrl?: string; rwApiToken?: string }, fetchFn: typeof fetch = fetch): RwClient {
  const base = (cfg.rwApiUrl ?? '').replace(/\/+$/, '');
  const call = async (method: 'GET' | 'POST', path: string, body?: object): Promise<any> => {
    if (!base || !cfg.rwApiToken) throw new Error('Remnawave API is not configured (RW_API_URL, RW_API_TOKEN)');
    const r = await fetchFn(`${base}${path}`, {
      method,
      headers: { Authorization: `Bearer ${cfg.rwApiToken}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(20_000),
    });
    if (!r.ok) throw new Error(`Remnawave ${method} ${path.replace(/[0-9a-f-]{36}/gi, '{uuid}')}: HTTP ${r.status}`);
    return ((await r.json()) as any)?.response;
  };
  const nodes = async (): Promise<RwNode[]> =>
    ((await call('GET', '/api/nodes')) ?? []).map((n: any) => ({
      uuid: String(n.uuid ?? ''),
      name: String(n.name ?? ''),
      isConnected: !!n.isConnected,
      isDisabled: !!n.isDisabled,
      xrayUptime: typeof n.xrayUptime === 'number' ? n.xrayUptime : null,
      usersOnline: typeof n.usersOnline === 'number' ? n.usersOnline : null,
    }));
  return {
    nodes,
    async systemStats() {
      const s = (await call('GET', '/api/system/stats')) ?? {};
      // Counts only: never pass through anything else.
      return redactValue({ users: s.users, onlineStats: s.onlineStats, nodes: s.nodes, uptime: s.uptime }) as Record<string, unknown>;
    },
    async nodeAction(name, action) {
      const found = (await nodes()).filter((n) => n.name === name);
      if (found.length !== 1) throw new Error(`Remnawave node "${name}" found ${found.length} times`);
      const uuid = found[0].uuid;
      if (!RE_UUID.test(uuid)) throw new Error('Remnawave returned an invalid node uuid');
      const path = `/api/nodes/${uuid}/actions/${action}`;
      const res = await call('POST', path, action === 'restart' ? { forceRestart: false } : undefined);
      const n = res && typeof res === 'object' ? res : {};
      return { ok: true, detail: `${action} ${name}${'isDisabled' in n ? ` · disabled=${!!n.isDisabled}` : ''}${'isConnected' in n ? ` · connected=${!!n.isConnected}` : ''}` };
    },
  };
}
