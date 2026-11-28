import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import remarkDirective from "remark-directive";
import { resolveDocPath, sameImagePath, sessionImageRef } from "@hermesh/her-client";
import { texMath } from "./tex.ts";

export type ReplyImageItem = { path: string; caption: string };
export type ReplyImages = { explicit: string[]; gallery: ReplyImageItem[] };

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
const standaloneImage = /^!\[(?:[^\[\]\n]|\[[^\[\]\n]*\])*\]\((?:<[^>\n]+>|(?:[^()\n]|\([^()\n]*\))+)\)$/;
const localDestPrefix = /^(?:\/|~\/|\.\.?\/|file:\/\/|[A-Za-z]:[\\/])/i;

function findClosingParen(line: string, start: number): number {
  let depth = 1;
  for (let j = start; j < line.length; j++) {
    if (line[j] === "(") depth++;
    else if (line[j] === ")") {
      depth--;
      if (depth === 0) return j;
    }
  }
  return -1;
}

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
      const close = findClosingParen(line, i + 2);
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

function cleanImageDest(dest: string): string {
  let raw = dest.trim();
  if (raw.startsWith("<") && raw.includes(">")) raw = raw.slice(1, raw.indexOf(">"));
  else {
    const titleIdx = raw.search(/\s+["']/);
    if (titleIdx > 0) raw = raw.slice(0, titleIdx);
  }
  return raw;
}

const inlineLinkOrImg = /(!?)\[((?:[^\[\]\n]|\[[^\[\]\n]*\])*)\]\((<[^>\n]+>|(?:[^()\n]|\([^()\n]*\))+)\)/g;
const standaloneBangImg = /^!\[((?:[^\[\]\n]|\[[^\[\]\n]*\])*)\]\((<[^>\n]+>|(?:[^()\n]|\([^()\n]*\))+)\)$/;
const plainListItem = /^ {0,3}(?:[-+*]|\d+[.)])\s+(?!\[[ xX]\]\s)(.+)$/;

/**
 * Recognizes a settled Markdown slice consisting solely of standalone local session images
 * (either image-only lines or a plain bullet/numbered list of standalone session images)
 * so consecutive blank-line-separated image blocks merge cleanly with zero false positives.
 */
export function extractPureImageSlice(text: string, basePath = ""): { items: ReplyImageItem[]; fromList: boolean } | null {
  const nonEmpty = text.split("\n").filter((l) => l.trim() !== "");
  if (nonEmpty.length === 0) return null;
  let fromList: boolean | null = null;
  const items: ReplyImageItem[] = [];
  for (const line of nonEmpty) {
    if (/^(?: {4,}|\t)/.test(line)) return null;
    const listMatch = plainListItem.exec(line);
    const isList = listMatch !== null;
    if (fromList !== null && fromList !== isList) return null;
    fromList = isList;
    const candidate = (listMatch ? listMatch[1]! : line).trim();
    const imgMatch = standaloneBangImg.exec(candidate);
    if (!imgMatch) return null;
    const rawPath = sessionImageRef(cleanImageDest(imgMatch[2]!));
    if (!rawPath) return null;
    const path = resolveDocPath(basePath, rawPath);
    if (!items.some((x) => sameImagePath(x.path, path))) {
      items.push({ path, caption: imgMatch[1]!.trim() });
    }
  }
  return items.length > 0 && fromList !== null ? { items, fromList } : null;
}

export const TALL_STAGE_RATIO = 1.05;
export const ULTRA_LONG_RATIO = 0.38;

/**
 * True when a decoded image is portrait, tall, or near-square (`width / height <= 1.05`),
 * so 3+ multi-image groups switch from a single-slide wide stage to an equal-height multi-card strip.
 */
export function isTallStageImage(width: number, height: number): boolean {
  return width > 0 && height > 0 && width / height <= TALL_STAGE_RATIO;
}

/**
 * True when a decoded image is an ultra-long scroll capture (`width / height < 0.38`),
 * warranting a readable width floor, top-aligned first-screen crop, and subtle badge inline.
 */
export function isUltraLongImage(width: number, height: number): boolean {
  return width > 0 && height > 0 && width / height < ULTRA_LONG_RATIO;
}

/**
 * Extracts a concise 2–12 char editorial label from an image caption for typographic index chips
 * (e.g. `"0 · 现状对照：桌面端..."` → `"现状对照"`, `"方案 A（默认收起）：..."` → `"方案 A"`),
 * returning `""` when the caption carries no distinct title beyond the filename.
 */
export function shortImageTag(caption: string, fileName: string): string {
  const trimmed = caption.trim();
  if (!trimmed || trimmed.toLowerCase() === fileName.trim().toLowerCase()) return "";
  let head = trimmed.split(/\s*(?:[：:—–]| - )\s*/)[0]!.trim();
  head = head.replace(/^(?:第\s*\d+\s*[张页步帧]?|0?\d+\s*[.、·\-:])\s*/u, "").trim();
  if (head.length > 11) {
    const stripped = head.replace(/\s*(?:（[^）]*）|\([^)]*\)|\[[^\]]*\])\s*$/g, "").trim();
    if (stripped.length >= 2) head = stripped;
  }
  if (head.length > 12) return `${head.slice(0, 11).trimEnd()}…`;
  return head;
}

/**
 * Scans settled Markdown text (skipping fenced and inline code) to find `![alt](src)` explicit
 * images and ordered unique gallery items so `[label](path)` never duplicates `![alt](path)`.
 */
export function scanReplyImages(text: string, basePath = ""): ReplyImages {
  const stripped = text
    .replace(/^(?:`{3,}|~{3,})[\s\S]*?^(?:`{3,}|~{3,})/gm, "")
    .replace(/`[^`\n]+`/g, "");
  const found: { bang: boolean; label: string; path: string }[] = [];
  const re = new RegExp(inlineLinkOrImg.source, "g");
  let m: RegExpExecArray | null;
  while ((m = re.exec(stripped)) !== null) {
    const raw = cleanImageDest(m[3]!);
    const rawPath = sessionImageRef(raw);
    const path = rawPath ? resolveDocPath(basePath, rawPath) : null;
    if (path) found.push({ bang: m[1] === "!", label: m[2]!.trim(), path });
  }
  const explicit: string[] = [];
  for (const f of found) {
    if (f.bang && !explicit.some((p) => sameImagePath(p, f.path))) explicit.push(f.path);
  }
  const gallery: ReplyImageItem[] = [];
  for (const f of found) {
    if (!f.bang && explicit.some((p) => sameImagePath(p, f.path))) continue;
    if (gallery.some((g) => sameImagePath(g.path, f.path))) continue;
    gallery.push({ path: f.path, caption: f.label });
  }
  return { explicit, gallery };
}
