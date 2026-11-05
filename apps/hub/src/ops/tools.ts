import { OpsPlan, OpsStep } from '../contract';
import { ToolDef } from '../assistant/openrouter';
import { Vault } from '../redact';
import { classify } from './classify';
import { RemnawaveClient } from './remnawave';
import { grantCommand, opsKeyOpenSsh, OpsServer, serverReachable, StoredOps } from './settings';
import { ExecResult, SshExec, SshTarget } from './ssh';

/**
 * The ops agent's tools, on top of the read-only map tools. `ssh_read` and
 * `rw_get` let it look at the servers; `propose_plan` is the only way it can
 * change anything, and even that just sends a plan to Telegram for the owner
 * to approve — the hub, not the model, runs an approved plan (see executor.ts).
 * A command that is not a pure read is refused by `ssh_read`; it must go in a
 * plan. Offered only to an approver when ops is enabled and a model is set.
 */

const MAX_OPS_OUTPUT = 8000;

const serverArg = { type: 'string', description: 'A server id from list_servers, e.g. relay-1, iran-mullvad.' };

export const OPS_TOOLS: ToolDef[] = [
  fn('list_servers', 'Servers the agent can reach over SSH (id, host, matching map node, access). Call before ssh_read.', {}),
  fn('ssh_read', 'Run ONE read-only command on a server and return its output (looking only: cat, ls, systemctl status, journalctl, docker ps/logs, wg show, ip … show, openssl x509, certbot certificates, …). A command that changes anything is refused here — put it in propose_plan instead.', { server: serverArg, command: { type: 'string', description: 'The shell command to run (read-only).' } }, ['server', 'command']),
  fn('rw_get', 'GET from the Remnawave panel API and return the JSON (read-only), e.g. /api/nodes, /api/users. Secrets are redacted.', { path: { type: 'string', description: 'API path, e.g. /api/nodes' } }, ['path']),
  fn('request_access', 'Get the one-line command the owner runs on a server to grant the agent SSH access, for a server that is not reachable yet.', { host: { type: 'string', description: 'The server IP or hostname.' }, id: { type: 'string', description: 'A short id to register it under (lowercase, dashes).' } }, ['host', 'id']),
  fn(
    'propose_plan',
    'Propose a plan that changes something, to send to the owner for approval. Explore first with the read tools; keep the plan as small as possible; give every step that changes state a rollback (or set backup files and use "restore"); list in verify the map ids (nodes/links/routes) that must stay healthy. You do NOT run the plan — the hub runs it only after the owner approves.',
    {
      title: { type: 'string', description: 'Short title in Persian.' },
      why: { type: 'string', description: 'Why this is needed, in Persian.' },
      impact: { type: 'string', description: 'Effect on users/routes while it runs, in Persian (e.g. "~۱۰ ثانیه قطعی برای ۴۰ کاربر مسیر Mullvad").' },
      steps: {
        type: 'array',
        description: 'Ordered steps.',
        items: {
          type: 'object',
          properties: {
            target: { type: 'string', description: 'A server id, or "remnawave" for a panel API call.' },
            run: { type: 'string', description: 'For a server: the shell command. For remnawave: a JSON string {"method","path","body"}.' },
            purpose: { type: 'string', description: 'What this step does, in Persian.' },
            backup: { type: 'array', items: { type: 'string' }, description: 'Absolute paths to copy aside before the step so "restore" can undo it.' },
            rollback: { type: 'string', description: 'How to undo: a shell command, a remnawave JSON call, or "restore" to put the backups back.' },
            critical: { type: 'boolean', description: 'True if this step touches a critical squad or the panel itself.' },
          },
          required: ['target', 'run', 'purpose'],
        },
      },
      verify: { type: 'array', items: { type: 'string' }, description: 'Map ids to re-check after each step (e.g. ["mullvad-route","iran-mullvad","e8-de"]).' },
    },
    ['title', 'why', 'impact', 'steps', 'verify'],
  ),
];

const OPS_TOOL_NAMES = new Set(OPS_TOOLS.map((t) => t.function.name));
export function isOpsTool(name: string): boolean {
  return OPS_TOOL_NAMES.has(name);
}

