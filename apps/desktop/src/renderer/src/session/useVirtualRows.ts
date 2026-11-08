import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { KindMeans, parts, positions, segments, windowOf, type Part } from "./virtual";

// A long conversation draws the rows within a screen of what is in sight, and stands in for the
// rest with space (docs/desktop-performance.md §7). The rows stay in the list's flow, so
// following the end, keeping the place while earlier history comes in, and the browser's own
// scroll anchoring work as they do with every row drawn.

/** A row of the list: its key (stable while it is written and after) and its kind, for guessing its height. */
export type VirtualRow = { key: string; kind: string };

export type VirtualRows = {
  /** What to draw, in order: space for rows not drawn, then rows (indexes into the rows given). */
  parts: Part[];
  /** The ref for a row's own container, to measure it. */
  measure: (key: string) => (el: HTMLElement | null) => (() => void) | undefined;
  /**
   * Brings a row into view, drawing it first if it is not. False when no row has that key (it is in history
   * not fetched yet). Far away it goes at once, then the rows around it are drawn; `then` gets its element.
   */
  scrollToKey: (key: string, o?: { block?: ScrollLogicalPosition; smooth?: boolean; then?: (el: HTMLElement) => void }) => boolean;
};

/** Rows drawn before the first layout says what is in sight: the last ones, where a conversation opens. */
const FIRST = 24;
/** The window moves by whole steps of this many rows, not at every row. */
const STEP = 8;
/** The height of a row of a kind nothing has measured or guessed. */
const ROW = 80;

/** The window, by the keys of its first and last rows: earlier history coming in shifts every index, not the keys. */
type Win = { first: string; last: string; toEnd: boolean };
type Range = readonly [string, string];

/**
 * The rows to draw of `rows`, laid in `list` inside `scroller` (both the elements themselves, from callback refs:
 * a list's effects run before its parent's ref is set). `initial`: a guess of each kind's height until some are measured.
 */
