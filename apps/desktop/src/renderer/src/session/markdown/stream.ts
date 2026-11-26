import { useEffect, useRef, useState } from "react";
import type { Element, ElementContent, Root as HtmlRoot } from "hast";

/** Duration of one row's left-to-right silk mask wipe (must match .md-wipe in markdown.css). */
export const WIPE_MS = 680;
/** Target visual width of one streamed prose row (~34 CJK chars or ~68 ASCII chars). */
export const ROW_UNITS = 68;
const ROW_CADENCE_MS = 135;
const FAST_CADENCE_MS = 60;
const GATE_WAIT_MS = 175;

const wideChar = (code: number) =>
  (code >= 0x2e80 && code <= 0x9fff) ||
  (code >= 0xac00 && code <= 0xd7af) ||
  (code >= 0xf900 && code <= 0xfaff) ||
  (code >= 0xfe30 && code <= 0xfe6f) ||
  (code >= 0xff01 && code <= 0xff60) ||
  code >= 0x20000;

const breakPunct = new Set([..."，。、；：！？）》」』】,.!?;:)"]);

/**
 * Finds the end offset in `text` of the next full visual row starting at `from`.
 * Returns `null` while `text.slice(from)` is shorter than one row and has no newline,
 * unless `flushTail` is true (gate timeout elapsed).
 */