export interface OpsEnv {
  settings: StoredOps;
  privateKey: string | null;
  sshExec: SshExec;
  rw: RemnawaveClient;
  vault: Vault;
  /** Valid node/link/route ids, for checking `verify`. */
  validIds: Set<string>;
  /** Persist a pinned host key / lastOk; returns whether the key matched. */
  onHostKey: (serverId: string, fp: string) => Promise<{ ok: boolean; changed: boolean }>;
  /** Store the pending task and send the Telegram approval message. */
  createTask: (plan: OpsPlan) => Promise<{ taskId: string }>;
}

export interface OpsToolResult {
  output: string;
  proposedTaskId?: string;
}

export async function runOpsTool(name: string, rawArgs: string, env: OpsEnv): Promise<OpsToolResult> {
  let args: any = {};
  try {
    args = rawArgs ? JSON.parse(rawArgs) : {};
  } catch {
    return { output: 'error: arguments are not valid JSON' };
  }
  try {
    switch (name) {
      case 'list_servers':
        return { output: listServers(env) };
      case 'ssh_read':
        return { output: await sshRead(env, String(args.server ?? ''), String(args.command ?? '')) };
      case 'rw_get':
        return { output: await rwGet(env, String(args.path ?? '')) };
      case 'request_access':
        return { output: requestAccess(env, String(args.host ?? ''), String(args.id ?? '')) };
      case 'propose_plan':
        return await proposePlan(env, args);
      default:
        return { output: `error: unknown tool ${name}` };
    }
  } catch (e: any) {
    return { output: `error: ${e?.message ?? String(e)}` };
  }
}

function listServers(env: OpsEnv): string {
  const rows = env.settings.servers.map((s) => ({
    id: s.id,
    host: s.host,
    node: s.nodeId ?? null,
    reachable: serverReachable(env.settings, s),
    lastOk: s.lastOkAt ? new Date(s.lastOkAt).toISOString() : null,
  }));
  if (!rows.length) return 'No servers are registered yet. Use request_access to add one.';
  return JSON.stringify(rows);
}

function findServer(env: OpsEnv, id: string): OpsServer | undefined {
  return env.settings.servers.find((s) => s.id === id.trim());
}

async function sshRead(env: OpsEnv, id: string, command: string): Promise<string> {
  const srv = findServer(env, id);
  if (!srv) return `error: no server "${id}" (call list_servers)`;
  if (!serverReachable(env.settings, srv)) return `error: server "${id}" is not reachable (access is off). Use request_access to get the grant command.`;
  if (!env.privateKey) return 'error: the ops SSH key is not available';
  const c = classify(command);
  if (c.kind !== 'read') return `refused: this is not a read (${c.reason}). To change something, use propose_plan.`;
  const target: SshTarget = { host: srv.host, port: srv.port, user: srv.user };
  const r = await env.sshExec(target, env.privateKey, command, { vault: env.vault, maxOutput: MAX_OPS_OUTPUT, expectHostKey: srv.hostKey });
  if (r.hostKeyFp) {
    const hk = await env.onHostKey(srv.id, r.hostKeyFp);
    if (!hk.ok) return `error: the host key of "${id}" changed (${r.hostKeyFp}). Refusing for safety; the owner must check the server.`;
  }
  if (!r.ok && r.error) return `exit ${r.code ?? '?'}: ${r.error}\n${r.output}`.slice(0, MAX_OPS_OUTPUT);
  return (r.output || `(no output, exit ${r.code})`) + (r.truncated ? '\n… (truncated)' : '');
}

