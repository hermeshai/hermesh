// How a message mentions skills: `/skill:name` anywhere in the user's text, outside code
// (docs/kernel/skill-design.md §6.3.2). The kernel reads text the same way (her-core commands.rs);
// both run the cases in her/crates/her-core/testdata/mentions.json. Offsets here are UTF-16
// indices, as JavaScript strings count them.

/** What a mention starts with. */
export const MENTION_PREFIX = "/skill:";

/** A `/skill:name` in a text: `start` to `end` covers all of it. */
export type Mention = { start: number; end: number; name: string };

/** A piece of a message's text: plain, or a mention of `skill`. */
export type Segment = { text: string; skill?: string };

const isNameChar = (c: string) => /^[A-Za-z0-9_-]$/.test(c);
/** ASCII characters that go on in a path or an address: next to one, `/skill:name` is part of something else. */
const isPathChar = (c: string) => /^[A-Za-z0-9/\\.:~_\-@#%&=?+$*]$/.test(c);
const isAscii = (c: string) => c.charCodeAt(0) < 0x80;
const isSpace = (c: string) => /\s/.test(c);

/** Whether `/skill:name` can be written for this name: other characters would end the mention. */
export function isMentionable(name: string): boolean {
  return name.length > 0 && [...name].every(isNameChar);
}

/** Every `/skill:name` in `text` that stands on its own, outside code, whether or not a skill has that name. */
export function mentions(text: string): Mention[] {
  const code = codeRanges(text);
  const out: Mention[] = [];
  let from = 0;
  for (;;) {
    const start = text.indexOf(MENTION_PREFIX, from);
    if (start < 0) return out;
    from = start + 1;
    if (code.some(([a, b]) => start >= a && start < b) || (start > 0 && isPathChar(text[start - 1]))) continue;
    const nameStart = start + MENTION_PREFIX.length;
    let end = nameStart;
    while (end < text.length && isNameChar(text[end])) end += 1;
    if (end === nameStart || !endsMention(text, end)) continue;
    out.push({ start, end, name: text.slice(nameStart, end) });
    from = end;
  }
}

/** The mentions of `text` that name one of `skills`. */
export function skillMentions(text: string, skills: Iterable<string>): Mention[] {
  const known = new Set(skills);
  return mentions(text).filter((m) => known.has(m.name));
}

/** `text` cut at its mentions of `skills`, which a page draws as tags. */
export function segments(text: string, skills: Iterable<string>): Segment[] {
  const out: Segment[] = [];
  let at = 0;
  for (const m of skillMentions(text, skills)) {
    if (m.start > at) out.push({ text: text.slice(at, m.start) });
    out.push({ text: text.slice(m.start, m.end), skill: m.name });
    at = m.end;
  }
  if (at < text.length || out.length === 0) out.push({ text: text.slice(at) });
  return out;
}

/** Whether what follows a name ends the mention: nothing, a space, a character outside ASCII,
 * an ASCII character that is not part of a path, or `.`, `:` or `?` ending a sentence. */
function endsMention(text: string, at: number): boolean {
  if (at >= text.length) return true;
  const c = text[at];
  if (c === "." || c === ":" || c === "?") {
    const next = text[at + 1];
    return next === undefined || isSpace(next) || !isAscii(next);
  }
  return isSpace(c) || !isAscii(c) || !isPathChar(c);
}

/** Ranges of `text` that are code: fenced blocks, from a line starting with ``` or ~~~ to the
 * next such line (or the end), and inline spans between equal runs of backticks. */
function codeRanges(text: string): [number, number][] {
  const ranges: [number, number][] = [];
  let fence: { start: number; marker: string } | null = null;
  let proseStart = 0;
  let offset = 0;
  for (const line of linesWithEnds(text)) {
    const trimmed = line.replace(/^ +/, "");
    const marker = line.length - trimmed.length <= 3 ? (["```", "~~~"].find((m) => trimmed.startsWith(m)) ?? null) : null;
    if (fence === null && marker !== null) {
      inlineCode(text, proseStart, offset, ranges);
      fence = { start: offset, marker };
    } else if (fence !== null && marker === fence.marker) {
      ranges.push([fence.start, offset + line.length]);
      fence = null;
      proseStart = offset + line.length;
    }
    offset += line.length;
  }
  if (fence !== null) ranges.push([fence.start, text.length]);
  else inlineCode(text, proseStart, text.length, ranges);
  return ranges;
}

/** Inline code in `text` from `from` to `to`: a run of backticks up to the next run of the same length. A run never closed is text. */
function inlineCode(text: string, from: number, to: number, out: [number, number][]) {
  const runEnd = (i: number) => {
    while (i < to && text[i] === "`") i += 1;
    return i;
  };
  let i = from;
  while (i < to) {
    if (text[i] !== "`") {
      i += 1;
      continue;
    }
    const open = i;
    i = runEnd(i);
    const length = i - open;
    let j = i;
    while (j < to) {
      if (text[j] !== "`") {
        j += 1;
        continue;
      }
      const close = j;
      j = runEnd(j);
      if (j - close === length) {
        out.push([open, j]);
        i = j;
        break;
      }
    }
  }
}

/** The lines of `text`, each with its line break. */
function linesWithEnds(text: string): string[] {
  const lines: string[] = [];
  let i = 0;
  while (i < text.length) {
    const j = text.indexOf("\n", i);
    if (j < 0) {
      lines.push(text.slice(i));
      break;
    }
    lines.push(text.slice(i, j + 1));
    i = j + 1;
  }
  return lines;
}
