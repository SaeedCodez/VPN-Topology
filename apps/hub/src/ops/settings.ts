import { generateKeyPairSync } from 'crypto';
import { Bundle, OpsServerView } from '../contract';
import { Box, open, seal } from '../secretbox';
import { RE_FINGERPRINT } from './ssh';

/**
 * Ops settings, edited from the board (setting key `ops`). The one secret,
 * the hub's SSH private key, is generated here, stored AES-256-GCM encrypted
 * (secretbox purpose "ops-ssh-key") and never returned.
 *
 * Servers are not a registry of their own: every node whose ops: block says
 * `runner: true` is a server, so a node added to the topology shows up here
 * by itself. Per node the owner sets only access, an optional host/port
 * override, and the host-key fingerprint (required before any connection).
 * `enabled` is the master switch; the board's kill switch turns it off.
 */

export const OPS_KEY = 'ops';
const SSH_PURPOSE = 'ops-ssh-key';

export type Access = 'on' | 'off' | { until: number };

export interface OpsServerState {
  host?: string;
  port?: number;
  access: Access;
  hostKey?: string;
  lastOkAt?: number;
  lastError?: string;
  reportedConfig?: string;
}

export interface StoredOps {
  enabled: boolean;
  actionModel: string;
  approverUserIds: string[];
  sshPublicKey?: string;
  sshPrivateKey?: Box;
  servers: Record<string, OpsServerState>;
}

export const OPS_DEFAULTS: StoredOps = { enabled: false, actionModel: '', approverUserIds: [], servers: {} };

export interface OpsServerPatch {
  node: string;
  host?: string | null;
  port?: number | null;
  access?: 'on' | 'off' | { minutes: number };
  hostKey?: string | null;
}

export interface OpsPatch {
  enabled?: boolean;
  actionModel?: string;
  approverUserIds?: (string | number)[] | string;
  /** Regenerate the SSH keypair (every server must be granted again). */
  rotateKey?: boolean;
  servers?: OpsServerPatch[];
}

function parseIds(v: OpsPatch['approverUserIds']): string[] {
  const list = Array.isArray(v) ? v.map(String) : String(v ?? '').split(/[\s,]+/);
  return [...new Set(list.map((x) => x.trim()).filter((x) => /^\d{3,20}$/.test(x)))];
}

function ed25519(): { publicKey: string; privateKey: string } {
  const { publicKey, privateKey } = generateKeyPairSync('ed25519', { publicKeyEncoding: { format: 'pem', type: 'spki' }, privateKeyEncoding: { format: 'pem', type: 'pkcs8' } });
  return { publicKey: publicKey as string, privateKey: privateKey as string };
}

/** OpenSSH one-line form of an ed25519 public key (PEM SPKI → "ssh-ed25519 base64 vpnmap-hub"). */
export function toOpenSsh(pem: string): string {
  const der = Buffer.from(pem.replace(/-----[^-]+-----/g, '').replace(/\s+/g, ''), 'base64');
  const raw = der.subarray(der.length - 32);
  const part = (b: Buffer) => {
    const n = Buffer.alloc(4);
    n.writeUInt32BE(b.length);
    return Buffer.concat([n, b]);
  };
  return `ssh-ed25519 ${Buffer.concat([part(Buffer.from('ssh-ed25519')), part(raw)]).toString('base64')} vpnmap-hub`;
}

export function opsKeyOpenSsh(s: StoredOps): string | null {
  return s.sshPublicKey ? toOpenSsh(s.sshPublicKey) : null;
}

export function opsPrivateKey(s: StoredOps, secret: string): string | null {
  return s.sshPrivateKey ? open(s.sshPrivateKey, secret, SSH_PURPOSE) : null;
}

export function serverReachable(s: StoredOps, node: string, now = Date.now()): boolean {
  const srv = s.servers[node];
  if (!s.enabled || !srv) return false;
  if (srv.access === 'on') return true;
  return typeof srv.access === 'object' && srv.access.until > now;
}

const RE_HOST = /^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$|^[0-9a-fA-F:]{2,39}$/;

