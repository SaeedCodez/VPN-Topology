import { generateKeyPairSync } from 'crypto';
import { OpsServerView, OpsSettingsView } from '../contract';
import { Box, open, seal } from '../secretbox';

/**
 * Ops-agent settings, edited from the board (setting key `ops`). Like the
 * assistant's key, the one secret here — the hub's SSH private key — is stored
 * AES-256-GCM encrypted (secretbox purpose "ops-ssh-key") and never returned;
 * only its public half is shown, so it can be installed on a server.
 *
 * `enabled` is the master switch (the kill switch on the board turns it off).
 * A server is reachable only when ops is enabled AND that server's access is
 * "on" (or a "until" window that has not passed). Nothing here runs a command;
 * it only decides what is allowed and where.
 */

export const OPS_KEY = 'ops';
const SSH_PURPOSE = 'ops-ssh-key';

export type Access = 'on' | 'off' | { until: number };

export interface OpsServer {
  id: string;
  host: string;
  port: number;
  user: string;
  /** Matching topology node, if any (for the map link and verify). */
  nodeId?: string;
  access: Access;
  /** SHA256 fingerprint of the host key, pinned on first successful connect. */
  hostKey?: string;
  lastOkAt?: number;
  note?: string;
}

export interface StoredOps {
  enabled: boolean;
  /** OpenRouter model used for ops runs (may differ from the Q&A model). */
  actionModel: string;
  /** Telegram user ids allowed to approve a plan (the owner). */
  approverUserIds: string[];
  /** Outbound IP of the hub, put in the SSH key's from="" restriction. */
  hubIp: string;
  sshPublicKey?: string;
  sshPrivateKey?: Box;
  servers: OpsServer[];
}

export const OPS_DEFAULTS: StoredOps = { enabled: false, actionModel: '', approverUserIds: [], hubIp: '', servers: [] };

export interface OpsPatch {
  enabled?: boolean;
  actionModel?: string;
  approverUserIds?: (string | number)[] | string;
  hubIp?: string;
  /** Regenerate the SSH keypair (invalidates every grant: servers must be re-granted). */
  rotateKey?: boolean;
  servers?: OpsServerPatch[];
}

export interface OpsServerPatch {
  id: string;
  host?: string;
  port?: number;
  user?: string;
  nodeId?: string | null;
  access?: Access;
  note?: string;
  /** Remove this server from the registry. */
  remove?: boolean;
}

function parseIds(v: OpsPatch['approverUserIds']): string[] {
  const list = Array.isArray(v) ? v.map(String) : String(v ?? '').split(/[\s,]+/);
  return [...new Set(list.map((x) => x.trim()).filter((x) => /^\d{3,20}$/.test(x)))];
}

function ed25519(): { publicKey: string; privateKey: string } {
  const { publicKey, privateKey } = generateKeyPairSync('ed25519', { publicKeyEncoding: { format: 'pem', type: 'spki' }, privateKeyEncoding: { format: 'pem', type: 'pkcs8' } });
  return { publicKey: publicKey as string, privateKey: privateKey as string };
}

/** OpenSSH one-line form of an ed25519 public key (PEM → "ssh-ed25519 base64 comment"). */
function toOpenSsh(pem: string): string {
  const der = Buffer.from(pem.replace(/-----[^-]+-----/g, '').replace(/\s+/g, ''), 'base64');
  const raw = der.subarray(der.length - 32); // last 32 bytes of the SPKI are the raw key
  const type = Buffer.from('ssh-ed25519');
  const part = (b: Buffer) => Buffer.concat([u32(b.length), b]);
  const blob = Buffer.concat([part(type), part(raw)]);
  return `ssh-ed25519 ${blob.toString('base64')} vpnmap-hub`;
}
function u32(n: number): Buffer {
  const b = Buffer.alloc(4);
  b.writeUInt32BE(n);
  return b;
}

export function opsKeyOpenSsh(s: StoredOps): string | null {
  return s.sshPublicKey ? toOpenSsh(s.sshPublicKey) : null;
}

export function opsPrivateKey(s: StoredOps, secret: string): string | null {
  return s.sshPrivateKey ? open(s.sshPrivateKey, secret, SSH_PURPOSE) : null;
}

/** Whether a server may be reached right now. */
export function serverReachable(s: StoredOps, srv: OpsServer, now = Date.now()): boolean {
  if (!s.enabled) return false;
  if (srv.access === 'on') return true;
  if (srv.access === 'off') return false;
  return typeof srv.access === 'object' && srv.access.until > now;
}

