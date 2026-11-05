/**
 * Classifies a shell command as `read`, `change` or `refused`. Pure, no I/O.
 *
 * It is the gate between what the agent may run on its own and what needs the
 * owner's approval:
 *   - read    → `ssh_read` runs it immediately (looking only).
 *   - change  → runs only inside a plan the owner approved in Telegram.
 *   - refused → never runs, not even in an approved plan (destructive or it
 *               would tamper with the hub's own access).
 *
 * Conservative by construction: anything not clearly a read is a change, and
 * anything that could hide another command (substitution, redirection to a
 * file, find -exec) is never a read. The classifier does not try to be a
 * shell; it refuses what it cannot understand rather than guess it is safe.
 */

export type CmdClass = 'read' | 'change' | 'refused';

export interface Classification {
  kind: CmdClass;
  reason: string;
}

/** Operators that string several commands together; each piece is classified on its own. */
const SPLIT = /\s*(?:\|\||&&|;|\||&(?!>))\s*/;

/** Constructs that could smuggle a second command past the classifier — never a read. */
const SMUGGLE = [
  { re: /\$\(/, why: 'command substitution $(…)' },
  { re: /`/, why: 'command substitution with backticks' },
  { re: /<\(/, why: 'process substitution <(…)' },
  // `> file` / `>> file`, but not `2>/dev/null`, `>/dev/null` or `2>&1`.
  { re: /\d*>>?\s*(?!&|\/dev\/null\b)[^\s&|;>]/, why: 'output redirection to a file' },
  { re: /\btee\b/, why: 'tee writes a file' },
];

/** Refused anywhere, even inside an approved plan. */
const REFUSED: { re: RegExp; why: string }[] = [
  { re: /\bmkfs\b|\bmke2fs\b/, why: 'formats a filesystem' },
  { re: /\bdd\b[^|;]*\bof=\/dev\//, why: 'writes to a raw device' },
  { re: /\bwipefs\b|\bshred\b|\bfdisk\b|\bparted\b/, why: 'destroys a disk or partition table' },
  // recursive/forced rm aimed at / , ~ , or a top-level system dir (optionally one level down)
  { re: /\brm\b[^|;&]*?\s-[a-zA-Z]*[rf][a-zA-Z]*\b[^|;&]*?\s+(~|\/|\/(etc|var|usr|boot|home|lib|lib64|root|bin|sbin|opt|srv|dev|proc|sys|run|mnt|media|data)(\/[^\/\s]+)?\/?)(\s|$)/, why: 'removes a system root' },
  { re: /\b(nft|nftables)\b[^|;]*\bflush\b[^|;]*\bruleset\b/, why: 'flushes the whole firewall' },
  { re: /\biptables\b[^|;]*\s-F\b/, why: 'flushes the firewall chains' },
  { re: /:\(\)\s*\{.*\}\s*;/, why: 'fork bomb' },
  { re: /\bvpnmap-op\b/, why: 'touches the hub\'s own operator account' },
  { re: /authorized_keys/, why: 'touches SSH authorized_keys' },
  { re: /\/etc\/sudoers|sudoers\.d/, why: 'touches sudoers' },
  { re: /\buserdel\b|\bdeluser\b|\bpasswd\b\s+\w/, why: 'changes a user account' },
];

/** Prefixes (with their own args) that wrap another command; we look past them. */
function stripWrappers(tokens: string[]): string[] {
  let t = tokens;
  for (;;) {
    if (!t.length) return t;
    const w = t[0];
    if (w === 'sudo') {
      t = t.slice(1);
      // skip sudo flags and -u user
      while (t.length && t[0].startsWith('-')) t = t[0] === '-u' ? t.slice(2) : t.slice(1);
      continue;
    }
    if (w === 'nice' || w === 'ionice' || w === 'stdbuf' || w === 'nohup') {
      t = t.slice(1);
      while (t.length && t[0].startsWith('-')) t = t.slice(1);
      continue;
    }
    if (w === 'timeout') {
      t = t.slice(1);
      while (t.length && t[0].startsWith('-')) t = t.slice(1);
      if (t.length) t = t.slice(1); // the duration
      continue;
    }
    if (w === 'env') {
      t = t.slice(1);
      while (t.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(t[0])) t = t.slice(1);
      continue;
    }
    if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(w)) {
      t = t.slice(1); // leading VAR=value assignment
      continue;
    }
    // `ip netns exec <ns> <cmd...>` — classify the inner command.
    if (w === 'ip' && t[1] === 'netns' && t[2] === 'exec') {
      t = t.slice(4);
      continue;
    }
    return t;
  }
}

/** Read programs whose every invocation is a read. */
const READ_ALWAYS = new Set([
  'cat', 'ls', 'dir', 'vdir', 'grep', 'egrep', 'fgrep', 'zgrep', 'tail', 'head', 'wc', 'less', 'more', 'nl',
  'journalctl', 'dmesg', 'uptime', 'free', 'df', 'du', 'date', 'uname', 'hostname', 'hostnamectl', 'whoami', 'id',
  'ss', 'netstat', 'lsof', 'ps', 'pgrep', 'top', 'vmstat', 'iostat', 'mpstat', 'vnstat', 'conntrack',
  'stat', 'readlink', 'realpath', 'file', 'basename', 'dirname', 'cut', 'sort', 'uniq', 'tr', 'column', 'tac', 'echo', 'printf', 'true', 'test', '[',
  'dig', 'host', 'nslookup', 'getent', 'resolvectl', 'timedatectl', 'env', 'printenv', 'lsblk', 'findmnt', 'mount',
  'curl', 'wg-quick',
]);

/** Programs that read only with certain subcommands/flags. */
function subcommandPolicy(prog: string, args: string[]): CmdClass | null {
  const a0 = args[0] ?? '';
  switch (prog) {
    case 'systemctl': {
      const sub = args.find((x) => !x.startsWith('-')) ?? '';
      return ['status', 'is-active', 'is-enabled', 'is-failed', 'show', 'cat', 'list-units', 'list-unit-files', 'list-timers', 'list-sockets', 'get-default'].includes(sub) ? 'read' : 'change';
    }
    case 'docker':
    case 'podman': {
      const sub = args.find((x) => !x.startsWith('-')) ?? '';
      return ['ps', 'logs', 'inspect', 'stats', 'images', 'version', 'info', 'top', 'port', 'diff', 'events', 'df'].includes(sub) ? 'read' : 'change';
    }
    case 'wg':
      return a0 === '' || a0 === 'show' || a0 === 'showconf' ? 'read' : 'change';
    case 'ip': {
      const obj = args[0];
      const verb = args[1] ?? '';
      if (['monitor'].includes(obj)) return 'read';
      return verb === '' || verb === 'show' || verb === 'list' || verb === 'get' ? 'read' : 'change';
    }
    case 'nft':
      return a0 === 'list' ? 'read' : 'change';
    case 'iptables':
    case 'ip6tables':
      return args.some((x) => x === '-L' || x === '-S' || x === '--list' || x === '--list-rules') ? 'read' : 'change';
    case 'openssl':
      return ['x509', 's_client', 'verify', 'crl', 'asn1parse', 'dgst'].includes(a0) ? 'read' : 'change';
    case 'certbot':
      return args.includes('certificates') || args.includes('--dry-run') ? 'read' : 'change';
    case 'relay-ctl':
    case 'mullvad-ns':
    case 'mullvad':
      return ['status', 'list', 'show', 'get', 'version', 'account'].includes(a0) ? 'read' : 'change';
    case 'xray':
    case 'v2ray':
      return a0 === 'version' || a0 === 'test' ? 'read' : 'change';
    case 'find':
      return args.some((x) => x === '-delete' || x === '-exec' || x === '-execdir' || x === '-fprint' || x === '-ok') ? 'change' : 'read';
    case 'git':
      return ['status', 'log', 'show', 'diff', 'branch', 'remote', 'rev-parse', 'config', 'ls-files'].includes(a0) ? (a0 === 'config' && args.length > 2 ? 'change' : 'read') : 'change';
    default:
      return null;
  }
}

function classifySegment(seg: string): Classification {
  const s = seg.trim();
  if (!s) return { kind: 'read', reason: 'empty' };
  for (const r of REFUSED) if (r.re.test(s)) return { kind: 'refused', reason: r.why };
  for (const m of SMUGGLE) if (m.re.test(s)) return { kind: 'change', reason: m.why };
  const tokens = tokenize(s);
  const real = stripWrappers(tokens);
  if (!real.length) return { kind: 'change', reason: 'cannot parse the command' };
  const prog = real[0].replace(/^.*\//, ''); // basename, so /usr/bin/systemctl == systemctl
  const args = real.slice(1);
  const policy = subcommandPolicy(prog, args);
  if (policy) return { kind: policy, reason: policy === 'read' ? `${prog} (read-only form)` : `${prog} (changes state)` };
  if (READ_ALWAYS.has(prog)) {
    if (prog === 'curl' && args.some((x) => /^-[a-zA-Z]*[oO]$/.test(x) || x === '--output' || x === '-T' || x === '--upload-file' || /^-X$/.test(x))) return { kind: 'change', reason: 'curl writes a file or uses a non-GET method' };
    return { kind: 'read', reason: `${prog} is read-only` };
  }
  return { kind: 'change', reason: `${prog}: not a known read command` };
}

/** The class of a whole command line is the most restrictive of its segments. */
export function classify(cmd: string): Classification {
  const segments = splitTopLevel(cmd);
  let worst: Classification = { kind: 'read', reason: 'read-only' };
  const rank: Record<CmdClass, number> = { read: 0, change: 1, refused: 2 };
  for (const seg of segments) {
    const c = classifySegment(seg);
    if (rank[c.kind] > rank[worst.kind]) worst = c;
  }
  return worst;
}

/** Split on ; && || | & but not inside single/double quotes. */
function splitTopLevel(cmd: string): string[] {
  const out: string[] = [];
  let buf = '';
  let quote = '';
  for (let i = 0; i < cmd.length; i++) {
    const ch = cmd[i];
    if (quote) {
      buf += ch;
      if (ch === quote) quote = '';
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      buf += ch;
      continue;
    }
    const two = cmd.slice(i, i + 2);
    if (two === '&&' || two === '||') {
      out.push(buf);
      buf = '';
      i++;
      continue;
    }
    if (ch === ';' || ch === '|' || ch === '&' || ch === '\n') {
      out.push(buf);
      buf = '';
      continue;
    }
    buf += ch;
  }
  if (buf.trim()) out.push(buf);
  return out.length ? out : [cmd];
}

/** Whitespace tokenize, honouring quotes (quotes kept out of the tokens). */
function tokenize(s: string): string[] {
  const out: string[] = [];
  let buf = '';
  let quote = '';
  let has = false;
  for (const ch of s) {
    if (quote) {
      if (ch === quote) quote = '';
      else buf += ch;
      has = true;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
      continue;
    }
    if (/\s/.test(ch)) {
      if (has) out.push(buf);
      buf = '';
      has = false;
      continue;
    }
    buf += ch;
    has = true;
  }
  if (has) out.push(buf);
  return out;
}
