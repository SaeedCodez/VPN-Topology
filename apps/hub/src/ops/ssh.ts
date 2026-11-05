import { createHash } from 'crypto';
import { Client } from 'ssh2';
import { redact } from '../redact';

/**
 * Sends one request line to vpnmap-ops on a server. The server's
 * authorized_keys entry forces `vpnmap-ops serve` whatever is sent, so the
 * line arrives only as SSH_ORIGINAL_COMMAND; the hub never builds or sends a
 * shell command. The host key must match the fingerprint the owner entered
 * (no trust-on-first-use): without one, nothing connects.
 */

export interface SshTarget {
  host: string;
  port: number;
  user: string;
  /** Required: "SHA256:<base64>" as printed by `ssh-keygen -lf`. */
  hostKey: string;
}

/** The runner's JSON answer (apps/ops-runner Response). */
export interface RunnerResponse {
  ok: boolean;
  node?: string;
  config_hash?: string;
  version?: string;
  type?: string;
  req?: string;
  exit_code?: number;
  output?: string;
  truncated?: boolean;
  error?: string;
  duration_ms?: number;
  cooldown_left_s?: number;
}

export interface SshResult {
  /** Transport worked and the runner answered with JSON. */
  answered: boolean;
  response: RunnerResponse | null;
  /** Fingerprint the server presented (for the panel's mismatch message). */
  hostKeyFp: string;
  error?: string;
}

export type SshSend = (target: SshTarget, privateKey: string, line: string, opts?: { timeoutMs?: number }) => Promise<SshResult>;

export const OPS_USER = 'vpnmap-ops';
const MAX_ANSWER = 256 << 10;

export function fingerprint(key: Buffer): string {
  return 'SHA256:' + createHash('sha256').update(key).digest('base64').replace(/=+$/, '');
}

export const RE_FINGERPRINT = /^SHA256:[A-Za-z0-9+/]{43}$/;

export const sshSend: SshSend = (target, privateKey, line, opts = {}) =>
  new Promise<SshResult>((resolve) => {
    if (!RE_FINGERPRINT.test(target.hostKey ?? '')) {
      resolve({ answered: false, response: null, hostKeyFp: '', error: 'no host-key fingerprint entered for this server' });
      return;
    }
    const timeoutMs = opts.timeoutMs ?? 180_000;
    const conn = new Client();
    let hostKeyFp = '';
    let out = '';
    let settled = false;
    const finish = (r: Omit<SshResult, 'hostKeyFp'>) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try {
        conn.end();
      } catch {
        /* ignore */
      }
      resolve({ ...r, hostKeyFp });
    };
    const timer = setTimeout(() => finish({ answered: false, response: null, error: `timed out after ${Math.round(timeoutMs / 1000)}s` }), timeoutMs);

    conn.on('ready', () => {
      conn.exec(line, (err, stream) => {
        if (err) return finish({ answered: false, response: null, error: err.message });
        stream.on('data', (c: Buffer) => {
          if (out.length < MAX_ANSWER) out += c.toString('utf8');
        });
        stream.stderr.on('data', () => undefined);
        stream.on('close', () => {
          const lastLine = out.trim().split('\n').pop() ?? '';
          try {
            const j = JSON.parse(lastLine) as RunnerResponse;
            if (typeof j !== 'object' || j === null || typeof j.ok !== 'boolean') throw new Error('bad answer');
            j.output = redact(String(j.output ?? ''));
            j.error = j.error ? redact(String(j.error)) : undefined;
            finish({ answered: true, response: j });
          } catch {
            finish({ answered: false, response: null, error: `the server did not answer like vpnmap-ops (${redact(lastLine).slice(0, 120)})` });
          }
        });
      });
    });
    conn.on('error', (e: Error) => finish({ answered: false, response: null, error: redact(e.message) }));
    conn.connect({
      host: target.host,
      port: target.port,
      username: target.user,
      privateKey,
      readyTimeout: Math.min(timeoutMs, 20_000),
      hostVerifier: (key: Buffer) => {
        hostKeyFp = fingerprint(key);
        return hostKeyFp === target.hostKey;
      },
    });
  });
