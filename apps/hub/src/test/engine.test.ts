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
import { computeDrift, ignoredSocket } from '../engine/drift';
import { toRemnawaveNode } from '../engine/remnawave';

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
  const long = 'node:' + 'x'.repeat(80) + ':check-id';
  assert.equal(ssId(long, 2053), 'node-' + 'x'.repeat(53) + '-2053');
  assert.equal(ssId(long, 443), 'node-' + 'x'.repeat(54) + '-443');
});

test('steady state: only the known dead US tunnel is a root cause', () => {
  const s = run(null, 5);
  const roots = s.overall.rootCauses.map((r: any) => `${r.kind}:${r.elementId}`);
  assert.deepEqual(roots, ['link:e8-us']);
  assert.equal(s.routes.main.status, 'ok');
  // Only test users (Mullvad-Test) lost a route: amber, not red.
  assert.equal(s.overall.status, 'degraded');
  assert.equal(s.overall.rootCauses[0].critical, false);
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
  assert.equal(rc.critical, true, 'Premium/Economic lost their route');
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

test('a probe that cannot start its own client is "no data", not a broken route', () => {
  const store = new LatestStore();
  const push = (error: string) =>
    store.ingest({ v: 1, agent: { node: 'probe', kind: 'probe', version: 't', hostname: 'h', config_sha: '', capabilities: {} }, ts: new Date().toISOString(), seq: 1, probes: [{ route: 'main', ok: false, latency_ms: 0, error, at: new Date(Date.now() + Math.random()).toISOString() }] }, Date.now());
  for (let i = 0; i < 5; i++) push('sing-box: outbound file: no such file');
  assert.equal(store.probes.get('main')!.failures, 0);
  const obs = observe(b, store, Date.now());
  assert.equal(obs.checks['route:main:probe'].status, 'unknown');
});

test('E6 traffic comes from the France NIC counters (no nft needed)', () => {
  const s = run(null, 5);
  assert.ok(s.links.e6.downBps > 0 || s.links.e6.upBps > 0);
  assert.equal(s.links.e6.trafficSource, 'primary');
});

test('drift ignores ephemeral UDP client sockets, DHCP and declared port ranges', () => {
  assert.equal(ignoredSocket('udp/45000', 60_000), true, 'fresh ephemeral UDP client socket');
  assert.equal(ignoredSocket('udp/45000', 20 * 60_000), false, 'still there after 15 min: a service');
  assert.equal(ignoredSocket('udp/68', 1e9), true);
  assert.equal(ignoredSocket('tcp/45000'), false);
  const store = new LatestStore();
  const t0 = Date.now() - 30 * 60_000;
  const push = (listening: string[], at: number) =>
    store.ingest({ v: 1, agent: { node: 'xbridger-node-3', kind: 'agent', version: 't', hostname: 'h', config_sha: '', capabilities: { listening } }, ts: new Date(at).toISOString(), seq: 1 }, at);
  push(['tcp/22', 'tcp/80', 'tcp/443', 'tcp/8042', 'tcp/9050', 'udp/51820'], t0);
  push(['tcp/22', 'tcp/80', 'tcp/443', 'tcp/8042', 'tcp/9050', 'udp/51820', 'udp/40000'], Date.now() - 1000);
  const d = computeDrift(b, store).find((x) => x.host === 'xbridger-node-3')!;
  // 80 and the 8000-8100 / 9000-9100 ranges are declared; 443 (legacy realm, 08 #17) and the
  // long-lived WireGuard port are not; the fresh udp/40000 client socket is ignored.
  assert.equal(d.detail, 'tcp/443, udp/51820');
});

test('time parsing for --at', () => {
  assert.equal(parseAt(undefined), null);
  assert.equal(parseAt('2026-10-22T02:03Z'), Date.UTC(2026, 9, 22, 2, 3));
  const rel = parseAt('-2h')!;
  assert.ok(Math.abs(Date.now() - 2 * 3_600_000 - rel) < 1000);
});

test('Remnawave 3.4 node fields', () => {
  // Shape seen on panel 3.4.5: no isNodeOnline, isXrayRunning or xrayVersion.
  const live = { uuid: 'u', name: 'Local-Node', isConnected: true, isDisabled: false, xrayUptime: 1122998, versions: { xray: '26.7.28', node: '3.4.2' }, usersOnline: 12 };
  const up = toRemnawaveNode(live);
  assert.ok(up.isConnected && up.isNodeOnline && up.isXrayRunning);
  assert.equal(up.xrayUptime, 1122998);
  assert.equal(up.lastStatusMessage, null);

  const noXray = toRemnawaveNode({ ...live, xrayUptime: 0 });
  assert.equal(noXray.isXrayRunning, false);
  assert.equal(toRemnawaveNode({ ...live, isConnected: false }).isNodeOnline, false);

  // Older panels: their own fields win.
  assert.equal(toRemnawaveNode({ ...live, isXrayRunning: false }).isXrayRunning, false);
  assert.equal(toRemnawaveNode({ ...live, isNodeOnline: false }).isNodeOnline, false);
  assert.equal(toRemnawaveNode({ isConnected: true, xrayVersion: '25.1.1' }).isXrayRunning, true);
});
