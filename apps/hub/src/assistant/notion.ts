import { redact } from '../redact';

/**
 * Read-only access to the project's Notion pages for the assistant.
 *
 * Why read-only and how it is held there:
 * - The hub only ever sends GET /v1/pages, GET /v1/blocks, GET /v1/blocks/{id}/children
 *   and GET /v1/databases (to walk parents). No request in this file writes.
 * - The Notion integration itself should have only the "Read content"
 *   capability and be connected only to the root page ("VPN Infrastructure"),
 *   so Notion refuses anything else even if this code were wrong.
 * - A page is served only if its parent chain reaches the root page, so a page
 *   shared with the same integration by mistake stays invisible here.
 *
 * Every text that leaves this file has gone through redact() (same rules as
 * server output for the ops agent): Notion is meant to hold no secrets, this
 * is the second line. Page ids are returned outside the redacted text because
 * redact() masks long hex strings.
 */

export const NOTION_VERSION = '2022-06-28';
/** Characters of page text per docs_read part (the tool output cap is 8000 after JSON). */
export const PART_CHARS = 5500;
const MAX_BLOCKS_PER_PAGE = 2500;
const MAX_DEPTH = 4;
const MAX_PAGES = 60;
const META_TTL_MS = 60_000;
const PARENT_TTL_MS = 30 * 60_000;
const MAX_LIST = 40;

export interface DocsHit {
  page: string;
  title: string;
  snippet: string;
}

export interface DocsPage {
  page: string;
  title: string;
  url: string;
  lastEdited: string;
  part: number;
  parts: number;
  text: string;
  subpages: { page: string; title: string }[];
  linked: { page: string; title: string }[];
}

export interface DocsCheck {
  ok: boolean;
  ms: number;
  title?: string;
  subpages?: number;
  error?: string;
}

/** What the assistant tools need (NotionDocs, or a fake in tests). */
export interface DocsApi {
  search(query: string): Promise<{ hits: DocsHit[]; pagesSearched: number; partial: boolean }>;
  read(page: string | undefined, part: number): Promise<DocsPage>;
  check(): Promise<DocsCheck>;
}

interface Rendered {
  id: string;
  title: string;
  url: string;
  lastEdited: string;
  text: string;
  subpages: { page: string; title: string }[];
  linked: { page: string; title: string }[];
  checkedAt: number;
}

/** A Notion page id (32 hex, no dashes) from an id or any Notion URL; null if none. */
export function pageId(v: string | undefined | null): string | null {
  if (!v) return null;
  const s = String(v).trim();
  const dashed = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i.exec(s);
  if (dashed) return dashed[0].replace(/-/g, '').toLowerCase();
  const all = s.split('?')[0].match(/[0-9a-f]{32}/gi);
  return all ? all[all.length - 1].toLowerCase() : null;
}

class NotionError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

export class NotionDocs implements DocsApi {
  private pages = new Map<string, Rendered>();
  private parents = new Map<string, { inside: boolean; at: number }>();

  constructor(
    private readonly opts: {
      token: string;
      /** Root page id; resolved at call time (it may come from map.yaml). */
      rootId: () => string | null;
      apiRoot?: string;
      fetchFn?: typeof fetch;
      now?: () => number;
    },
  ) {}

  private now() {
    return (this.opts.now ?? Date.now)();
  }

  private root(): string {
    const r = this.opts.rootId();
    if (!r) throw new Error('no Notion root page is configured');
    return r;
  }

  // ------------------------------------------------------------------ public

  async check(): Promise<DocsCheck> {
    const t0 = this.now();
    try {
      const p = await this.render(this.root(), true);
      return { ok: true, ms: this.now() - t0, title: p.title, subpages: p.subpages.length };
    } catch (e: any) {
      return { ok: false, ms: this.now() - t0, error: friendly(e) };
    }
  }

  async read(page: string | undefined, part: number): Promise<DocsPage> {
    const root = this.root();
    const id = page ? pageId(page) : root;
    if (!id) throw new Error('page must be a Notion page id or URL (see subpages / docs_search)');
    if (!(await this.inside(id))) throw new Error('that page is not part of the VPN documentation');
    let p: Rendered;
    try {
      p = await this.render(id);
    } catch (e: any) {
      throw new Error(friendly(e));
    }
    const parts = Math.max(1, Math.ceil(p.text.length / PART_CHARS));
    const n = Math.min(Math.max(1, Math.floor(part || 1)), parts);
    return {
      page: p.id,
      title: p.title,
      url: p.url,
      lastEdited: p.lastEdited,
      part: n,
      parts,
      text: p.text.slice((n - 1) * PART_CHARS, n * PART_CHARS),
      subpages: p.subpages.slice(0, MAX_LIST),
      linked: p.linked.slice(0, MAX_LIST),
    };
  }

