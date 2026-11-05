import * as fs from 'fs';
import * as path from 'path';

/**
 * The board's fonts (Geist, Geist Mono; SIL OFL, see assets/fonts/LICENSE.txt)
 * for the image renderer: the files for resvg, and advance widths read from
 * the same files so text can be truncated and pills sized exactly as they
 * will be drawn. Only cmap + hmtx are read; kerning is ignored (a pixel or
 * two on a label).
 */

export type FontFace = 'sans' | 'mono';
export type Measure = (text: string, size: number, face?: FontFace, weight?: number) => number;

const FILES: { face: FontFace; weight: number; file: string }[] = [
  { face: 'sans', weight: 400, file: 'Geist-Regular.ttf' },
  { face: 'sans', weight: 500, file: 'Geist-Medium.ttf' },
  { face: 'sans', weight: 600, file: 'Geist-SemiBold.ttf' },
  { face: 'mono', weight: 400, file: 'GeistMono-Regular.ttf' },
  { face: 'mono', weight: 500, file: 'GeistMono-Medium.ttf' },
];

export interface BoardFonts {
  files: string[];
  measure: Measure;
}

let cached: BoardFonts | null = null;

/** Finds assets/fonts next to dist/ (image) or src/ (dev); null when missing. */
export function loadFonts(dir = defaultDir()): BoardFonts | null {
  if (cached) return cached;
  if (!dir) return null;
  const metrics = new Map<string, Advances>();
  const files: string[] = [];
  for (const f of FILES) {
    const p = path.join(dir, f.file);
    if (!fs.existsSync(p)) continue;
    files.push(p);
    try {
      metrics.set(`${f.face}:${f.weight}`, readAdvances(fs.readFileSync(p)));
    } catch {
      /* a font we cannot parse still renders; measuring falls back to estimates */
    }
  }
  if (!files.length) return null;
  const pick = (face: FontFace, weight: number) => {
    const cands = FILES.filter((f) => f.face === face && metrics.has(`${f.face}:${f.weight}`)).sort((a, z) => Math.abs(a.weight - weight) - Math.abs(z.weight - weight));
    return cands.length ? metrics.get(`${cands[0].face}:${cands[0].weight}`)! : null;
  };
  const measure: Measure = (text, size, face = 'sans', weight = 400) => {
    const m = pick(face, weight);
    if (!m) return estimate(text, size, face, weight);
    let w = 0;
    for (const ch of text) w += m.advance(ch.codePointAt(0)!);
    return (w / m.unitsPerEm) * size;
  };
  cached = { files, measure };
  return cached;
}

function defaultDir(): string | null {
  for (const d of [path.join(__dirname, '..', '..', 'assets', 'fonts'), path.join(process.cwd(), 'assets', 'fonts'), path.join(process.cwd(), 'apps', 'hub', 'assets', 'fonts')]) {
    if (fs.existsSync(d)) return d;
  }
  return null;
}

/** Width estimate without font files (tests, or a broken font). */
export const estimate: Measure = (text, size, face = 'sans', weight = 400) => {
  if (face === 'mono') return [...text].length * 0.6 * size;
  let w = 0;
  for (const ch of text) {
    if (/[ilI.,:;'!|]/.test(ch)) w += 0.26;
    else if (/[mwMW]/.test(ch)) w += 0.82;
    else if (/[A-Z]/.test(ch)) w += 0.66;
    else if (/[0-9]/.test(ch)) w += 0.6;
    else if (ch === ' ') w += 0.28;
    else w += 0.54;
  }
  return w * size * (weight >= 600 ? 1.04 : 1);
};

interface Advances {
  unitsPerEm: number;
  advance: (cp: number) => number;
}

/** Reads unitsPerEm, the Unicode cmap (format 4 or 12) and hmtx of a TrueType font. */
export function readAdvances(buf: Buffer): Advances {
  const u16 = (o: number) => buf.readUInt16BE(o);
  const u32 = (o: number) => buf.readUInt32BE(o);
  const tables = new Map<string, number>();
  const numTables = u16(4);
  for (let i = 0; i < numTables; i++) {
    const r = 12 + i * 16;
    tables.set(buf.toString('latin1', r, r + 4), u32(r + 8));
  }
  const head = tables.get('head');
  const hhea = tables.get('hhea');
  const hmtx = tables.get('hmtx');
  const cmap = tables.get('cmap');
  if (head === undefined || hhea === undefined || hmtx === undefined || cmap === undefined) throw new Error('not a TrueType font');
  const unitsPerEm = u16(head + 18);
  const numH = u16(hhea + 34);
  const adv = (g: number) => u16(hmtx + 4 * Math.min(g, numH - 1));

  // Prefer a full-Unicode subtable (3,10 / 0,4 format 12), then BMP (3,1 / 0,3 format 4).
  let sub = -1;
  let best = -1;
  for (let i = 0; i < u16(cmap + 2); i++) {
    const r = cmap + 4 + i * 8;
    const pid = u16(r);
    const eid = u16(r + 2);
    const off = cmap + u32(r + 4);
    const fmt = u16(off);
    const score = fmt === 12 && (pid === 3 || pid === 0) ? 2 : fmt === 4 && ((pid === 3 && eid === 1) || pid === 0) ? 1 : 0;
    if (score > best) {
      best = score;
      sub = off;
    }
  }
  if (best <= 0) throw new Error('no Unicode cmap');
  let glyph: (cp: number) => number;
  if (u16(sub) === 12) {
    const n = u32(sub + 12);
    glyph = (cp) => {
      let lo = 0;
      let hi = n - 1;
      while (lo <= hi) {
        const mid = (lo + hi) >> 1;
        const g = sub + 16 + mid * 12;
        if (cp < u32(g)) hi = mid - 1;
        else if (cp > u32(g + 4)) lo = mid + 1;
        else return u32(g + 8) + (cp - u32(g));
      }
      return 0;
    };
  } else {
    const segX2 = u16(sub + 6);
    const ends = sub + 14;
    const starts = ends + segX2 + 2;
    const deltas = starts + segX2;
    const ranges = deltas + segX2;
    glyph = (cp) => {
      if (cp > 0xffff) return 0;
      for (let i = 0; i < segX2 / 2; i++) {
        if (u16(ends + 2 * i) < cp) continue;
        const start = u16(starts + 2 * i);
        if (start > cp) return 0;
        const delta = buf.readInt16BE(deltas + 2 * i);
        const ro = u16(ranges + 2 * i);
        if (!ro) return (cp + delta) & 0xffff;
        const g = u16(ranges + 2 * i + ro + 2 * (cp - start));
        return g ? (g + delta) & 0xffff : 0;
      }
      return 0;
    };
  }
  const cache = new Map<number, number>();
  return {
    unitsPerEm,
    advance: (cp) => {
      let v = cache.get(cp);
      if (v === undefined) {
        const g = glyph(cp);
        v = g ? adv(g) : unitsPerEm * 0.55;
        cache.set(cp, v);
      }
      return v;
    },
  };
}
