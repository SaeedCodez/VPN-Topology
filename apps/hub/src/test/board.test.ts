import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { execFileSync } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import { Resvg } from '@resvg/resvg-js';
import { Bundle, Snapshot } from '../contract';
import { LatestStore } from '../engine/store';
import { observe } from '../engine/resolve';
import { buildSnapshot } from '../engine/evaluate';
import { NotFound } from '../engine/views';
import { Simulator } from '../sim/simulator';
import { resolveSelection } from '../board/select';
import { renderBoardSvg } from '../board/render';
import { estimate, loadFonts, readAdvances } from '../board/fonts';
import { autoLayout } from '../board/shared/layout';
import { selectionFrom } from '../board/image';
import { TelegramSender } from '../notify/telegram';
import { answer } from '../assistant/agent';
import { ChatFn } from '../assistant/openrouter';
import { IMAGE_TOOL, ReadApi, runTool, ToolContext } from '../assistant/tools';
import { parseAt, parseDuration } from '../queries';

/** Pictures of the board: what a selection covers, what is drawn, how it reaches Telegram and the assistant. */

const root = path.resolve(__dirname, '../../../..');
const bin = path.join(root, 'bin', 'vpnmap');
const b: Bundle = JSON.parse(
  (fs.existsSync(bin) ? execFileSync(bin, ['bundle', '--dir', root], { maxBuffer: 64 << 20 }) : execFileSync('go', ['run', './apps/cli', 'bundle', '--dir', root], { cwd: root, maxBuffer: 64 << 20 })).toString(),
);

function snapshot(scenario: string | null): Snapshot {
  const t0 = Date.UTC(2026, 9, 13, 10, 0, 0);
  const store = new LatestStore();
  const sim = new Simulator(b, t0 - 3_600_000);
  if (scenario) sim.schedule(scenario, t0);
  let snap!: Snapshot;
  for (let t = t0 - 10 * 60_000; t <= t0 + 10 * 60_000; t += 10_000) {
    sim.step(t, store, (p, now) => store.ingest(p, now));
    snap = buildSnapshot(b, observe(b, store, t), { mode: 'demo', live: true, version: 1, since: new Map(), incidentSince: new Map(), alerts: [] });
  }
  return snap;
}
const steady = snapshot(null);
const positions = autoLayout(b);

test('selection: nothing = whole board; a route is its own nodes and hops', () => {
  const all = resolveSelection(b, steady, {});
  assert.equal(all.all, true);
  assert.equal(all.nodes.size, b.nodes.length);
  const main = resolveSelection(b, steady, { route: 'main' });
  assert.deepEqual([...main.nodes].sort(), ['cloudflare-edge', 'france-exit', 'internet', 'local-node', 'relay-1', 'users', 'xbridger-node-3']);
  assert.deepEqual([...main.links].sort(), ['e1', 'e2', 'e3', 'e5a', 'e5b', 'e6']);
  assert.equal(main.route, 'main');
  assert.throws(() => resolveSelection(b, steady, { route: 'nope' }), NotFound);
});

test('selection: users → France exit → internet follows the path, without the DIRECT link beside it', () => {
  const p = resolveSelection(b, steady, { from: 'users', via: ['france-exit'], to: 'internet' });
  assert.ok(p.nodes.has('france-exit') && p.nodes.has('internet') && p.nodes.has('users'));
  assert.ok(!p.links.has('e4'), 'e4 (DIRECT) is not on this path');
  assert.ok(!p.nodes.has('panel'));
  assert.equal(p.title, 'Users → France exit → Internet');
  // Names work too, case-insensitively.
  assert.ok(resolveSelection(b, steady, { from: 'Users', to: 'France exit' }).nodes.has('france-exit'));
  assert.throws(() => resolveSelection(b, steady, { from: 'users' }), /both from and to/);
  assert.throws(() => resolveSelection(b, steady, { nodes: ['ghost'] }), NotFound);
});

