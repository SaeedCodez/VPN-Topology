/**
 * Names shared with the Go side (pkg/topology/agentgen.go). The agent config
 * generator and the hub must derive the same counter/target ids from a check
 * key, so keep both implementations in sync (there is a test on each side).
 */

const slugRe = /[^a-z0-9]+/g;

/** "link:e3:traffic-fallback" → "link-e3-traffic-fallback" */
export function slug(key: string): string {
  return key.toLowerCase().replace(slugRe, '-').replace(/^-+|-+$/g, '').slice(0, 63);
}

/** nft counter name: "link:e3:traffic-fallback", "up" → "l_e3_traffic_fallback_up" */
export function nftName(key: string, dir: 'up' | 'down'): string {
  const [kind, ...rest] = key.split(':');
  const body = rest.join('_').toLowerCase().replace(/[^a-z0-9]+/g, '_');
  const name = `${kind[0]}_${body}`.slice(0, 63 - dir.length - 1);
  return `${name}_${dir}`;
}

/** ss id per port: "link:e1:conns", 2053 → "link-e1-conns-2053" */
export function ssId(key: string, port: number): string {
  return `${slug(key)}-${port}`.slice(0, 63);
}

/** http / log ids */
export const httpId = (key: string) => slug(key);
export const logId = (key: string) => slug(key);

/** The id of the Remnawave panel metrics scrape on the panel agent. */
export const PROM_REMNAWAVE = 'remnawave';
