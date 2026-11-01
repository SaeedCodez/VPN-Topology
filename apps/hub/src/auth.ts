import { createHash, createHmac, scryptSync, timingSafeEqual } from 'crypto';
import { CanActivate, ExecutionContext, Injectable, SetMetadata, UnauthorizedException } from '@nestjs/common';
import { Reflector } from '@nestjs/core';
import { Config } from './config';

/**
 * Single-admin auth: password (scrypt hash in ADMIN_PASSWORD_HASH) + TOTP
 * (ADMIN_TOTP_SECRET, RFC 6238) → signed session cookie. The CLI uses a
 * personal token whose sha256 is in CLI_TOKEN_SHA256. Agents push with
 * per-agent tokens (AGENT_TOKENS="node=sha256,..."), which only allow ingest.
 */

export const IS_PUBLIC = 'isPublic';
export const Public = () => SetMetadata(IS_PUBLIC, true);
export const SESSION_COOKIE = 'vpnmap_session';
const SESSION_MS = 12 * 3_600_000;

export const sha256 = (s: string) => createHash('sha256').update(s).digest('hex');

/** Hash format: scrypt$<N>$<r>$<p>$<salt b64>$<hash b64> (create with `vpnmap hash-password`). */
export function verifyPassword(password: string, stored: string | undefined): boolean {
  if (!stored) return false;
  const [algo, n, r, p, salt, hash] = stored.split('$');
  if (algo !== 'scrypt') return false;
  const want = Buffer.from(hash, 'base64');
  const got = scryptSync(password, Buffer.from(salt, 'base64'), want.length, { N: Number(n), r: Number(r), p: Number(p), maxmem: 256 * 1024 * 1024 });
  return got.length === want.length && timingSafeEqual(got, want);
}

function base32(s: string): Buffer {
  const alpha = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const c of s.replace(/=+$/, '').replace(/\s/g, '').toUpperCase()) {
    const v = alpha.indexOf(c);
    if (v < 0) throw new Error('invalid base32');
    bits += v.toString(2).padStart(5, '0');
  }
  const out: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(out);
}

export function totp(secret: string, at = Date.now(), step = 30): string {
  const counter = Math.floor(at / 1000 / step);
  const buf = Buffer.alloc(8);
  buf.writeBigUInt64BE(BigInt(counter));
  const h = createHmac('sha1', base32(secret)).update(buf).digest();
  const o = h[h.length - 1] & 0xf;
  const code = ((h.readUInt32BE(o) & 0x7fffffff) % 1_000_000).toString().padStart(6, '0');
  return code;
}

export function verifyTotp(secret: string | undefined, code: string): boolean {
  if (!secret || !/^\d{6}$/.test(code)) return false;
  const now = Date.now();
  return [-1, 0, 1].some((w) => {
    const a = Buffer.from(totp(secret, now + w * 30_000));
    const b = Buffer.from(code);
    return timingSafeEqual(a, b);
  });
}

export class Auth {
  private attempts = new Map<string, number[]>();
  constructor(private cfg: Config) {}

  issueSession(): { value: string; maxAge: number } {
    const exp = Date.now() + SESSION_MS;
    const sig = createHmac('sha256', this.cfg.sessionSecret).update(`admin.${exp}`).digest('base64url');
    return { value: `${exp}.${sig}`, maxAge: SESSION_MS };
  }

  validSession(v: string | undefined): boolean {
    if (!v) return false;
    const [exp, sig] = v.split('.');
    if (!exp || !sig || Number(exp) < Date.now()) return false;
    const want = createHmac('sha256', this.cfg.sessionSecret).update(`admin.${exp}`).digest('base64url');
    return want.length === sig.length && timingSafeEqual(Buffer.from(want), Buffer.from(sig));
  }

  validApiToken(token: string | undefined): boolean {
    if (!token) return false;
    const h = sha256(token);
    return this.cfg.cliTokenHashes.some((x) => x.length === h.length && timingSafeEqual(Buffer.from(x), Buffer.from(h)));
  }

  agentForToken(token: string | undefined): string | null {
    if (!token) return null;
    return this.cfg.agentTokens.get(sha256(token)) ?? null;
  }

  /** 5 failed logins per 15 minutes per IP. */
  allowAttempt(ip: string): boolean {
    const now = Date.now();
    const list = (this.attempts.get(ip) ?? []).filter((t) => now - t < 15 * 60_000);
    this.attempts.set(ip, list);
    return list.length < 5;
  }

  failed(ip: string) {
    this.attempts.set(ip, [...(this.attempts.get(ip) ?? []), Date.now()]);
  }

  login(ip: string, password: string, code: string): boolean {
    if (!this.allowAttempt(ip)) return false;
    const ok = verifyPassword(password, this.cfg.adminPasswordHash) && verifyTotp(this.cfg.adminTotpSecret, code);
    if (!ok) this.failed(ip);
    return ok;
  }

  /** Session cookie or CLI bearer token. */
  isAuthorized(req: any): boolean {
    if (this.cfg.authDisabled) return true;
    const cookie = parseCookies(req.headers?.cookie)[SESSION_COOKIE];
    if (this.validSession(cookie)) return true;
    const auth = String(req.headers?.authorization ?? '');
    return auth.startsWith('Bearer ') && this.validApiToken(auth.slice(7));
  }
}

export function parseCookies(h: string | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of (h ?? '').split(';')) {
    const i = part.indexOf('=');
    if (i > 0) out[part.slice(0, i).trim()] = decodeURIComponent(part.slice(i + 1).trim());
  }
  return out;
}

@Injectable()
export class AuthGuard implements CanActivate {
  constructor(
    private reflector: Reflector,
    private auth: Auth,
  ) {}

  canActivate(ctx: ExecutionContext): boolean {
    if (this.reflector.getAllAndOverride<boolean>(IS_PUBLIC, [ctx.getHandler(), ctx.getClass()])) return true;
    if (this.auth.isAuthorized(ctx.switchToHttp().getRequest())) return true;
    throw new UnauthorizedException('login required (web) or a personal token (CLI: vpnmap login)');
  }
}
