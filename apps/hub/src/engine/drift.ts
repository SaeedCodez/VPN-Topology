import { Bundle, DriftItem } from '../contract';
import { LatestStore } from './store';

/**
 * Drift: things the servers report that the topology files do not declare.
 * The hub never invents topology; it lists these so `vpnmap discover` (phase 2)
 * can draft YAML and the board can show ghost nodes and a drift badge.
 */
export function computeDrift(b: Bundle, s: LatestStore): DriftItem[] {
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
    const undeclared = (a.push.agent.capabilities.listening ?? []).filter((p) => !declared.has(p));
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

const portRe = /(tcp|udp)\/(\d{1,5})|:(\d{2,5})\b/g;

/** Ports a host is known to serve, from checks and link/role text. */
export function declaredPorts(b: Bundle, host: string): Set<string> {
  const ports = new Set<string>(['tcp/22']);
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