test('selection: explicit nodes show every link between them; links bring their ends', () => {
  const n = resolveSelection(b, steady, { nodes: ['relay-1', 'local-node', 'internet'] });
  assert.ok(n.links.has('e2') && n.links.has('e4'));
  const l = resolveSelection(b, steady, { links: ['E3'] });
  assert.deepEqual([...l.nodes].sort(), ['local-node', 'xbridger-node-3']);
  assert.deepEqual(selectionFrom({ nodes: 'a, b,', via: ['x'], route: ' ' }), { route: undefined, from: undefined, to: undefined, via: ['x'], nodes: ['a', 'b'], links: [] });
});

test('render: a route picture draws only its nodes, cropped, with the route users in the header', () => {
  const sel = resolveSelection(b, steady, { route: 'main' });
  const r = renderBoardSvg({ bundle: b, snap: steady, positions, selection: sel });
  assert.ok(r.svg.startsWith('<svg') && r.svg.endsWith('</svg>'));
  for (const id of sel.nodes) assert.ok(r.svg.includes(`>${b.nodes.find((n) => n.id === id)!.name}<`), `draws ${id}`);
  assert.ok(!r.svg.includes('>Panel server<') && !r.svg.includes('>Mullvad US<'), 'nothing else');
  assert.ok(r.svg.includes('users<'), 'route users in the header');
  assert.equal(r.status, 'ok');
  const whole = renderBoardSvg({ bundle: b, snap: steady, positions, selection: resolveSelection(b, steady, {}) });
  assert.ok(whole.width > r.width || whole.height > r.height, 'the route is a crop');
  assert.ok(whole.svg.includes('>Mullvad US<'));
  assert.ok(!/&(?!amp;|lt;|gt;|quot;)/.test(whole.svg), 'text is escaped');
  // With context, neighbours inside the crop are drawn, dimmed.
  const ctx = renderBoardSvg({ bundle: b, snap: steady, positions, selection: sel, context: true });
  assert.ok((ctx.svg.match(/opacity="0.28"/g) ?? []).length > 0);
  assert.equal((r.svg.match(/opacity="0.28"/g) ?? []).length, 0);
});

test('render: an outage shows the root cause on the card and Down in the header', () => {
  const snap = snapshot('xbridger-reboot-2026-10-13');
  const r = renderBoardSvg({ bundle: b, snap, positions, selection: resolveSelection(b, snap, { route: 'main' }), theme: 'dark' });
  assert.equal(r.status, 'down');
  assert.ok(r.svg.includes('>root cause<'));
  assert.ok(r.svg.includes('>Down<'));
  assert.ok(r.svg.includes('rgb(8,8,8)'), 'dark background');
});

test('fonts: advances come from the Geist files and the PNG renders with them', () => {
  const fonts = loadFonts();
  assert.ok(fonts, 'assets/fonts present');
  const geist = readAdvances(fs.readFileSync(fonts!.files.find((f) => f.endsWith('Geist-Regular.ttf'))!));
  assert.ok(geist.advance('M'.codePointAt(0)!) > geist.advance('i'.codePointAt(0)!));
  assert.equal(fonts!.measure('iiii', 10, 'mono'), fonts!.measure('MMMM', 10, 'mono'), 'mono is fixed width');
  const w = fonts!.measure('Relay 1 · 31.56.89.121', 15, 'sans', 600);
  assert.ok(Math.abs(w - estimate('Relay 1 · 31.56.89.121', 15, 'sans', 600)) < w * 0.25, 'estimate is in the same range');

  const r = renderBoardSvg({ bundle: b, snap: steady, positions, selection: resolveSelection(b, steady, { route: 'main' }), measure: fonts!.measure });
  const img = new Resvg(r.svg, { fitTo: { mode: 'zoom', value: 1 }, font: { fontFiles: fonts!.files, loadSystemFonts: false, defaultFontFamily: 'Geist' } }).render();
  const png = img.asPng();
  assert.deepEqual([...png.subarray(1, 4)].map((c) => String.fromCharCode(c)).join(''), 'PNG');
  assert.equal(img.width, r.width);
});

