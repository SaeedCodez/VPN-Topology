import { createHash } from 'crypto';
import { Client } from 'ssh2';
import { redact, Vault } from '../redact';

/**
 * Runs one command on a server over SSH as the `vpnmap-op` user, via
 * `sudo -n bash -c`. One connection per call (no long-lived shell). The host
 * key is returned as a SHA256 fingerprint so the caller can pin it on first
 * use and refuse a change afterwards. Output is capped and redacted; if a
 * Vault is given, secrets become placeholders instead of [REDACTED].
 *
 * This module only transports a command and returns its output. What may run
 * is decided elsewhere (classify.ts for reads, an approved plan for changes);
 * nothing here interprets the command.
 */

export interface SshTarget {
  host: string;
  port: number;
  user: string;
}

export interface ExecResult {
  ok: boolean;
  code: number | null;
  /** Redacted stdout+stderr for the model / the log. */
  output: string;
  /** SHA256 fingerprint of the server's host key ("SHA256:base64"). */
  hostKeyFp: string;
  error?: string;
  truncated: boolean;
}

export interface ExecOptions {
  timeoutMs?: number;
  maxOutput?: number;
  /** Redaction vault (for ops output going to the model). */
  vault?: Vault;
  /** Expected host-key fingerprint; connection is dropped if it differs. */
  expectHostKey?: string;
}

function fingerprint(key: Buffer): string {
  return 'SHA256:' + createHash('sha256').update(key).digest('base64').replace(/=+$/, '');
}

export type SshExec = (target: SshTarget, privateKey: string, command: string, opts?: ExecOptions) => Promise<ExecResult>;

export const sshExec: SshExec = (target, privateKey, command, opts = {}) =>
  new Promise<ExecResult>((resolve) => {
    const timeoutMs = opts.timeoutMs ?? 120_000;
    const maxOutput = opts.maxOutput ?? 64_000;
    const conn = new Client();
    let hostKeyFp = '';
    let out = '';
    let truncated = false;
    let settled = false;

    const finish = (r: Partial<ExecResult> & { ok: boolean }) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try {
        conn.end();
      } catch {
        /* ignore */
      }
      const text = opts.vault ? redact(out, opts.vault) : redact(out);
      resolve({ code: null, output: text, hostKeyFp, truncated, ...r });
    };

    const timer = setTimeout(() => finish({ ok: false, error: `timed out after ${Math.round(timeoutMs / 1000)}s` }), timeoutMs);

    conn.on('ready', () => {
      // sudo -n: never prompt; bash -c so pipes/redirects in an approved command work.
      conn.exec(`sudo -n bash -c ${shQuote(command)}`, (err, stream) => {
        if (err) return finish({ ok: false, error: err.message });
        const push = (chunk: Buffer) => {
          if (truncated) return;
          out += chunk.toString('utf8');
          if (out.length > maxOutput) {
            out = out.slice(0, maxOutput);
            truncated = true;
          }
        };
        stream.on('data', push);
        stream.stderr.on('data', push);
        stream.on('close', (code: number | null) => finish({ ok: code === 0, code }));
      });
    });
    conn.on('error', (e: Error & { level?: string }) => finish({ ok: false, error: redact(e.message) }));

    conn.connect({
      host: target.host,
      port: target.port,
      username: target.user,
      privateKey,
      readyTimeout: Math.min(timeoutMs, 20_000),
      keepaliveInterval: 0,
      hostVerifier: (key: Buffer) => {
        hostKeyFp = fingerprint(key);
        if (opts.expectHostKey && opts.expectHostKey !== hostKeyFp) return false;
        return true;
      },
    });
  });

/** Single-quote a string for the remote shell. */
function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}