export function useVirtualRows({ scroller, list, rows, initial }: {
  scroller: HTMLElement | null;
  list: HTMLElement | null;
  rows: readonly VirtualRow[];
  initial: Readonly<Record<string, number>>;
}): VirtualRows {
  const index = useMemo(() => new Map(rows.map((r, i) => [r.key, i])), [rows]);
  const latest = useRef({ rows, index, initial, scroller, list });
  latest.current = { rows, index, initial, scroller, list };
  const kept = useRef({
    /** Each row's height as measured last, by key; kept while the row is not drawn. */
    measured: new Map<string, number>(),
    means: new KindMeans(),
    /** The averages as they were when the window last moved: what rows not measured are taken to be. */
    guesses: new Map<string, number>(),
    elements: new Map<string, HTMLElement>(),
    keys: new WeakMap<Element, string>(),
    refs: new Map<string, (el: HTMLElement | null) => (() => void) | undefined>(),
    /** The window as last drawn. */
    drawn: [0, 0] as [number, number],
    gap: 0,
    jump: null as { key: string; block: ScrollLogicalPosition; smooth: boolean; then?: (el: HTMLElement) => void } | null,
    unpin: undefined as ReturnType<typeof setTimeout> | undefined,
    /**
     * A window placed but not drawn yet. Placing takes new guesses for the rows not measured, which the page shows
     * only once that window is drawn: placed again before then (a scroll in between), the page and the guesses
     * disagree on where every row is, and the window would land screens away for a frame.
     */
    placed: false,
  }).current;
  const [gap, setGap] = useState(0);
  kept.gap = gap;
  const [win, setWin] = useState<Win | null>(null);
  /** Rows kept drawn wherever the window is: the selection's, the focused one's, one being brought into view. */
  const [pins, setPins] = useState<Readonly<Record<string, Range>>>({});

  const heightOf = (r: VirtualRow) => kept.measured.get(r.key) ?? kept.guesses.get(r.kind) ?? latest.current.initial[r.kind] ?? ROW;

  // The window this render draws: the one placed last, else the last rows.
  const n = rows.length;
  const P = positions(rows.map(heightOf), gap);
  const f = win ? index.get(win.first) : undefined;
  const l = win ? index.get(win.last) : undefined;
  const [first, end] = win && f !== undefined && (win.toEnd || l !== undefined) ? [f, win.toEnd ? n : Math.max(f, l!) + 1] : [Math.max(0, n - FIRST), n];
  const pinned = Object.values(pins).flatMap(([a, b]): [number, number][] => {
    const i = index.get(a);
    const j = index.get(b);
    return i === undefined || j === undefined ? [] : [[Math.min(i, j), Math.max(i, j) + 1]];
  });
  const drawnParts = parts(P, gap, segments([[first, end], ...pinned], n));

  /**
   * Where the window should be for what is in sight now. It stays while it still reaches half a screen past what is
   * in sight each way and no row of it is more than three screens away; otherwise it is placed a screen each way of
   * what is in sight. A window placed again only when it must does not chase the small moves its own drawing makes
   * (measured rows replacing guesses), and does not keep growing while rows come in at the end.
   */
  const place = () => {
    const { rows, scroller, list } = latest.current;
    if (!scroller || !list || kept.placed) return;
    const n = rows.length;
    if (n === 0) return;
    const view = scroller.clientHeight;
    const g = kept.gap;
    // What is in sight, in the list's own terms.
    const top = scroller.getBoundingClientRect().top + scroller.clientTop - list.getBoundingClientRect().top;
    const bottom = top + view;
    const P = positions(rows.map(heightOf), g);
    const [df, de] = kept.drawn;
    if (de > df && de <= n) {
      const reaches = (df === 0 || P[df] <= top - view / 2) && (de === n || P[de] - g >= bottom + view / 2);
      const near = P[df + 1] - g >= top - 3 * view && P[de - 1] <= bottom + 3 * view;
      if (reaches && near) return;
    }
    const [a, b] = windowOf(P, g, top - view, bottom + view, STEP);
    if (a === df && b === de) return;
    kept.guesses = kept.means.snapshot();
    kept.placed = true;
    setWin({ first: rows[a].key, last: rows[b - 1].key, toEnd: b === n });
  };
  const placeRef = useRef(place);
  placeRef.current = place;
  // After a render or a row's new size: once, in the next frame. Placing it at once could chain render to render.
  const frame = useRef(0);
  const schedule = useCallback(() => {
    if (frame.current) return;
    frame.current = requestAnimationFrame(() => {
      frame.current = 0;
      placeRef.current();
    });
  }, []);

  const pin = useCallback(
    (name: string, range: Range | null) =>
      setPins((p) => {
        const was = p[name];
        if (range ? was?.[0] === range[0] && was?.[1] === range[1] : !was) return p;
        const next = { ...p };
        if (range) next[name] = range;
        else delete next[name];
        return next;
      }),
    [],
  );

  /** Brings the row being jumped to into view, once it is drawn. */
  const land = (el: HTMLElement) => {
    const jump = kept.jump;
    const scroller = latest.current.scroller;
    if (!jump || !scroller) return;
    kept.jump = null;
    const far = Math.abs(el.getBoundingClientRect().top - scroller.getBoundingClientRect().top) > 3 * scroller.clientHeight;
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    el.scrollIntoView({ block: jump.block, behavior: jump.smooth && !far && !still ? "smooth" : "auto" });
    jump.then?.(el);
    clearTimeout(kept.unpin);
    kept.unpin = setTimeout(() => pin("jump", null), 1500);
  };
  const landRef = useRef(land);
  landRef.current = land;

  // After each render: what was drawn, where the window should go (rows came or changed), a jump that waited for its row.
  useLayoutEffect(() => {
    kept.drawn = [first, end];
    kept.placed = false;
    schedule();
    const jump = kept.jump;
    const el = jump && kept.elements.get(jump.key);
    if (el) landRef.current(el);
  });

  useLayoutEffect(() => {
    if (list) setGap(parseFloat(getComputedStyle(list).rowGap) || 0);
  }, [list]);

  useLayoutEffect(() => {
    if (!scroller) return;
    const on = () => placeRef.current();
    scroller.addEventListener("scroll", on, { passive: true });
    const ro = new ResizeObserver(on);
    ro.observe(scroller);
    return () => {
      scroller.removeEventListener("scroll", on);
      ro.disconnect();
    };
  }, [scroller]);

  // One observer measures every drawn row; a change moves the window if it needs to, and draws nothing else.
  const observer = useMemo(
    () =>
      new ResizeObserver((entries) => {
        let changed = false;
        for (const e of entries) {
          const key = kept.keys.get(e.target);
          if (key === undefined) continue;
          const height = e.borderBoxSize?.[0]?.blockSize ?? e.target.getBoundingClientRect().height;
          if (kept.measured.get(key) === height) continue;
          kept.measured.set(key, height);
          const i = latest.current.index.get(key);
          if (i !== undefined) kept.means.set(key, latest.current.rows[i].kind, height);
          changed = true;
        }
        if (changed) schedule();
      }),
    [schedule],
  );
  useEffect(
    () => () => {
      observer.disconnect();
      clearTimeout(kept.unpin);
      cancelAnimationFrame(frame.current);
    },
    [observer],
  );

  const measure = useCallback(
    (key: string) => {
      let ref = kept.refs.get(key);
      if (!ref) {
        ref = (el: HTMLElement | null) => {
          if (!el) return undefined;
          kept.elements.set(key, el);
          kept.keys.set(el, key);
          observer.observe(el);
          return () => {
            observer.unobserve(el);
            if (kept.elements.get(key) === el) kept.elements.delete(key);
          };
        };
        kept.refs.set(key, ref);
      }
      return ref;
    },
    [observer],
  );
  // Refs of rows that are gone (a work row became part of another) go too.
  useEffect(() => {
    for (const key of kept.refs.keys()) if (!index.has(key)) kept.refs.delete(key);
  }, [index]);

  // The selection's rows and the focused row stay drawn: a drag or a key press is not cut off by the window moving.
  useEffect(() => {
    if (!list) return;
    const rowOf = (node: Node | null): string | undefined => {
      for (let el = node instanceof Element ? node : (node?.parentElement ?? null); el && el !== list; el = el.parentElement) {
        const key = kept.keys.get(el);
        if (key !== undefined) return key;
      }
      return undefined;
    };
    const selection = () => {
      const s = document.getSelection();
      const a = s && !s.isCollapsed ? rowOf(s.anchorNode) : undefined;
      const b = s && !s.isCollapsed ? rowOf(s.focusNode) : undefined;
      pin("selection", a === undefined && b === undefined ? null : [a ?? b!, b ?? a!]);
    };
    const focusin = (e: FocusEvent) => {
      const key = rowOf(e.target as Node);
      pin("focus", key === undefined ? null : [key, key]);
    };
    const focusout = (e: FocusEvent) => {
      if (rowOf(e.relatedTarget as Node | null) === undefined) pin("focus", null);
    };
    document.addEventListener("selectionchange", selection);
    list.addEventListener("focusin", focusin);
    list.addEventListener("focusout", focusout);
    return () => {
      document.removeEventListener("selectionchange", selection);
      list.removeEventListener("focusin", focusin);
      list.removeEventListener("focusout", focusout);
    };
  }, [list, pin]);

  const scrollToKey = useCallback<VirtualRows["scrollToKey"]>(
    (key, o = {}) => {
      if (!latest.current.index.has(key)) return false;
      kept.jump = { key, block: o.block ?? "center", smooth: o.smooth ?? true, then: o.then };
      clearTimeout(kept.unpin);
      pin("jump", [key, key]);
      const el = kept.elements.get(key);
      if (el) landRef.current(el);
      return true;
    },
    [pin],
  );

  return { parts: drawnParts, measure, scrollToKey };
}
