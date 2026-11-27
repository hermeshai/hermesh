import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import remarkDirective from "remark-directive";
import { texMath } from "./tex.ts";

// The same grammar as the renderer. Keep the last AST block open: a paragraph can
// become a heading/table, and lists/quotes can continue after a blank line.
const parser = unified().use(remarkParse).use(remarkGfm, { singleTilde: false }).use(remarkMath, { singleDollarTextMath: false }).use(remarkDirective).use(texMath);
export type MarkdownSlice = { id: number; text: string; pending: boolean };

// A definition, not a use, is what ties blocks together: `[a]` or `arr[0]` means
// nothing across blocks until `[a]: url` or `[^a]: note` exists. Every finished block
// is then parsed again together, however far above the definition it sits. A line
// that only looks like one costs that parse, so containers may lead and code may match.
const definition = /^[\s>]*(?:(?:[-+*]|\d+[.)])\s+)?\[[^\]\n]+\]:/;
function hasDefinitions(text: string) {
  if (!text.includes("]:")) return false;
  let fence = "";
  for (const line of text.split("\n")) {
    const marker = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
    if (marker && !fence && !(marker[1][0] === "`" && marker[2].includes("`"))) { fence = marker[1]; continue; }
    if (fence) {
      if (marker && marker[1][0] === fence[0] && marker[1].length >= fence.length && !marker[2].trim()) fence = "";
      continue;
    }
    if (definition.test(line)) return true;
  }
  return false;
}

export function createMarkdownSlices() {
  let previous = "";
  let prefix: MarkdownSlice[] = [];
  let offset = 0;
  return (text: string, streaming: boolean): MarkdownSlice[] => {
    if (!text.startsWith(previous)) { prefix = []; offset = 0; }
    previous = text;
    if (hasDefinitions(text)) {
      prefix = []; offset = 0;
      return [{ id: 0, text, pending: streaming }];
    }
    const tail = text.slice(offset);
    const nodes = parser.parse(tail).children;
    // Retain two AST nodes for lookbehind: an arriving "2" is initially a
    // paragraph, but "2. next" can still join the preceding loose list.
    const keepFrom = Math.max(0, nodes.length - 2);
    const boundary = keepFrom ? nodes[keepFrom].position!.start.offset! : 0;
    const slices = nodes.map((node, i) => {
      const start = i === 0 ? 0 : node.position!.start.offset!;
      const end = nodes[i + 1]?.position?.start.offset ?? tail.length;
      return { id: offset + start, text: tail.slice(start, end), pending: streaming && i === nodes.length - 1 };
    });
    prefix = [...prefix, ...slices.slice(0, keepFrom).map((slice) => ({ ...slice, pending: false }))];
    offset += boundary;
    return [...prefix, ...slices.slice(keepFrom)];
  };
}

/** Only suppress a provisional table outside a fenced block. Escaped pipes and
 * pipes in inline code belong to a cell, not to the column count. Until its
 * delimiter row is whole a table is a paragraph's last lines, its header and the
 * part of that row written: those lines, each starting with a pipe, wait. */
export function pendingText(text: string): string {
  const nodes = parser.parse(text).children;
  const last = nodes.at(-1);
  if (last?.type !== "paragraph") return text;
  const start = last.position!.start.offset!;
  const lines = text.slice(start).split("\n");
  const end = lines.at(-1) === "" ? lines.length - 1 : lines.length;
  let cut = end;
  while (cut > 0 && /^\s*\|/.test(lines[cut - 1])) cut--;
  if (cut === end) return text;
  const cutLines = lines.slice(0, cut);
  return cut === 0 ? text.slice(0, start) : text.slice(0, start) + cutLines.join("\n");
}

const fenceLine = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const standaloneImage = /^!\[[^\]]*\]\([^)\n]+\)$/;
const localDestPrefix = /^(?:\/|~\/|\.\.?\/|file:\/\/|[A-Za-z]:[\\/])/i;

