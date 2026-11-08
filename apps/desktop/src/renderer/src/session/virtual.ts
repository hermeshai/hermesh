// A conversation draws the rows near what is in sight (docs/desktop-performance.md §7): where
// each row starts, which rows a stretch of the list covers, the window to draw, and the space that
// stands in for the rows not drawn. Rows sit in a flex column with a gap; nothing here reads the page.

/**
 * Where each row starts from the list's top: P[i] for row i, P[n] for one past the last. A row
 * takes its height and the gap after it, so P[j] - P[i] is how far rows i..j-1 reach.
 */
export function positions(heights: ArrayLike<number>, gap: number): Float64Array {
  const P = new Float64Array(heights.length + 1);
  for (let i = 0; i < heights.length; i++) P[i + 1] = P[i] + heights[i] + gap;
  return P;
}

/** The first index j in [lo, P.length) where `past(P[j])` holds, else P.length (P only grows). */
function search(P: Float64Array, lo: number, past: (p: number) => boolean): number {
  let hi = P.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (past(P[mid])) hi = mid;
    else lo = mid + 1;
  }
  return lo;
}

/** The rows a stretch [top, bottom) of the list overlaps: [first, end). A gap between rows belongs to no row. */
export function rowsIn(P: Float64Array, gap: number, top: number, bottom: number): [number, number] {
  const n = P.length - 1;
  if (n <= 0 || bottom <= top) return [0, 0];
  // The first row whose bottom, P[i + 1] - gap, is below top.
  const first = search(P, 1, (p) => p - gap > top) - 1;
  // The first row that starts at or past bottom.
  const end = Math.min(n, search(P, first, (p) => p >= bottom));
  return [Math.min(first, n), Math.max(Math.min(first, n), end)];
}

/**
 * The rows to draw for a stretch of the list: those it overlaps, widened to whole steps of `step`
 * rows so the window moves now and then rather than at every row. Never empty while there are rows.
 */
export function windowOf(P: Float64Array, gap: number, top: number, bottom: number, step: number): [number, number] {
  const n = P.length - 1;
  if (n <= 0) return [0, 0];
  const [a, b] = rowsIn(P, gap, top, bottom);
  let first = Math.floor(a / step) * step;
  let end = Math.min(n, Math.ceil(b / step) * step);
  if (end <= first) {
    // Out past the rows, above or below: the nearest step of them.
    first = Math.min(first, Math.max(0, n - step));
    end = Math.min(n, first + step);
  }
  return [Math.max(0, first), end];
}

/** Ranges of rows in order, apart: those that overlap or touch are one, empty and outside ones go. */
export function segments(ranges: readonly (readonly [number, number])[], n: number): [number, number][] {
  const sorted = ranges
    .map(([a, b]) => [Math.max(0, a), Math.min(n, b)] as [number, number])
    .filter(([a, b]) => b > a)
    .sort((x, y) => x[0] - y[0]);
  const out: [number, number][] = [];
  for (const [a, b] of sorted) {
    const last = out.at(-1);
    if (last && a <= last[1]) last[1] = Math.max(last[1], b);
    else out.push([a, b]);
  }
  return out;
}

/**
 * What the list draws, in order: a space that stands in for rows not drawn, then a range of rows, and so on.
 * A space is one flex item among the rows, so it leaves out the gap after it: with that gap it reaches as
 * far as the rows it stands for. `at` is the first row it stands for.
 */
export type Part = { space: number; at: number } | { from: number; to: number };

export function parts(P: Float64Array, gap: number, segs: readonly (readonly [number, number])[]): Part[] {
  const n = P.length - 1;
  const out: Part[] = [];
  let at = 0;
  for (const [from, to] of segs) {
    if (from > at) out.push({ space: P[from] - P[at] - gap, at });
    out.push({ from, to });
    at = to;
  }
  if (at < n) out.push({ space: P[n] - P[at] - gap, at });
  return out;
}

/**
 * How tall rows of each kind turned out, on average, for rows not measured yet. A row measured again
 * (it grew while written, or the width changed) replaces what it counted for before.
 */
export class KindMeans {
  private readonly rows = new Map<string, { kind: string; height: number }>();
  private readonly sums = new Map<string, { total: number; count: number }>();

  set(key: string, kind: string, height: number) {
    const was = this.rows.get(key);
    if (was) {
      const s = this.sums.get(was.kind)!;
      s.total -= was.height;
      s.count -= 1;
    }
    this.rows.set(key, { kind, height });
    const s = this.sums.get(kind) ?? { total: 0, count: 0 };
    s.total += height;
    s.count += 1;
    this.sums.set(kind, s);
  }

  /** The kind's average, else `fallback`. */
  of(kind: string, fallback: number): number {
    const s = this.sums.get(kind);
    return s && s.count > 0 ? s.total / s.count : fallback;
  }

  /** The averages as they are now, by kind. */
  snapshot(): Map<string, number> {
    const out = new Map<string, number>();
    for (const [kind, s] of this.sums) if (s.count > 0) out.set(kind, s.total / s.count);
    return out;
  }
}
