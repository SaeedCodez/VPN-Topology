import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import { NotionDocs, pageId, PART_CHARS } from '../assistant/notion';
import { answer, systemPrompt } from '../assistant/agent';
import { ChatFn } from '../assistant/openrouter';
import { DOCS_TOOLS, runTool, TOOLS, ToolContext } from '../assistant/tools';
import { parseAt, parseDuration } from '../queries';

/** Notion docs for the assistant: GET only, nothing outside the root page, secrets masked. */

const TOKEN = 'ntn_test_0123456789abcdefghijklmnopqrstuv';
const ROOT = '3f828d2a9e9b8115bba4ee2a8a69c8fc';
const RUNBOOK = '3f828d2a9e9b8106bcbcf4ff25e7d45b';
const ISSUES = '3f828d2a9e9b814da13efd6632b6eb8f';
const TOGGLE = 'aaaaaaaabbbbccccddddeeeeeeeeeeee';
const OUTSIDE = '99999999999999999999999999999999';

const dash = (id: string) => `${id.slice(0, 8)}-${id.slice(8, 12)}-${id.slice(12, 16)}-${id.slice(16, 20)}-${id.slice(20)}`;
const rt = (s: string, extra: object = {}) => ({ type: 'text', plain_text: s, annotations: {}, ...extra });
const blk = (id: string, type: string, v: object, hasChildren = false) => ({ id: dash(id), type, [type]: v, has_children: hasChildren });
const page = (id: string, title: string, parent: object, edited = '2026-11-06T00:00:00.000Z') => ({
  object: 'page',
  id: dash(id),
  url: `https://www.notion.so/${id}`,
  last_edited_time: edited,
  parent,
  properties: { title: { type: 'title', title: [rt(title)] } },
});

function fakeNotion() {
  const calls: { method: string; path: string; auth: string | null; version: string | null }[] = [];
  let rootEdited = '2026-11-06T00:00:00.000Z';
  const pages: Record<string, () => object> = {
    [ROOT]: () => page(ROOT, 'VPN Infrastructure', { type: 'workspace', workspace: true }, rootEdited),
    [RUNBOOK]: () => page(RUNBOOK, '07 · Operations Runbook', { type: 'page_id', page_id: dash(ROOT) }),
    // Lives inside a toggle block of the runbook page: parent is a block.
    [ISSUES]: () => page(ISSUES, '08 · Known Issues', { type: 'block_id', block_id: dash(TOGGLE) }),
    [OUTSIDE]: () => page(OUTSIDE, 'Personal finances', { type: 'workspace', workspace: true }),
  };
  const children: Record<string, object[]> = {
    [ROOT]: [
      blk('10000000000000000000000000000001', 'heading_2', { rich_text: [rt('TL;DR')] }),
      blk('10000000000000000000000000000002', 'bulleted_list_item', { rich_text: [rt('Relay 1 is a '), rt('realm', { annotations: { code: true } }), rt(' TCP forwarder, see '), { type: 'mention', plain_text: '07 · Operations Runbook', mention: { type: 'page', page: { id: dash(RUNBOOK) } } }] }),
      blk('10000000000000000000000000000003', 'paragraph', { rich_text: [rt('backup password: hunter2-very-secret')] }),
      blk('10000000000000000000000000000004', 'table', { table_width: 2 }, true),
      blk(RUNBOOK, 'child_page', { title: '07 · Operations Runbook' }),
    ],
    '10000000000000000000000000000004': [
      { type: 'table_row', table_row: { cells: [[rt('Component')], [rt('Host')]] } },
      { type: 'table_row', table_row: { cells: [[rt('Relay 1')], [rt('31.56.89.121')]] } },
    ],
    [RUNBOOK]: [
      blk('20000000000000000000000000000001', 'numbered_list_item', { rich_text: [rt('Check forwards with relay-ctl status')] }),
      blk('20000000000000000000000000000002', 'numbered_list_item', { rich_text: [rt('Back up before any panel change')] }),
      blk('20000000000000000000000000000003', 'code', { language: 'bash', rich_text: [rt('/opt/remnawave-backup/backup.sh --tag "reason"')] }),
      blk(TOGGLE, 'toggle', { rich_text: [rt('More')] }, true),
    ],
    [TOGGLE]: [blk(ISSUES, 'child_page', { title: '08 · Known Issues' })],
    [ISSUES]: [blk('30000000000000000000000000000001', 'paragraph', { rich_text: [rt('Port 9000 refuses connections after a node 3 reboot.')] })],
    [OUTSIDE]: [blk('40000000000000000000000000000001', 'paragraph', { rich_text: [rt('not for the bot')] })],
  };
  const blocks: Record<string, object> = { [TOGGLE]: { object: 'block', id: dash(TOGGLE), type: 'toggle', parent: { type: 'page_id', page_id: dash(RUNBOOK) } } };
  const json = (status: number, body: object) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
  const fetchFn = (async (url: string, init: RequestInit) => {
    const u = new URL(url);
    const h = new Headers(init.headers);
    calls.push({ method: String(init.method ?? 'GET'), path: u.pathname, auth: h.get('authorization'), version: h.get('notion-version') });
    let m = /^\/v1\/pages\/(\w+)$/.exec(u.pathname);
    if (m) return pages[m[1]] ? json(200, pages[m[1]]()) : json(404, { message: 'Could not find page' });
    m = /^\/v1\/blocks\/(\w+)\/children$/.exec(u.pathname);
    if (m) return children[m[1]] ? json(200, { results: children[m[1]], has_more: false, next_cursor: null }) : json(404, { message: 'nope' });
    m = /^\/v1\/blocks\/(\w+)$/.exec(u.pathname);
    if (m) return blocks[m[1]] ? json(200, blocks[m[1]]) : json(404, { message: 'nope' });
    return json(400, { message: `unexpected ${u.pathname}` });
  }) as unknown as typeof fetch;
  return { calls, fetchFn, children, touchRoot: () => (rootEdited = '2026-11-07T00:00:00.000Z') };
}

