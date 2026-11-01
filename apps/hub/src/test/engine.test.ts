import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { execFileSync } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import { Bundle } from '../contract';
import { LatestStore } from '../engine/store';
import { observe } from '../engine/resolve';
import { buildSnapshot } from '../engine/evaluate';
import { AlertEngine } from '../engine/alerts';
import { Simulator } from '../sim/simulator';
import { nftName, ssId, slug } from '../engine/ids';
import { parseAt } from '../api/api.controller';

/**
 * Regression tests for the evaluation pipeline: the simulator replays the
 * real incidents through ingest → resolve → evaluate, and the root cause must
 * be what an operator would name.
 */

const root = path.resolve(__dirname, '../../../..');

function loadBundle(): Bundle {
  const bin = path.join(root, 'bin', 'vpnmap');
  const out = fs.existsSync(bin)
    ? execFileSync(bin, ['bundle', '--dir', root], { maxBuffer: 64 << 20 })
    : execFileSync('go', ['run', './apps/cli', 'bundle', '--dir', root], { cwd: root, maxBuffer: 64 << 20 });
  return JSON.parse(out.toString());
}

const b = loadBundle();
const ctx = (alerts: any[] = []) => ({ mode: 'demo' as const, live: true, version: 1, since: new Map(), incidentSince: new Map(), alerts });

function run(scenario: string | null, minutesIn: number) {
  const t0 = Date.UTC(2026, 9, 13, 10, 0, 0);
  const store = new LatestStore();
  const sim = new Simulator(b, t0 - 3_600_000);
  const start = t0;
  if (scenario) sim.schedule(scenario, start);
  const alerts = new AlertEngine();
  let snap: any;
  for (let t = t0 - 10 * 60_000; t <= start + minutesIn * 60_000; t += 10_000) {
    sim.step(t, store, (p, now) => store.ingest(p, now));
    const obs = observe(b, store, t);
    snap = buildSnapshot(b, obs, ctx());
    snap.alerts = alerts.evaluate(b, snap, t, () => null).active;
  }
  return snap;
}

test('ids match the Go implementation (pkg/topology/ids.go)', () => {
  assert.equal(slug('link:e3:traffic-fallback'), 'link-e3-traffic-fallback');
  assert.equal(nftName('link:e3:traffic-fallback', 'up'), 'l_e3_traffic_fallback_up');
  assert.equal(ssId('link:e1:conns', 2053), 'link-e1-conns-2053');
});

test('steady state: only the known dead US tunnel is a root cause', () => {
  const s = run(null, 5);
  const roots = s.overall.rootCauses.map((r: any) => `${r.kind}:${r.elementId}`);
  assert.deepEqual(roots, ['link:e8-us']);
  assert.equal(s.routes.main.status, 'ok');
  assert.ok(s.links.e1.downBps > 0, 'E1 carries traffic');
  assert.ok(s.links.e3.trafficSource === 'primary', 'E3 traffic from Xray stats');
});

test('2026-10-13: :9000 closed after node 3 reboot → XBridger node 3 is the root cause', () => {
  const s = run('xbridger-reboot-2026-10-13', 10);
  const rc = s.overall.rootCauses[0];
  assert.equal(`${rc.kind}:${rc.elementId}`, 'node:xbridger-node-3');
  assert.match(rc.summary, /mt6snj5mlbnn/);
  assert.deepEqual(rc.routes, ['main']);
  assert.ok(rc.affected.nodes.includes('france-exit'));
  assert.ok(rc.affected.links.includes('e3'));
  assert.equal(s.nodes['relay-1'].impact, null, 'relay still serves the .ir route');
  assert.equal(s.overall.status, 'down');
  const ids = s.alerts.map((a: any) => a.id);
  assert.ok(ids.includes('main-probe-failing'));
  assert.ok(ids.includes('xbridger-connection-not-running'));
  assert.ok(ids.includes('server-rebooted'));
});

test('2026-10-13: after the fix the main route is healthy again', () => {
  const s = run('xbridger-reboot-2026-10-13', 31);
  assert.equal(s.routes.main.broken, false);
  // Still amber: France rebooted less than 30 minutes ago (node 3 just over 30).
  assert.equal(s.routes.main.status, 'degraded');
  assert.ok(s.nodes['france-exit'].rebootedRecently);
  assert.ok(!s.overall.rootCauses.some((r: any) => r.routes.includes('main')));
});

test('2026-10-22: rw-api hang (container healthy) → panel is the root cause, users keep working', () => {
  const s = run('rw-api-hang-2026-10-22', 5);
  const rc = s.overall.rootCauses[0];
  assert.equal(`${rc.kind}:${rc.elementId}`, 'node:panel');
  assert.equal(rc.impact, 'degraded');
  assert.ok(rc.routes.includes('subscription'));
  assert.equal(s.routes.main.status, 'ok', 'data plane unaffected');
  const backend = s.nodes.panel.checks.find((c: any) => c.id === 'backend');
  assert.equal(backend.status, 'ok', 'Docker still says healthy');
  assert.ok(s.alerts.some((a: any) => a.id === 'panel-api-slow'));
});

test('time parsing for --at', () => {
  assert.equal(parseAt(undefined), null);
  assert.equal(parseAt('2026-10-22T02:03Z'), Date.UTC(2026, 9, 22, 2, 3));
  const rel = parseAt('-2h')!;
  assert.ok(Math.abs(Date.now() - 2 * 3_600_000 - rel) < 1000);
});
