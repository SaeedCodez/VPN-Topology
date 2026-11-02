import { Bundle, DriftItem } from '../contract';
import { LatestStore } from './store';

/**
 * Drift: things the servers report that the topology files do not declare.
 * The hub never invents topology; it lists these so `vpnmap discover` (phase 2)
 * can draft YAML and the board can show ghost nodes and a drift badge.
 */
export function computeDrift(b: Bundle, s: LatestStore, now = Date.now()): DriftItem[] {
  const out: DriftItem[] = [];
  const known = new Set(b.nodes.map((n) => n.id));
  const sources = new Set(['probe', ...(b.map.sources ?? []).map((x) => x.id)]);
  for (const [node, a] of s.agents) {
    if (!known.has(node) && !sources.has(node)) {
      out.push({ kind: 'agent', id: node, title: `Unknown agent "${node}"`, detail: `An agent reports as ${node} (${a.push.agent.hostname}) but no node with that id exists.`, host: node });
    }
  }
  for (const n of b.nodes) {
    if (!n.agent) continue;
    const a = s.agents.get(n.id);
    if (!a) continue;
    const declared = declaredPorts(b, n.id);
    const ranges = declaredRanges(b, n.id);
    const since = s.listenSince.get(n.id);
    const undeclared = (a.push.agent.capabilities.listening ?? []).filter(
      (p) => !declared.has(p) && !inRanges(p, ranges) && !ignoredSocket(p, now - (since?.get(p) ?? now)),
    );
    if (undeclared.length) {
      out.push({
        kind: 'port',
        id: `${n.id}:ports`,
        title: `${n.name}: ${undeclared.length} undeclared listening port${undeclared.length > 1 ? 's' : ''}`,
        detail: undeclared.join(', '),
        host: n.id,
      });
    }
  }
  return out;
}

/**
 * Sockets that are not services: the DHCP client, and unconnected UDP client
 * sockets (DNS resolvers, Xray UDP relays) on ephemeral ports (32768-60999),
 * which come and go. A UDP port in that range counts only after it has been
 * listening for 15 minutes, so a real service there (WireGuard's 51820) still
 * shows up.
 */
export const EPHEMERAL_UDP_GRACE_MS = 15 * 60_000;

export function ignoredSocket(p: string, ageMs = 0): boolean {
  const [proto, port] = p.split('/');
  const n = Number(port);
  if (proto !== 'udp') return false;
  if (n === 68) return true;
  return n >= 32768 && n <= 60999 && ageMs < EPHEMERAL_UDP_GRACE_MS;
}

/** Ranges from node `listens:` ("tcp/8000-8100"); single ports are in declaredPorts. */
function declaredRanges(b: Bundle, host: string): { proto: string; from: number; to: number }[] {
  const out: { proto: string; from: number; to: number }[] = [];
  for (const n of b.nodes) {
    if (n.id !== host) continue;
    for (const l of n.listens ?? []) {
      const m = /^(tcp|udp)\/(\d+)-(\d+)$/.exec(l);
      if (m) out.push({ proto: m[1], from: Number(m[2]), to: Number(m[3]) });
    }
  }
  return out;
}

function inRanges(p: string, ranges: { proto: string; from: number; to: number }[]): boolean {
  const [proto, port] = p.split('/');
  const n = Number(port);
  return ranges.some((r) => r.proto === proto && n >= r.from && n <= r.to);
}

const portRe = /(tcp|udp)\/(\d{1,5})|:(\d{2,5})\b/g;

/** Ports a host is known to serve, from checks and link/role text. */
export function declaredPorts(b: Bundle, host: string): Set<string> {
  const ports = new Set<string>(['tcp/22']);
  for (const l of b.nodes.find((n) => n.id === host)?.listens ?? []) if (!l.includes('-')) ports.add(l);
  const colocated = new Set([host]);
  for (const n of b.nodes) {
    if (n.agent) continue;
    const hosts = new Set([...(n.checks ?? []), ...(n.metrics ?? [])].map((c) => c.params.host).filter(Boolean));
    if (hosts.size === 1 && hosts.has(host)) colocated.add(n.id);
  }
  const addrs = new Set<string>();
  for (const id of colocated) for (const a of b.nodes.find((n) => n.id === id)?.address ?? []) addrs.add(a.split(':')[0]);
  const scan = (text: string | undefined, proto = 'tcp') => {
    if (!text) return;
    for (const m of text.matchAll(portRe)) {
      if (m[1]) ports.add(`${m[1]}/${m[2]}`);
      else if (m[3]) ports.add(`${proto}/${m[3]}`);
    }
  };
  for (const id of colocated) {
    const n = b.nodes.find((x) => x.id === id)!;
    scan(n.role);
    for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) {
      for (const p of [c.params.port, ...(c.params.ports ?? [])]) if (p) ports.add(`tcp/${p}`);
    }
  }
  for (const l of b.links) {
    if (!colocated.has(l.from) && !colocated.has(l.to)) continue;
    const proto = /udp/i.test(l.protocol ?? '') ? 'udp' : 'tcp';
    if (colocated.has(l.to)) scan(l.protocol, proto);
    for (const c of [l.traffic, l.traffic?.fallback, l.conns, ...(l.health ?? [])]) {
      if (!c) continue;
      const t: string | undefined = c.params.target;
      if (t && addrs.has(t.split(':')[0])) ports.add(`tcp/${t.split(':').pop()}`);
      if (c.params.host && colocated.has(c.params.host)) {
        for (const m of [c.params.up, c.params.down]) {
          if (!m) continue;
          const ps = [m.dport, m.sport].flat().filter(Boolean);
          if (m.hook === 'input' && m.dport) ps.forEach(() => [m.dport].flat().forEach((p: number) => ports.add(`${m.proto ?? 'tcp'}/${p}`)));
        }
        for (const p of [c.params.port, ...(c.params.ports ?? [])]) if (p && (c.params.side ?? 'local') === 'local') ports.add(`tcp/${p}`);
      }
    }
  }
  return ports;
}
