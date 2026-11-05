import { Resvg } from '@resvg/resvg-js';
import { Health } from '../contract';
import { HubService } from '../hub.service';
import { loadFonts } from './fonts';
import { autoLayout } from './shared/layout';
import { cardSeriesKeys, renderBoardSvg, Theme } from './render';
import { BoardSelection, resolveSelection } from './select';

/**
 * Pictures of the board for Telegram and the API: the snapshot at a moment,
 * the board's positions (saved layout, else auto-layout), the last hour of
 * card sparklines, drawn by render.ts and rasterised by resvg with the
 * board's own fonts. Reads only.
 */

export interface ImageOptions {
  at: number | null;
  theme?: Theme;
  context?: boolean;
  labels?: boolean;
  /** 'photo': sized for Telegram's photo viewer (max side 2560). 'file': full resolution (scale 2, max side 6000). */
  quality?: 'photo' | 'file';
}

export interface BoardImage {
  png: Buffer;
  svg: string;
  width: number;
  height: number;
  title: string;
  status: Health;
  at: string;
  nodes: string[];
  links: string[];
  /** Whether it reads well as a Telegram photo; when false send it as a file. */
  fitsPhoto: boolean;
}

/** Telegram photos are shown at most 2560 px on the long side. */
export const PHOTO_MAX_SIDE = 2560;
const FILE_MAX_SIDE = 6000;

export class BoardImages {
  constructor(private readonly hub: HubService) {}

  async render(sel: BoardSelection, o: ImageOptions): Promise<BoardImage> {
    const { snap, bundle } = await this.hub.snapshotAt(o.at);
    const saved = await this.hub.db.layout();
    const auto = autoLayout(bundle);
    const positions: Record<string, { x: number; y: number }> = {};
    for (const n of bundle.nodes) positions[n.id] = saved[n.id] ?? auto[n.id]; // exactly as the board
    const resolved = resolveSelection(bundle, snap, sel);

    const sparks: Record<string, (number | null)[]> = {};
    const keys = cardSeriesKeys(bundle, resolved.nodes);
    if (keys.length) {
      const end = o.at ?? Date.now();
      try {
        const series = await this.hub.tsdb.range(keys, end - 3_600_000, end, 60_000);
        for (const k of keys) sparks[k] = (series.get(k) ?? []).map((p) => p[1]);
      } catch {
        /* no sparklines is fine; the numbers are still on the cards */
      }
    }

    const fonts = loadFonts();
    const r = renderBoardSvg({ bundle, snap, positions, selection: resolved, theme: o.theme, sparks, context: o.context, labels: o.labels, measure: fonts?.measure });
    const side = Math.max(r.width, r.height);
    const photoScale = Math.min(2, PHOTO_MAX_SIDE / side);
    const scale = o.quality === 'file' ? Math.min(2, FILE_MAX_SIDE / side) : photoScale;
    const resvg = new Resvg(r.svg, {
      fitTo: { mode: 'zoom', value: scale },
      font: fonts ? { fontFiles: fonts.files, loadSystemFonts: false, defaultFontFamily: 'Geist' } : { loadSystemFonts: true },
      shapeRendering: 2,
      textRendering: 1,
    });
    const img = resvg.render();
    return {
      png: img.asPng(),
      svg: r.svg,
      width: img.width,
      height: img.height,
      title: resolved.title,
      status: r.status,
      at: snap.at,
      nodes: r.nodes,
      links: r.links,
      // Below 1x the 11 px labels get too small to read once Telegram recompresses the photo.
      fitsPhoto: photoScale >= 1,
    };
  }
}

/** Parse the API/tool form of a selection (comma lists or arrays). */
export function selectionFrom(q: Record<string, unknown>): BoardSelection {
  const list = (v: unknown) => (Array.isArray(v) ? v.map(String) : typeof v === 'string' && v.trim() ? v.split(',') : []).map((s) => s.trim()).filter(Boolean);
  const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v.trim() : undefined);
  return { route: str(q.route), from: str(q.from), to: str(q.to), via: list(q.via), nodes: list(q.nodes), links: list(q.links) };
}