async function rwGet(env: OpsEnv, path: string): Promise<string> {
  if (!/^\/?api\//i.test(path)) return 'error: path must start with /api/';
  const r = await env.rw.request('GET', path, undefined, env.vault);
  if (!r.ok) return `error: ${r.error ?? 'request failed'}`;
  return JSON.stringify(r.body).slice(0, MAX_OPS_OUTPUT);
}

function requestAccess(env: OpsEnv, host: string, id: string): string {
  const pub = opsKeyOpenSsh(env.settings);
  if (!pub) return 'error: no SSH key yet; enable ops on the board first';
  const user = 'vpnmap-op';
  const cmd = grantCommand(user, pub, env.settings.hubIp);
  return [`The owner runs this once on ${host} (as root or with sudo) to grant access, then registers it as server id "${id}" on the board:`, '', cmd, '', 'After that, ssh_read works on that server. Tell the owner you need this; do not claim access you do not have yet.'].join('\n');
}

async function proposePlan(env: OpsEnv, raw: any): Promise<OpsToolResult> {
  const plan = normalizePlan(raw);
  const errs = validatePlan(plan, env);
  if (errs.length) return { output: `error: the plan is not valid:\n- ${errs.join('\n- ')}` };
  const { taskId } = await env.createTask(plan);
  return { output: `Plan sent to the owner for approval (task ${taskId}). It will run only if they approve. Tell the user briefly what you proposed and that you are waiting for their confirmation.`, proposedTaskId: taskId };
}

export function normalizePlan(raw: any): OpsPlan {
  const steps: OpsStep[] = Array.isArray(raw?.steps)
    ? raw.steps.map((s: any) => ({
        target: String(s?.target ?? '').trim(),
        run: parseRun(s?.run),
        purpose: s?.purpose ? String(s.purpose) : undefined,
        backup: Array.isArray(s?.backup) ? s.backup.map(String) : undefined,
        rollback: s?.rollback === undefined || s?.rollback === null ? null : s.rollback === 'restore' ? 'restore' : parseRun(s.rollback),
        critical: !!s?.critical,
      }))
    : [];
  return {
    title: String(raw?.title ?? '').trim(),
    why: String(raw?.why ?? '').trim(),
    impact: String(raw?.impact ?? '').trim(),
    steps,
    verify: Array.isArray(raw?.verify) ? raw.verify.map(String) : [],
  };
}

/** A step's `run`/`rollback`: a shell string, or a {method,path,body} API call (possibly given as a JSON string). */
function parseRun(v: any): OpsStep['run'] {
  if (v && typeof v === 'object' && v.method && v.path) return { method: String(v.method), path: String(v.path), body: v.body };
  const s = String(v ?? '');
  if (/^\s*\{/.test(s)) {
    try {
      const o = JSON.parse(s);
      if (o && o.method && o.path) return { method: String(o.method), path: String(o.path), body: o.body };
    } catch {
      /* a plain command that happens to start with { */
    }
  }
  return s;
}

export function validatePlan(plan: OpsPlan, env: OpsEnv): string[] {
  const errs: string[] = [];
  if (!plan.title) errs.push('title is required');
  if (!plan.steps.length) errs.push('a plan needs at least one step');
  if (plan.steps.length > 20) errs.push('too many steps (max 20)');
  plan.steps.forEach((s, i) => {
    const n = i + 1;
    if (s.target === 'remnawave') {
      if (typeof s.run !== 'object') errs.push(`step ${n}: a remnawave step needs {method,path,body}`);
    } else {
      const srv = findServer(env, s.target);
      if (!srv) errs.push(`step ${n}: unknown server "${s.target}"`);
      else if (!serverReachable(env.settings, srv)) errs.push(`step ${n}: server "${s.target}" is not reachable`);
      if (typeof s.run !== 'string') errs.push(`step ${n}: a server step's run must be a shell command`);
      else {
        const c = classify(s.run);
        if (c.kind === 'refused') errs.push(`step ${n}: refused command (${c.reason})`);
      }
      if (typeof s.rollback === 'string') {
        const rc = classify(s.rollback);
        if (rc.kind === 'refused') errs.push(`step ${n}: refused rollback (${rc.reason})`);
      }
    }
  });
  for (const id of plan.verify) if (!env.validIds.has(id)) errs.push(`verify id "${id}" is not a node/link/route on the map`);
  return errs;
}

function fn(name: string, description: string, properties: Record<string, unknown>, required: string[] = []): ToolDef {
  return { type: 'function', function: { name, description, parameters: { type: 'object', properties, required, additionalProperties: false } } };
}