test('notion: page ids come from ids and URLs', () => {
  assert.equal(pageId('https://app.notion.com/p/3f828d2a9e9b8115bba4ee2a8a69c8fc'), ROOT);
  assert.equal(pageId('https://www.notion.so/VPN-Infrastructure-3f828d2a9e9b8115bba4ee2a8a69c8fc?pvs=4'), ROOT);
  assert.equal(pageId(dash(ROOT)), ROOT);
  assert.equal(pageId('not a page'), null);
  assert.equal(pageId(undefined), null);
});

test('notion: the main page as text, ids outside the masked text, secrets masked, GET only', async () => {
  const n = fakeNotion();
  let now = 1_000_000;
  const docs = new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: n.fetchFn, now: () => now });
  const p = await docs.read(undefined, 1);
  assert.equal(p.title, 'VPN Infrastructure');
  assert.match(p.text, /## TL;DR/);
  assert.match(p.text, /- Relay 1 is a `realm` TCP forwarder, see \[page: 07 · Operations Runbook\]/);
  assert.match(p.text, /\| Relay 1 \| 31\.56\.89\.121 \|/);
  assert.match(p.text, /\[subpage: 07 · Operations Runbook\]/);
  assert.ok(!p.text.includes('hunter2'), 'secret masked');
  assert.deepEqual(p.subpages, [{ page: RUNBOOK, title: '07 · Operations Runbook' }]);
  assert.deepEqual(p.linked, [{ page: RUNBOOK, title: '07 · Operations Runbook' }]);
  assert.equal(p.parts, 1);
  assert.ok(n.calls.every((c) => c.method === 'GET'), 'only reads');
  assert.ok(n.calls.every((c) => c.auth === `Bearer ${TOKEN}` && c.version === '2022-06-28'));
  assert.ok(!JSON.stringify(p).includes(TOKEN));

  // Within a minute: from the cache, no request.
  const before = n.calls.length;
  await docs.read(undefined, 1);
  assert.equal(n.calls.length, before);
  // Later, unchanged: one page request, blocks not fetched again.
  now += 120_000;
  await docs.read(undefined, 1);
  assert.equal(n.calls.length, before + 1);
  // Edited: rendered again.
  n.touchRoot();
  now += 120_000;
  await docs.read(undefined, 1);
  assert.ok(n.calls.length > before + 2);
});

