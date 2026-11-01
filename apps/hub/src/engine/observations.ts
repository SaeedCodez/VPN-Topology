import { Health } from '../contract';

/**
 * Observations are the evaluated inputs of one moment: the result of every
 * check plus the numbers on links, nodes and routes. They are what the hub
 * stores in the time-series database, so the board at any past moment can be
 * rebuilt by running the same evaluation on stored observations.
 */
export interface CheckObs {
  status: Health;
  value: number | null;
  detail: string;
  observedAt: number | null; // ms
  source: string;
}

export interface NodeObs {
  cpu: number | null;
  mem: number | null;
  disk: number | null;
  load: number | null;
  uptime: number | null; // seconds
  net: number | null; // bps rx+tx
  bootTime: number | null; // unix seconds
  agentAge: number | null; // seconds since last push
  agentVersion?: string;
  configSha?: string;
}

export interface LinkObs {
  up: number | null;
  down: number | null;
  conns: number | null;
  src: 'primary' | 'fallback' | null;
}

export interface RouteObs {
  users: number | null;
  usersSource: 'live' | 'estimate' | null;
  probeFailures: number;
  latencyMs: number | null;
  exitCountry: string | null;
}

export interface Observations {
  at: number; // ms
  checks: Record<string, CheckObs>;
  nodes: Record<string, NodeObs>;
  links: Record<string, LinkObs>;
  routes: Record<string, RouteObs>;
  /** Extra node metric values: "node:<id>:<metric id>" → value. */
  metrics: Record<string, number | null>;
}

export const STATUS_CODE: Record<Health, number> = { ok: 0, degraded: 1, down: 2, unknown: -1 };
export const CODE_STATUS: Record<string, Health> = { '0': 'ok', '1': 'degraded', '2': 'down', '-1': 'unknown' };

export function emptyNodeObs(): NodeObs {
  return { cpu: null, mem: null, disk: null, load: null, uptime: null, net: null, bootTime: null, agentAge: null };
}