function encodeLocalLinkSpaces(line: string): string {
  if (!line.includes("](") || !line.includes(" ")) return line;
  let out = "";
  let i = 0;
  while (i < line.length) {
    if (line[i] === "`") {
      let ticks = 1;
      while (i + ticks < line.length && line[i + ticks] === "`") ticks++;
      const fence = line.slice(i, i + ticks);
      const close = line.indexOf(fence, i + ticks);
      if (close >= 0) {
        out += line.slice(i, close + ticks);
        i = close + ticks;
        continue;
      }
    }
    if (line[i] === "]" && line[i + 1] === "(") {
      const close = line.indexOf(")", i + 2);
      if (close > i + 2) {
        const dest = line.slice(i + 2, close);
        const trimmed = dest.trim();
        if (trimmed.includes(" ") && !trimmed.startsWith("<") && localDestPrefix.test(trimmed) && !/\s+["']/.test(trimmed)) {
          out += `](${trimmed.replace(/ /g, "%20")})`;
          i = close + 1;
          continue;
        }
      }
    }
    out += line[i]!;
    i++;
  }
  return out;
}

function splitCarouselSlides(lines: string[]): string[] {
  const slides: string[][] = [[]];
  let inner = "";
  for (const line of lines) {
    const m = fenceLine.exec(line);
    if (m && !inner && !(m[1]![0] === "`" && m[2]!.includes("`"))) {
      inner = m[1]!;
      slides[slides.length - 1]!.push(line);
      continue;
    }
    if (inner) {
      if (m && m[1]![0] === inner[0] && m[1]!.length >= inner.length && !m[2]!.trim()) inner = "";
      slides[slides.length - 1]!.push(line);
      continue;
    }
    const parts = line.split(/<!--\s*slide\s*-->/i);
    if (parts.length > 1) {
      slides[slides.length - 1]!.push(encodeLocalLinkSpaces(parts[0]!));
      for (let i = 1; i < parts.length; i++) slides.push([encodeLocalLinkSpaces(parts[i]!)]);
      continue;
    }
    slides[slides.length - 1]!.push(encodeLocalLinkSpaces(line));
  }
  return slides.map((s) => s.join("\n").trim()).filter(Boolean);
}

/**
 * Unwraps Antigravity ````carousel ... <!-- slide --> ... ```` blocks into standard Markdown
 * and percent-encodes unescaped spaces in local file/image destinations (e.g. `Application Support`):
 * pure-image slides merge into one paragraph so Hermesh renders them as an inline horizontal
 * carousel strip + Lightbox; mixed code/prose slides separate with horizontal rules.
 */
export function unwrapCarousels(text: string): string {
  if (!/carousel/i.test(text) && !(text.includes("](") && text.includes(" "))) return text;
  const lines = text.split("\n");
  const out: string[] = [];
  let outer = "";
  let carouselFence = "";
  let buf: string[] = [];

  const flushCarousel = () => {
    const slides = splitCarouselSlides(buf);
    buf = [];
    if (slides.length === 0) return;
    const allImages = slides.every((s) =>
      s.split("\n").map((l) => l.trim()).filter(Boolean).every((l) => standaloneImage.test(l)),
    );
    if (allImages) {
      const imgs = slides.flatMap((s) => s.split("\n").map((l) => l.trim()).filter(Boolean));
      out.push(imgs.join("\n"));
    } else {
      out.push(slides.join("\n\n---\n\n"));
    }
  };

  for (const line of lines) {
    const m = fenceLine.exec(line);
    if (carouselFence) {
      if (m && m[1]![0] === carouselFence[0] && m[1]!.length >= carouselFence.length && !m[2]!.trim()) {
        carouselFence = "";
        flushCarousel();
      } else {
        buf.push(line);
      }
      continue;
    }
    if (outer) {
      if (m && m[1]![0] === outer[0] && m[1]!.length >= outer.length && !m[2]!.trim()) outer = "";
      out.push(line);
      continue;
    }
    if (m && !(m[1]![0] === "`" && m[2]!.includes("`"))) {
      if (m[2]!.trim().toLowerCase() === "carousel") {
        carouselFence = m[1]!;
        buf = [];
        continue;
      }
      outer = m[1]!;
      out.push(line);
      continue;
    }
    out.push(encodeLocalLinkSpaces(line));
  }
  if (carouselFence) flushCarousel();
  return out.join("\n");
}