export function nextRowEnd(text: string, from: number, flushTail: boolean): number | null {
  if (from >= text.length) return null;
  const rem = text.slice(from);

  // Skip leading blank newlines so a wave carries content, not an empty newline alone.
  let lead = 0;
  while (lead < rem.length && (rem[lead] === "\n" || rem[lead] === "\r")) lead++;
  if (lead >= rem.length) return flushTail ? text.length : null;

  // 1. A newline ends a line immediately (headings, list items, table rows, code lines).
  const nl = rem.indexOf("\n", lead);
  if (nl >= 0) return from + nl + 1;

  // 2. Measure visible width units while tracking open inline constructs (`code`, [link](url)).
  let units = 0;
  let inCode = false;
  let inLinkUrl = false;
  let lastGoodCut = 0;
  let reachedAt = -1;

  for (let i = lead; i < rem.length; ) {
    const ch = rem[i]!;
    if (ch === "`") {
      inCode = !inCode;
      i++;
      if (!inCode && units >= ROW_UNITS - 18) lastGoodCut = i;
      continue;
    }
    if (!inCode) {
      if (inLinkUrl) {
        i++;
        if (ch === ")") {
          inLinkUrl = false;
          if (units >= ROW_UNITS - 18) lastGoodCut = i;
        }
        continue;
      }
      if (ch === "]" && rem[i + 1] === "(") {
        inLinkUrl = true;
        i += 2;
        continue;
      }
      if (ch === "*" || ch === "~" || ch === "[") {
        i++;
        continue;
      }
    }

    const cp = rem.codePointAt(i)!;
    const step = cp > 0xffff ? 2 : 1;
    units += wideChar(cp) ? 2 : 1;
    i += step;

    if (!inCode && !inLinkUrl) {
      if (ch === " " || ch === "\t" || breakPunct.has(ch)) {
        lastGoodCut = i;
      } else if (wideChar(cp) && units >= ROW_UNITS - 16) {
        // Any CJK character boundary outside inline code/links is a clean break if no punctuation follows right after.
        const next = rem[i];
        if (!next || !breakPunct.has(next)) lastGoodCut = i;
      }
    }

    if (units >= ROW_UNITS && !inCode && !inLinkUrl && reachedAt < 0) {
      // Avoid cutting between paired '**' or mid-ASCII word when an earlier break exists.
      if (rem[i - 1] === "*" && rem[i] === "*") continue;
      const midWord = /[A-Za-z0-9_]/.test(rem[i - 1] ?? "") && /[A-Za-z0-9_]/.test(rem[i] ?? "");
      if (!midWord) {
        reachedAt = lastGoodCut >= lead + 8 && i - lastGoodCut <= 16 ? lastGoodCut : i;
        break;
      }
      if (lastGoodCut >= lead + 8) {
        reachedAt = lastGoodCut;
        break;
      }
    }
  }

  if (reachedAt > 0) {
    // Include an immediately following closing '**' or punctuation so we don't strand a lone marker.
    while (reachedAt < rem.length && rem[reachedAt] === "*") reachedAt++;
    if (reachedAt < rem.length && breakPunct.has(rem[reachedAt]!)) reachedAt++;
    return from + reachedAt;
  }

  if (!flushTail) return null;

  // Timeout flush: hold back only if all we have so far is raw opening syntax with no words yet.
  const trimmed = rem.slice(lead).trim();
  if (trimmed === "" || /^([*`~|#>-]+|\|[^|\n]*)$/.test(trimmed)) return null;
  let end = rem.length;
  if (end >= 2 && rem[end - 1] === "*" && rem[end - 2] !== "*") end--;
  return end > lead ? from + end : null;
}

const prefersReducedMotion = () =>
  typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

/**
 * Releases a streaming reply one full visual row at a time so each row wipes left-to-right
 * as a silk wave; flushes synchronously the instant `streaming` turns false.
 */
export function useStreamText(text: string, streaming: boolean): { shown: string; animate: boolean } {
  const reduced = prefersReducedMotion();
  const [shownLen, setShownLen] = useState(() => (streaming && !reduced ? (nextRowEnd(text, 0, false) ?? 0) : text.length));
  const [lingering, setLingering] = useState(false);
  const wasStreaming = useRef(streaming && !reduced);
  const lastReleaseAt = useRef(0);
  const prevText = useRef(text);

  if (!text.startsWith(prevText.current.slice(0, shownLen))) {
    // Whole-draft replacement (e.g. reconnect or mid-turn reset).
    prevText.current = text;
    const resetLen = streaming && !reduced ? (nextRowEnd(text, 0, false) ?? 0) : text.length;
    if (shownLen !== resetLen) setShownLen(resetLen);
  } else {
    prevText.current = text;
  }

  useEffect(() => {
    if (reduced) return;
    if (streaming) {
      wasStreaming.current = true;
      setLingering(true);
    } else if (wasStreaming.current) {
      wasStreaming.current = false;
      setShownLen(text.length);
      const done = setTimeout(() => setLingering(false), WIPE_MS + 60);
      return () => clearTimeout(done);
    }
  }, [streaming, reduced, text.length]);

  useEffect(() => {
    if (!streaming || reduced || shownLen >= text.length) return;
    const now = performance.now();
    const elapsed = now - lastReleaseAt.current;
    const cadence = text.length - shownLen > 260 ? FAST_CADENCE_MS : ROW_CADENCE_MS;
    const readyCut = nextRowEnd(text, shownLen, false);

    if (readyCut !== null && elapsed >= cadence) {
      lastReleaseAt.current = now;
      setShownLen(readyCut);
      return;
    }

    const waitMs = readyCut !== null ? Math.max(12, cadence - elapsed) : GATE_WAIT_MS;
    const timer = setTimeout(() => {
      const cut = nextRowEnd(text, shownLen, readyCut === null);
      if (cut !== null && cut > shownLen) {
        lastReleaseAt.current = performance.now();
        setShownLen(cut);
      }
    }, waitMs);
    return () => clearTimeout(timer);
  }, [text, streaming, reduced, shownLen]);

  const animate = !reduced && (streaming || lingering);
  const shown = streaming && !reduced ? text.slice(0, Math.min(shownLen, text.length)) : text;
  return { shown, animate };
}

export type Wave = { start: number; end: number; until: number };
export type SilkWipeState = { seen: number; waves: Wave[] };

const BLOCK_TAGS = new Set([
  "div", "section", "blockquote", "ul", "ol", "li", "dl", "dt", "dd",
  "details", "summary", "table", "thead", "tbody", "tr", "td", "th",
  "p", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "pre",
]);

const ATOMIC_INLINE_TAGS = new Set(["code", "math", "sup", "img", "input"]);

function hasClass(el: Element, cls: string): boolean {
  const c: unknown = el.properties?.className;
  if (Array.isArray(c)) return c.some((x) => String(x) === cls);
  return typeof c === "string" ? c.split(/\s+/).includes(cls) : false;
}

function isSkipped(el: Element): boolean {
  return el.tagName === "pre" || hasClass(el, "sr-only");
}

function isAtomicInline(el: Element): boolean {
  return ATOMIC_INLINE_TAGS.has(el.tagName) || hasClass(el, "katex");
}

function plainLength(node: ElementContent): number {
  if (node.type === "text") return node.value.length;
  if (node.type !== "element") return 0;
  if (isSkipped(node) || node.tagName === "br" || node.tagName === "hr" || node.tagName === "input") return 0;
  if (node.tagName === "img") return String(node.properties?.alt ?? "").length || 1;
  let sum = 0;
  for (const child of node.children) sum += plainLength(child);
  return sum;
}

function treeVisibleLength(root: HtmlRoot): number {
  let sum = 0;
  for (const child of root.children) {
    if (child.type === "text" || child.type === "element") sum += plainLength(child as ElementContent);
  }
  return sum;
}

function wipeSpan(children: ElementContent[]): Element {
  return { type: "element", tagName: "span", properties: { className: ["md-wipe"] }, children };
}

/**
 * Rehype plugin that wraps newly revealed hast inline runs into `<span class="md-wipe">` waves:
 * - Consecutive inline siblings belonging to the same wave merge into ONE span so the silk gradient
 *   sweeps continuously across text, bold, links and inline code pills.
 * - `<br>` stays outside spans so newline-separated lines wipe independently.
 * - `<code>` stays intact inside the span so `InlineCode` always receives a plain string child.
 * - Once all waves in the block expire, emits zero extra spans.
 */
export function silkWipe(options: { state: SilkWipeState; now?: () => number }) {
  return (tree: HtmlRoot) => {
    const { state } = options;
    const now = options.now ? options.now() : performance.now();
    const total = treeVisibleLength(tree);

    if (total < state.seen) {
      state.seen = total;
      state.waves = state.waves
        .filter((w) => w.start < total)
        .map((w) => (w.end > total ? { ...w, end: total } : w));
    } else if (total > state.seen) {
      state.waves.push({ start: state.seen, end: total, until: now + WIPE_MS });
      state.seen = total;
    }

    if (state.waves.length === 0) return;
    if (state.waves.every((w) => w.until <= now)) {
      state.waves = [];
      return;
    }

    const waves = state.waves;
    const waveIndexAt = (pos: number): number => {
      for (let i = 0; i < waves.length; i++) {
        if (pos >= waves[i]!.start && pos < waves[i]!.end) return i;
      }
      return -1;
    };

    let cursor = 0;

    const transformBlockChildren = (children: ElementContent[]): ElementContent[] => {
      const out: ElementContent[] = [];
      let bucket: ElementContent[] = [];
      let bucketWave = -1;

      const flushBucket = () => {
        if (bucket.length === 0) return;
        if (bucketWave >= 0) out.push(wipeSpan(bucket));
        else out.push(...bucket);
        bucket = [];
        bucketWave = -1;
      };

      const pushPiece = (node: ElementContent, waveIdx: number) => {
        if (waveIdx !== bucketWave) {
          flushBucket();
          bucketWave = waveIdx;
        }
        bucket.push(node);
      };

      for (const child of children) {
        if (child.type === "text") {
          const text = child.value;
          let pos = 0;
          while (pos < text.length) {
            const wIdx = waveIndexAt(cursor + pos);
            let nextEnd = text.length;
            if (wIdx >= 0) {
              nextEnd = Math.min(text.length, waves[wIdx]!.end - cursor);
            } else {
              for (const w of waves) {
                if (w.start > cursor + pos) {
                  nextEnd = Math.min(text.length, w.start - cursor);
                  break;
                }
              }
            }
            pushPiece({ type: "text", value: text.slice(pos, nextEnd) }, wIdx);
            pos = nextEnd;
          }
          cursor += text.length;
          continue;
        }

        if (child.type !== "element") {
          flushBucket();
          out.push(child);
          continue;
        }

        if (isSkipped(child)) {
          flushBucket();
          out.push(child);
          continue;
        }

        if (child.tagName === "br") {
          flushBucket();
          out.push(child);
          continue;
        }

        if (BLOCK_TAGS.has(child.tagName)) {
          flushBucket();
          child.children = transformBlockChildren(child.children);
          out.push(child);
          continue;
        }

        const len = plainLength(child);
        if (len === 0) {
          // Zero-length inline (e.g. task checkbox input): attach without shifting cursor.
          flushBucket();
          out.push(child);
          continue;
        }

        const startWave = waveIndexAt(cursor);
        const endWave = waveIndexAt(cursor + len - 1);

        if (isAtomicInline(child) || (startWave === endWave && (startWave >= 0 || waves.every((w) => w.end <= cursor || w.start >= cursor + len)))) {
          // Entire inline element sits within one wave (or outside all waves): keep whole!
          pushPiece(child, startWave);
          cursor += len;
          continue;
        }

        // Wave boundary cuts through a splittable inline (e.g. long <strong> or <a>): recurse inside it.
        flushBucket();
        child.children = transformBlockChildren(child.children);
        out.push(child);
      }

      flushBucket();
      return out;
    };

    tree.children = transformBlockChildren(tree.children as ElementContent[]) as typeof tree.children;
  };
}

/**
 * Tracks newly arrived lines inside a streaming fenced code block so each new line sweeps in
 * left-to-right and unwraps to static text once settled.
 */
export function useWipeLines(lineCount: number, animate: boolean): number {
  const [, setTick] = useState(0);
  const state = useRef<{ seen: number; waves: { from: number; until: number }[] }>({
    seen: animate ? 0 : lineCount,
    waves: [],
  });

  if (!animate && state.current.waves.length === 0) {
    state.current.seen = lineCount;
  } else {
    const now = performance.now();
    if (lineCount < state.current.seen) {
      state.current.seen = lineCount;
      state.current.waves = state.current.waves.filter((w) => w.from < lineCount);
    } else if (lineCount > state.current.seen && animate) {
      state.current.waves.push({ from: state.current.seen, until: now + WIPE_MS });
      state.current.seen = lineCount;
    }
    state.current.waves = state.current.waves.filter((w) => w.until > now);
  }

  const active = state.current.waves;
  const latestUntil = active.length > 0 ? active[active.length - 1]!.until : 0;

  useEffect(() => {
    if (!latestUntil) return;
    const wait = Math.max(16, latestUntil - performance.now() + 24);
    const timer = setTimeout(() => setTick((n) => n + 1), wait);
    return () => clearTimeout(timer);
  }, [latestUntil]);

  return active.length > 0 ? active[0]!.from : Infinity;
}