test('telegram: sendImage posts multipart to sendPhoto / sendDocument and never leaks the token', async () => {
  const token = '123456:AAbbCCddEEffGGhhIIjjKKllMMnnOOppQQ';
  const calls: { url: string; body: FormData; headers: any }[] = [];
  const answers = [
    { status: 200, body: { ok: true, result: { message_id: 42 } } },
    { status: 200, body: { ok: true, result: { message_id: 43 } } },
    { status: 413, body: { ok: false, description: 'Request Entity Too Large' } },
  ];
  const fakeFetch = (async (url: string, init: any) => {
    calls.push({ url, body: init.body, headers: init.headers });
    const a = answers.shift()!;
    return { ok: a.status === 200, status: a.status, statusText: '', json: async () => a.body } as any;
  }) as unknown as typeof fetch;
  const s = new TelegramSender({ apiRoot: 'https://relay.example/', token, chatId: '-100' }, fakeFetch);
  const png = Buffer.from([0x89, 0x50, 0x4e, 0x47]);
  const r = await s.sendImage(png, { caption: 'مسیر اصلی', replyTo: 7 });
  assert.deepEqual(r, { ok: true, messageId: 42 });
  assert.equal(calls[0].url, `https://relay.example/bot${token}/sendPhoto`);
  assert.ok(calls[0].body instanceof FormData);
  assert.equal(calls[0].body.get('chat_id'), '-100');
  assert.equal(calls[0].body.get('caption'), 'مسیر اصلی');
  assert.deepEqual(JSON.parse(String(calls[0].body.get('reply_parameters'))), { message_id: 7, allow_sending_without_reply: true });
  const photo = calls[0].body.get('photo') as Blob;
  assert.equal(photo.type, 'image/png');
  assert.equal(photo.size, 4);
  assert.equal(calls[0].headers['content-type'], undefined, 'fetch sets the multipart boundary itself');

  await s.sendImage(png, { asFile: true });
  assert.ok(calls[1].url.endsWith('/sendDocument') && calls[1].body.get('document'));
  const bad = await s.sendImage(png);
  assert.equal(bad.ok, false);
  assert.ok(!bad.error!.includes(token));
});

test('relay worker allows sendPhoto/sendDocument and passes the multipart content-type through', () => {
  const src = fs.readFileSync(path.join(root, 'apps/hub/deploy/telegram-relay.worker.js'), 'utf8');
  assert.ok(/sendPhoto\|sendDocument/.test(src));
  assert.ok(src.includes("req.headers.get('content-type')"));
});

test('assistant: board_image is offered only when pictures can be sent, and runs with the default moment', async () => {
  const api = { bundle: () => b } as unknown as ReadApi;
  const base: ToolContext = { api, defaultAt: 1_700_000_000_000, parseAt, parseDuration };
  const offered: string[][] = [];
  const chat = (replies: any[]): ChatFn =>
    (async (_m: any, tools: any[]) => {
      offered.push(tools.map((t) => t.function.name));
      return replies.shift();
    }) as ChatFn;
  const usage = { promptTokens: 1, completionTokens: 1, costUsd: 0 };
  await answer({ question: 'status', now: 0, boardUrl: 'x' }, chat([{ content: 'ok', toolCalls: [], usage }]), base);
  assert.ok(!offered[0].includes('board_image'));

  const sent: { args: any; at: number | null }[] = [];
  const withImage: ToolContext = { ...base, image: async (args, at) => (sent.push({ args, at }), { sent: true, as: 'photo' }) };
  const call = { id: '1', type: 'function', function: { name: 'board_image', arguments: JSON.stringify({ from: 'users', via: ['france-exit'], to: 'internet' }) } };
  const res = await answer(
    { question: 'یه عکس از بورد کاربرها تا فرانسه تا اینترنت بفرست', now: 0, boardUrl: 'x' },
    chat([{ content: null, toolCalls: [call], usage }, { content: 'فرستادم.', toolCalls: [], usage }]),
    withImage,
  );
  assert.ok(offered[1].includes('board_image'));
  assert.equal(res.text, 'فرستادم.');
  assert.deepEqual(sent, [{ args: { from: 'users', via: ['france-exit'], to: 'internet' }, at: 1_700_000_000_000 }]);
  assert.match(await runTool('board_image', '{}', base), /^error: pictures cannot be sent here/);
  assert.equal(IMAGE_TOOL.function.name, 'board_image');
});