/** Apply a patch; returns the new value and readable changes (never a key). */
export function applyOpsPatch(cur: StoredOps, p: OpsPatch, secret: string, bundle: Bundle, now = Date.now()): { next: StoredOps; changes: string[] } {
  const next: StoredOps = { ...cur, approverUserIds: [...cur.approverUserIds], servers: Object.fromEntries(Object.entries(cur.servers).map(([k, v]) => [k, { ...v }])) };
  const changes: string[] = [];
  if (p.enabled !== undefined && !!p.enabled !== cur.enabled) {
    next.enabled = !!p.enabled;
    changes.push(next.enabled ? 'ops enabled' : 'ops disabled (kill switch)');
  }
  if ((next.enabled && !next.sshPrivateKey) || p.rotateKey) {
    const { publicKey, privateKey } = ed25519();
    next.sshPublicKey = publicKey;
    next.sshPrivateKey = seal(privateKey, secret, SSH_PURPOSE);
    for (const s of Object.values(next.servers)) delete s.lastOkAt;
    changes.push(p.rotateKey ? 'SSH key rotated (grant every server again)' : 'SSH key generated');
  }
  if (p.actionModel !== undefined) {
    const m = String(p.actionModel).trim();
    if (m.length > 120 || /\s/.test(m)) throw new Error('action model must be an OpenRouter model id');
    if (m !== cur.actionModel) {
      next.actionModel = m;
      changes.push(`action model → ${m || '(none)'}`);
    }
  }
  if (p.approverUserIds !== undefined) {
    const ids = parseIds(p.approverUserIds);
    if (ids.join(',') !== cur.approverUserIds.join(',')) {
      next.approverUserIds = ids;
      changes.push(`approvers → ${ids.length}`);
    }
  }
  const runners = new Set((bundle.ops ?? []).filter((x) => x.runner).map((x) => x.node));
  for (const sp of p.servers ?? []) {
    if (!runners.has(sp.node)) throw new Error(`node ${sp.node} has no ops: {runner: true} block in the topology`);
    const srv: OpsServerState = next.servers[sp.node] ?? { access: 'off' };
    if (sp.host !== undefined) {
      const h = sp.host ? String(sp.host).trim() : '';
      if (h && !RE_HOST.test(h)) throw new Error(`${sp.node}: host must be a host name or IP address`);
      srv.host = h || undefined;
    }
    if (sp.port !== undefined) srv.port = sp.port ? Math.min(65535, Math.max(1, Math.round(Number(sp.port)) || 22)) : undefined;
    if (sp.hostKey !== undefined) {
      const fp = sp.hostKey ? String(sp.hostKey).trim() : '';
      if (fp && !RE_FINGERPRINT.test(fp)) throw new Error(`${sp.node}: host key must look like SHA256:… (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub)`);
      if (fp !== (srv.hostKey ?? '')) changes.push(`${sp.node} host key ${fp ? 'set' : 'cleared'}`);
      srv.hostKey = fp || undefined;
    }
    if (sp.access !== undefined) {
      const a = sp.access;
      srv.access = a === 'on' || a === 'off' ? a : a && typeof a === 'object' && Number(a.minutes) > 0 ? { until: now + Math.min(Number(a.minutes), 24 * 60) * 60_000 } : 'off';
      changes.push(`${sp.node} access ${typeof srv.access === 'object' ? `for ${a && typeof a === 'object' ? a.minutes : ''} min` : srv.access}`);
    }
    next.servers[sp.node] = srv;
  }
  return { next, changes };
}

export function serverView(s: StoredOps, bundle: Bundle, node: string, running = false, now = Date.now()): OpsServerView {
  const p = (bundle.ops ?? []).find((x) => x.node === node);
  const n = bundle.nodes.find((x) => x.id === node);
  const srv = s.servers[node] ?? { access: 'off' as Access };
  const changes: string[] = [];
  for (const c of p?.config?.containers ?? []) if (c.restart) changes.push(`container.restart ${c.name}`);
  for (const u of p?.config?.units ?? []) if (u.restart) changes.push(`unit.restart ${u.name}`);
  for (const sc of p?.config?.scripts ?? []) if (sc.kind === 'change') changes.push(`script ${sc.id}`);
  void running;
  return {
    node,
    name: n?.name ?? node,
    host: srv.host || p?.address || '',
    port: srv.port ?? 22,
    access: srv.access === 'on' || srv.access === 'off' ? srv.access : { until: new Date(srv.access.until).toISOString() },
    reachable: serverReachable(s, node, now),
    hostKey: srv.hostKey ?? null,
    expectedConfig: p?.config_hash ?? null,
    reportedConfig: srv.reportedConfig ?? null,
    configCurrent: !!p?.config_hash && srv.reportedConfig === p.config_hash,
    lastOkAt: srv.lastOkAt ? new Date(srv.lastOkAt).toISOString() : null,
    lastError: srv.lastError ?? null,
    cutsUsers: !!p?.cuts_users,
    changes,
  };
}

/**
 * Commands the owner runs on one server (CLAUDE.md rule 3: the hub never
 * installs anything). The config is JSON, which the runner's YAML parser
 * reads as is; it holds no secret.
 */
export function grantCommands(opts: {
  hubUrl: string;
  node: string;
  config: object;
  binarySha: Record<string, string | null>;
  actions: { id: string; sha: string | null }[];
  publicKey: string;
}): { config: string; install: string; fingerprint: string; check: string; uninstall: string; missing: string[] } {
  const hub = opts.hubUrl.replace(/\/+$/, '');
  const missing: string[] = [];
  const sha = opts.binarySha.amd64 ?? '';
  if (!sha) missing.push('vpnmap-ops-linux-amd64 is not built into this hub');
  const acts = opts.actions.map((a) => {
    if (!a.sha) missing.push(`example action ${a.id} is not built into this hub; install your own script as /etc/vpnmap-ops/actions.d/${a.id}`);
    return a.sha ? `${a.id}:${a.sha}` : null;
  });
  const actionArg = acts.filter(Boolean).join(',');
  return {
    config: `cat > /tmp/vpnmap-ops.yaml <<'VPNMAP_OPS_EOF'\n${JSON.stringify(opts.config, null, 2)}\nVPNMAP_OPS_EOF`,
    install: [
      `curl -fsSL ${hub}/agent/ops-install.sh | sudo bash -s -- \\`,
      `  --hub ${hub} --node ${opts.node} --sha256 ${sha || '<missing>'} \\`,
      `  --key '${opts.publicKey}' \\`,
      `  --config-file /tmp/vpnmap-ops.yaml${actionArg ? ` \\\n  --actions ${actionArg}` : ''}`,
    ].join('\n') + (opts.binarySha.arm64 ? `\n# arm64 server: add --arch arm64 and use --sha256 ${opts.binarySha.arm64}` : ''),
    fingerprint: 'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub',
    check: 'sudo tail -5 /var/log/vpnmap-ops.log',
    uninstall: `curl -fsSL ${hub}/agent/ops-uninstall.sh | sudo bash`,
    missing,
  };
}