/** Apply a patch; returns the new value and human-readable changes (never a key). */
export function applyOpsPatch(cur: StoredOps, p: OpsPatch, secret: string): { next: StoredOps; changes: string[] } {
  const next: StoredOps = { ...cur, approverUserIds: [...cur.approverUserIds], servers: cur.servers.map((x) => ({ ...x })) };
  const changes: string[] = [];

  if (p.enabled !== undefined && !!p.enabled !== cur.enabled) {
    next.enabled = !!p.enabled;
    changes.push(next.enabled ? 'ops enabled' : 'ops disabled (kill switch)');
  }
  // Generate the keypair on first enable, or when asked to rotate.
  if ((next.enabled && !next.sshPrivateKey) || p.rotateKey) {
    const { publicKey, privateKey } = ed25519();
    next.sshPublicKey = publicKey;
    next.sshPrivateKey = seal(privateKey, secret, SSH_PURPOSE);
    changes.push(p.rotateKey ? 'SSH key rotated (re-grant every server)' : 'SSH key generated');
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
  if (p.hubIp !== undefined) {
    const ip = String(p.hubIp).trim();
    if (ip && !/^[0-9.]{7,15}$|^[0-9a-fA-F:]{2,39}$/.test(ip)) throw new Error('hub IP must be an IPv4 or IPv6 address');
    if (ip !== cur.hubIp) {
      next.hubIp = ip;
      changes.push(`hub IP → ${ip || '(none)'}`);
    }
  }
  for (const sp of p.servers ?? []) {
    const id = String(sp.id).trim().toLowerCase();
    if (!/^[a-z0-9][a-z0-9-]{0,40}$/.test(id)) throw new Error(`server id "${sp.id}" must be lowercase letters, digits and dashes`);
    const idx = next.servers.findIndex((x) => x.id === id);
    if (sp.remove) {
      if (idx >= 0) {
        next.servers.splice(idx, 1);
        changes.push(`server ${id} removed`);
      }
      continue;
    }
    const srv: OpsServer = idx >= 0 ? next.servers[idx] : { id, host: '', port: 22, user: 'vpnmap-op', access: 'off' };
    if (sp.host !== undefined) srv.host = String(sp.host).trim();
    if (sp.port !== undefined) srv.port = Math.min(65535, Math.max(1, Math.round(Number(sp.port)) || 22));
    if (sp.user !== undefined) srv.user = String(sp.user).trim() || 'vpnmap-op';
    if (sp.nodeId !== undefined) srv.nodeId = sp.nodeId ? String(sp.nodeId) : undefined;
    if (sp.note !== undefined) srv.note = String(sp.note).slice(0, 200);
    if (sp.access !== undefined) srv.access = normAccess(sp.access);
    if (!srv.host) throw new Error(`server ${id} needs a host`);
    if (idx < 0) {
      next.servers.push(srv);
      changes.push(`server ${id} added`);
    } else changes.push(`server ${id} updated`);
  }
  next.servers.sort((a, b) => a.id.localeCompare(b.id));
  return { next, changes };
}

function normAccess(a: Access): Access {
  if (a === 'on' || a === 'off') return a;
  if (a && typeof a === 'object' && Number.isFinite(a.until)) return { until: Number(a.until) };
  return 'off';
}

/** Trust-on-first-use: pin the host key the first time, otherwise report a change. */
export function recordHostKey(srv: OpsServer, fp: string): { ok: boolean; changed: boolean } {
  if (!srv.hostKey) {
    srv.hostKey = fp;
    return { ok: true, changed: false };
  }
  return { ok: srv.hostKey === fp, changed: srv.hostKey !== fp };
}

export function publicOpsView(s: StoredOps): OpsSettingsView {
  return {
    enabled: s.enabled,
    actionModel: s.actionModel,
    approverUserIds: s.approverUserIds,
    hubIp: s.hubIp,
    hasKey: !!s.sshPrivateKey,
    publicKey: opsKeyOpenSsh(s),
    servers: s.servers.map(serverView),
  };
}

export function serverView(srv: OpsServer, now = Date.now()): OpsServerView {
  const access = srv.access === 'on' ? 'on' : srv.access === 'off' ? 'off' : srv.access.until > now ? 'window' : 'expired';
  return {
    id: srv.id,
    host: srv.host,
    port: srv.port,
    user: srv.user,
    nodeId: srv.nodeId ?? null,
    access,
    accessUntil: typeof srv.access === 'object' ? new Date(srv.access.until).toISOString() : null,
    hostKey: srv.hostKey ?? null,
    lastOkAt: srv.lastOkAt ? new Date(srv.lastOkAt).toISOString() : null,
    note: srv.note ?? null,
  };
}

/** The one-line command the owner runs on a server to grant (or revoke) access. */
export function grantCommand(user: string, pubKey: string, hubIp: string): string {
  const from = hubIp ? `from=\\"${hubIp}\\",` : '';
  const restrict = `${from}no-agent-forwarding,no-port-forwarding,no-X11-forwarding`;
  return [
    `sudo useradd -m -s /bin/bash ${user} 2>/dev/null || true`,
    `sudo install -d -m 700 -o ${user} -g ${user} /home/${user}/.ssh`,
    `echo '${restrict} ${pubKey}' | sudo tee /home/${user}/.ssh/authorized_keys >/dev/null`,
    `sudo chown ${user}:${user} /home/${user}/.ssh/authorized_keys && sudo chmod 600 /home/${user}/.ssh/authorized_keys`,
    `echo '${user} ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/${user} >/dev/null && sudo chmod 440 /etc/sudoers.d/${user}`,
  ].join(' && ');
}

export function revokeCommand(user: string): string {
  return `sudo rm -f /etc/sudoers.d/${user} && sudo userdel -r ${user}`;
}