test('notion: only pages under the root, also through a block parent', async () => {
  const n = fakeNotion();
  const docs = new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: n.fetchFn });
  const rb = await docs.read(`https://www.notion.so/Runbook-${RUNBOOK}`, 1);
  assert.match(rb.text, /1\. Check forwards with relay-ctl status\n2\. Back up before any panel change/);
  assert.match(rb.text, /```bash\n\/opt\/remnawave-backup\/backup\.sh --tag "reason"\n```/);
  assert.match(rb.text, /▸ More\n  \[subpage: 08 · Known Issues\]/, 'toggle children are indented');
  const ki = await docs.read(ISSUES, 1);
  assert.match(ki.text, /Port 9000 refuses/);
  await assert.rejects(docs.read(OUTSIDE, 1), /not part of the VPN documentation/);
  await assert.rejects(docs.read('00000000000000000000000000000000', 1), /not part of the VPN documentation/);
  await assert.rejects(docs.read('hello', 1), /page id or URL/);
  assert.ok(!n.calls.some((c) => c.path === `/v1/blocks/${OUTSIDE}/children`), 'the outside page is never read');
});

test('notion: full-text search over the tree, long pages in parts', async () => {
  const n = fakeNotion();
  const docs = new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: n.fetchFn });
  const r = await docs.search('port 9000 reboot');
  assert.equal(r.pagesSearched, 3);
  assert.equal(r.partial, false);
  assert.equal(r.hits[0].page, ISSUES);
  assert.match(r.hits[0].snippet, /Port 9000 refuses/);
  assert.ok(!n.calls.some((c) => c.path.includes(OUTSIDE)));
  await assert.rejects(docs.search(' ? '), /query is empty/);

  // A long page.
  const long = fakeNotion();
  for (let i = 0; i < 40; i++) long.children[RUNBOOK].push(blk(`5${String(i).padStart(31, '0')}`, 'paragraph', { rich_text: [rt(`step ${i} `.repeat(40))] }));
  const d2 = new NotionDocs({ token: TOKEN, rootId: () => RUNBOOK, fetchFn: long.fetchFn });
  const p1 = await d2.read(undefined, 1);
  assert.equal(p1.part, 1);
  assert.ok(p1.parts >= 3, `${p1.parts} parts`);
  assert.ok(p1.text.length <= PART_CHARS);
  assert.ok(JSON.stringify(p1).length < 8000, 'one part fits the tool output cap');
  assert.match((await d2.read(undefined, 2)).text, /step/);
  assert.equal((await d2.read(undefined, 99)).part, p1.parts, 'clamped to the last part');
});

test('notion: check explains a missing connection or a bad token', async () => {
  const notFound = (async () => new Response('{"message":"Could not find page"}', { status: 404 })) as unknown as typeof fetch;
  const r = await new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: notFound }).check();
  assert.equal(r.ok, false);
  assert.match(r.error!, /Connections/);
  const bad = (async () => new Response('{"message":"API token is invalid."}', { status: 401 })) as unknown as typeof fetch;
  const r2 = await new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: bad }).check();
  assert.match(r2.error!, /NOTION_TOKEN/);
  assert.ok(!r2.error!.includes(TOKEN));
  const ok = await new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: fakeNotion().fetchFn }).check();
  assert.deepEqual({ ok: ok.ok, title: ok.title, subpages: ok.subpages }, { ok: true, title: 'VPN Infrastructure', subpages: 1 });
});

test('agent: docs tools and prompt only when the docs are connected', async () => {
  const api = { bundle: () => ({ nodes: [], links: [], routes: [] }) } as any;
  const offered: string[][] = [];
  const chat: ChatFn = async (_m, tools) => (offered.push(tools.map((t) => t.function.name)), { content: 'ok', toolCalls: [], usage: { promptTokens: 1, completionTokens: 1, costUsd: null } });
  const base: ToolContext = { api, defaultAt: null, parseAt, parseDuration };
  await answer({ question: 'q', now: 0, boardUrl: '' }, chat, base);
  assert.deepEqual(offered[0], TOOLS.map((t) => t.function.name));
  const docs = new NotionDocs({ token: TOKEN, rootId: () => ROOT, fetchFn: fakeNotion().fetchFn });
  await answer({ question: 'q', now: 0, boardUrl: '' }, chat, { ...base, docs });
  assert.deepEqual(offered[1], [...TOOLS, ...DOCS_TOOLS].map((t) => t.function.name));
  assert.deepEqual(DOCS_TOOLS.map((t) => t.function.name), ['docs_read', 'docs_search']);
  assert.match(systemPrompt(0, '', false, false, true), /Notion/);
  assert.doesNotMatch(systemPrompt(0, '', false, false, false), /Notion/);
  assert.match(await runTool('docs_read', '{}', base), /^error: the documentation is not connected/);
  const out = JSON.parse(await runTool('docs_read', JSON.stringify({ page: RUNBOOK }), { ...base, docs }));
  assert.equal(out.title, '07 · Operations Runbook');
});