  /** Full-text search over every page under the root (Notion's own search only matches titles). */
  async search(query: string): Promise<{ hits: DocsHit[]; pagesSearched: number; partial: boolean }> {
    const terms = [...new Set(query.toLowerCase().split(/[\s,،؛;:!?؟()"'«»]+/).filter((t) => t.length >= 2))];
    if (!terms.length) throw new Error('query is empty');
    const { pages, partial } = await this.crawl();
    const scored: { score: number; hit: DocsHit }[] = [];
    for (const p of pages) {
      const titleHit = terms.filter((t) => p.title.toLowerCase().includes(t)).length;
      const lines = p.text.split('\n');
      let best = { score: 0, i: -1 };
      const perPage: { score: number; i: number }[] = [];
      lines.forEach((line, i) => {
        const l = line.toLowerCase();
        const s = terms.filter((t) => l.includes(t)).length;
        if (s > 0) perPage.push({ score: s, i });
        if (s > best.score) best = { score: s, i };
      });
      perPage.sort((a, b) => b.score - a.score || a.i - b.i);
      for (const m of perPage.slice(0, 2)) {
        const ctx = lines.slice(Math.max(0, m.i - 1), m.i + 2).join('\n');
        scored.push({ score: m.score * 2 + titleHit, hit: { page: p.id, title: p.title, snippet: clip(ctx, 360) } });
      }
      if (!perPage.length && titleHit) scored.push({ score: titleHit, hit: { page: p.id, title: p.title, snippet: clip(lines.find((l) => l.trim()) ?? '', 200) } });
    }
    scored.sort((a, b) => b.score - a.score);
    return { hits: scored.slice(0, 8).map((s) => s.hit), pagesSearched: pages.length, partial };
  }

  // ------------------------------------------------------------------ tree

  /** Root and every sub-page below it (child_page blocks only, so all are inside by construction). */
  private async crawl(): Promise<{ pages: Rendered[]; partial: boolean }> {
    const out: Rendered[] = [];
    const seen = new Set<string>();
    const queue = [this.root()];
    let partial = false;
    while (queue.length && out.length < MAX_PAGES) {
      const batch = queue.splice(0, 3);
      const res = await Promise.all(
        batch.map(async (id) => {
          if (seen.has(id)) return null;
          seen.add(id);
          try {
            return await this.render(id);
          } catch (e) {
            if (!out.length) throw e; // the root itself failed: say why
            partial = true;
            return null;
          }
        }),
      );
      for (const p of res) {
        if (!p) continue;
        out.push(p);
        for (const c of p.subpages) {
          this.parents.set(c.page, { inside: true, at: this.now() });
          if (!seen.has(c.page)) queue.push(c.page);
        }
      }
    }
    if (queue.length) partial = true;
    return { pages: out, partial };
  }

  /** True when the page's parent chain reaches the root page. */
  private async inside(id: string): Promise<boolean> {
    const root = this.root();
    if (id === root) return true;
    const cached = this.parents.get(id);
    if (cached && this.now() - cached.at < PARENT_TTL_MS) return cached.inside;
    const chain: string[] = [];
    let kind: string = 'page';
    let cur = id;
    let inside = false;
    for (let i = 0; i < 16; i++) {
      const c = this.parents.get(cur);
      if (kind === 'page' && c && this.now() - c.at < PARENT_TTL_MS) {
        inside = c.inside;
        break;
      }
      chain.push(cur);
      let obj: any;
      try {
        obj = await this.get(kind === 'page' ? `/v1/pages/${cur}` : kind === 'database' ? `/v1/databases/${cur}` : `/v1/blocks/${cur}`);
      } catch (e: any) {
        if (e instanceof NotionError && e.status === 404) break;
        throw new Error(friendly(e));
      }
      const parent = obj?.parent ?? {};
      if (parent.type === 'page_id') [kind, cur] = ['page', String(parent.page_id).replace(/-/g, '')];
      else if (parent.type === 'database_id') [kind, cur] = ['database', String(parent.database_id).replace(/-/g, '')];
      else if (parent.type === 'block_id') [kind, cur] = ['block', String(parent.block_id).replace(/-/g, '')];
      else break; // workspace: the chain ended without the root
      if (kind === 'page' && cur === root) {
        inside = true;
        break;
      }
    }
    const at = this.now();
    // Ids are unique across pages, blocks and databases, so one map serves all.
    for (const c of chain) this.parents.set(c, { inside, at });
    return inside;
  }

  // ------------------------------------------------------------------ one page

  private async render(id: string, fresh = false): Promise<Rendered> {
    const cached = this.pages.get(id);
    if (cached && !fresh && this.now() - cached.checkedAt < META_TTL_MS) return cached;
    const meta: any = await this.get(`/v1/pages/${id}`);
    const lastEdited = String(meta.last_edited_time ?? '');
    if (cached && cached.lastEdited === lastEdited) {
      cached.checkedAt = this.now();
      return cached;
    }
    const title = redact(pageTitle(meta));
    const subpages: { page: string; title: string }[] = [];
    const linked = new Map<string, string>();
    const lines: string[] = [];
    const budget = { blocks: MAX_BLOCKS_PER_PAGE };
    await this.blocks(id, 0, lines, { subpages, linked }, budget);
    if (budget.blocks <= 0) lines.push('…(page too long; the rest is not shown)');
    const r: Rendered = {
      id,
      title,
      url: String(meta.url ?? `https://www.notion.so/${id}`),
      lastEdited,
      text: redact(lines.join('\n').replace(/\n{3,}/g, '\n\n').trim()),
      subpages,
      linked: [...linked].filter(([p]) => p !== id).map(([page, t]) => ({ page, title: redact(t) })),
      checkedAt: this.now(),
    };
    this.pages.set(id, r);
    return r;
  }

  private async blocks(
    parent: string,
    depth: number,
    out: string[],
    refs: { subpages: { page: string; title: string }[]; linked: Map<string, string> },
    budget: { blocks: number },
  ): Promise<void> {
    let cursor: string | undefined;
    const pad = '  '.repeat(depth);
    let numbered = 0;
    do {
      const res: any = await this.get(`/v1/blocks/${parent}/children?page_size=100${cursor ? `&start_cursor=${encodeURIComponent(cursor)}` : ''}`);
      for (const b of res.results ?? []) {
        if (budget.blocks-- <= 0) return;
        numbered = b.type === 'numbered_list_item' ? numbered + 1 : 0;
        const line = blockLine(b, numbered, refs.linked);
        if (b.type === 'child_page') {
          const t = b.child_page?.title ?? 'Untitled';
          refs.subpages.push({ page: bare(b.id), title: redact(t) });
          out.push(`${pad}[subpage: ${t}]`);
          continue; // a sub-page is read on its own
        }
        if (b.type === 'table') {
          await this.table(bare(b.id), pad, out, refs.linked);
          continue;
        }
        if (line !== null) out.push(line ? pad + line.replace(/\n/g, `\n${pad}`) : '');
        if (b.has_children && depth < MAX_DEPTH && b.type !== 'child_database') {
          await this.blocks(bare(b.id), depth + 1, out, refs, budget);
        }
      }
      cursor = res.has_more ? res.next_cursor : undefined;
    } while (cursor);
  }

  private async table(id: string, pad: string, out: string[], linked: Map<string, string>) {
    const res: any = await this.get(`/v1/blocks/${id}/children?page_size=100`);
    for (const row of res.results ?? []) {
      const cells: any[][] = row.table_row?.cells ?? [];
      out.push(`${pad}| ${cells.map((c) => text(c, linked).replace(/\|/g, '/')).join(' | ')} |`);
    }
  }

  private async get(path: string, retried = false): Promise<unknown> {
    const fetchFn = this.opts.fetchFn ?? fetch;
    const root = (this.opts.apiRoot ?? 'https://api.notion.com').replace(/\/$/, '');
    const r = await fetchFn(root + path, {
      method: 'GET',
      headers: { Authorization: `Bearer ${this.opts.token}`, 'Notion-Version': NOTION_VERSION },
      signal: AbortSignal.timeout(15_000),
    });
    if (r.status === 429 && !retried) {
      const wait = Math.min(Number(r.headers.get('retry-after') ?? 1) || 1, 5);
      await new Promise((ok) => setTimeout(ok, wait * 1000));
      return this.get(path, true);
    }
    if (!r.ok) {
      let msg = '';
      try {
        msg = String(((await r.json()) as any)?.message ?? '');
      } catch {
        /* not JSON */
      }
      // The token never appears in the message: it is only in a header.
      throw new NotionError(r.status, `Notion ${r.status}${msg ? `: ${redact(msg)}` : ''}`);
    }
    return r.json();
  }
}

function friendly(e: any): string {
  if (e instanceof NotionError) {
    if (e.status === 401) return 'Notion refused the token (401): check NOTION_TOKEN';
    if (e.status === 404) return 'Notion cannot find that page (404): connect the integration to the VPN Infrastructure page (··· → Connections) or check the id';
    if (e.status === 403) return 'the Notion integration lacks "Read content" (403)';
  }
  return e?.message ?? String(e);
}

function pageTitle(meta: any): string {
  for (const v of Object.values(meta?.properties ?? {}) as any[]) {
    if (v?.type === 'title') return text(v.title ?? [], new Map()) || 'Untitled';
  }
  return 'Untitled';
}

/** Rich text → plain text; page mentions are collected so the model can open them. */
export function text(rich: any[], linked: Map<string, string>): string {
  return (rich ?? [])
    .map((t: any) => {
      const plain = String(t?.plain_text ?? '');
      if (t?.type === 'mention' && t.mention?.type === 'page' && t.mention.page?.id) {
        linked.set(String(t.mention.page.id).replace(/-/g, ''), plain || 'page');
        return `[page: ${plain || 'untitled'}]`;
      }
      if (t?.type === 'mention' && t.mention?.type === 'date') {
        const d = t.mention.date;
        return d?.end ? `${d.start} → ${d.end}` : String(d?.start ?? plain);
      }
      if (t?.annotations?.code) return `\`${plain}\``;
      if (t?.href && !plain.includes(t.href)) return `${plain} (${t.href})`;
      return plain;
    })
    .join('');
}

/** One block as one or more lines; null = nothing to show (its children may still follow). */
export function blockLine(b: any, numbered: number, linked: Map<string, string>): string | null {
  const v = b?.[b?.type] ?? {};
  const rt = () => text(v.rich_text ?? [], linked);
  switch (b?.type) {
    case 'paragraph':
      return rt();
    case 'heading_1':
      return `# ${rt()}`;
    case 'heading_2':
      return `## ${rt()}`;
    case 'heading_3':
      return `### ${rt()}`;
    case 'bulleted_list_item':
      return `- ${rt()}`;
    case 'numbered_list_item':
      return `${numbered}. ${rt()}`;
    case 'to_do':
      return `[${v.checked ? 'x' : ' '}] ${rt()}`;
    case 'toggle':
      return `▸ ${rt()}`;
    case 'quote':
      return `> ${rt()}`;
    case 'callout':
      return `${v.icon?.emoji ? `${v.icon.emoji} ` : ''}${rt()}`;
    case 'code':
      return `\`\`\`${v.language ?? ''}\n${rt()}\n\`\`\``;
    case 'equation':
      return String(v.expression ?? '');
    case 'divider':
      return '---';
    case 'child_database':
      return `[database: ${v.title ?? 'untitled'}]`;
    case 'bookmark':
    case 'embed':
    case 'link_preview':
      return String(v.url ?? '');
    case 'link_to_page': {
      const id = v.page_id ?? v.database_id;
      if (v.page_id) linked.set(String(id).replace(/-/g, ''), 'linked page');
      return '[link to page]';
    }
    case 'image':
    case 'file':
    case 'pdf':
    case 'video':
      return `[${b.type}${v.caption?.length ? `: ${text(v.caption, linked)}` : ''}]`;
    case 'table_of_contents':
    case 'breadcrumb':
    case 'column_list':
    case 'column':
    case 'synced_block':
      return null;
    default:
      return `[${b?.type ?? 'block'}]`;
  }
}

/** Notion ids come with dashes; one spelling everywhere (and in the parent cache). */
function bare(id: unknown): string {
  return String(id).replace(/-/g, '').toLowerCase();
}

function clip(s: string, n: number): string {
  const t = s.trim();
  return t.length > n ? t.slice(0, n) + '…' : t;
}
