import { Bundle } from '../contract';

/**
 * Defense in depth for bundles pushed by `vpnmap apply`: the CLI already
 * validated the YAML; the hub checks the resolved structure before using it.
 */
export function validateBundle(b: Bundle): void {
  const errs: string[] = [];
  if (!b || typeof b !== 'object') throw new Error('bundle must be an object');
  if (b.schema !== 'vpnmap/v1') errs.push(`unsupported schema ${b.schema}`);
  if (!Array.isArray(b.nodes) || !Array.isArray(b.links) || !Array.isArray(b.routes) || !Array.isArray(b.alerts)) errs.push('nodes, links, routes and alerts must be lists');
  if (errs.length) throw new Error(errs.join('; '));
  const nodes = new Set<string>();
  for (const n of b.nodes) {
    if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(n.id)) errs.push(`bad node id ${n.id}`);
    if (nodes.has(n.id)) errs.push(`duplicate node ${n.id}`);
    nodes.add(n.id);
  }
  const links = new Set<string>();
  for (const l of b.links) {
    if (links.has(l.id)) errs.push(`duplicate link ${l.id}`);
    links.add(l.id);
    if (!nodes.has(l.from) || !nodes.has(l.to)) errs.push(`link ${l.id} references an unknown node`);
  }
  for (const r of b.routes) {
    for (const h of r.hops) if (!links.has(h)) errs.push(`route ${r.id} references unknown link ${h}`);
    if (r.nodes.length !== r.hops.length + 1) errs.push(`route ${r.id} has an inconsistent node list`);
  }
  for (const a of b.alerts) if (!a.expr) errs.push(`alert ${a.id} has no parsed expression`);
  if (!b.hash) errs.push('bundle has no hash');
  if (JSON.stringify(b).length > 4 * 1024 * 1024) errs.push('bundle is larger than 4 MiB');
  if (errs.length) throw new Error(`invalid bundle: ${errs.slice(0, 10).join('; ')}`);
}
