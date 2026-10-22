// Agent replies as Markdown, parsed to plain data the window renders as text
// nodes: never HTML. Blocks: paragraphs, headings, lists, quotes, fenced
// code. Inline: code, bold, italic, links (http and https only, written as
// [text](url) or bare).

export type Inline =
  | { t: "text"; v: string }
  | { t: "code"; v: string }
  | { t: "strong"; c: Inline[] }
  | { t: "em"; c: Inline[] }
  | { t: "link"; href: string; c: Inline[] }
  | { t: "br" };

export type Block =
  | { t: "p"; c: Inline[] }
  | { t: "h"; level: 1 | 2 | 3; c: Inline[] }
  | { t: "ul" | "ol"; start: number; items: Inline[][] }
  | { t: "quote"; c: Inline[] }
  | { t: "pre"; lang: string; v: string };

export function markdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const out: Block[] = [];
  let i = 0;
  const isBlank = (l: string) => l.trim() === "";
  const listItem = (l: string) => /^\s{0,3}([-*+]|\d{1,9}[.)])\s+(.*)$/.exec(l);
  const startsBlock = (l: string) => /^\s{0,3}(```|~~~|#{1,6}\s|>)/.test(l) || listItem(l) !== null;
  while (i < lines.length) {
    const line = lines[i];
    if (isBlank(line)) {
      i++;
      continue;
    }
    const fence = /^\s{0,3}(```|~~~)\s*([\w+-]*)/.exec(line);
    if (fence) {
      const code: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith(fence[1])) code.push(lines[i++]);
      i++; // the closing fence (or the end)
      out.push({ t: "pre", lang: fence[2], v: code.join("\n") });
      continue;
    }
    const h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (h) {
      out.push({ t: "h", level: Math.min(3, h[1].length) as 1 | 2 | 3, c: inline(h[2]) });
      i++;
      continue;
    }
    if (/^\s{0,3}>/.test(line)) {
      const q: string[] = [];
      while (i < lines.length && /^\s{0,3}>/.test(lines[i])) q.push(lines[i++].replace(/^\s{0,3}>\s?/, ""));
      out.push({ t: "quote", c: inline(q.join("\n")) });
      continue;
    }
    const li = listItem(line);
    if (li) {
      const ordered = /\d/.test(li[1]);
      const items: string[][] = [];
      while (i < lines.length) {
        const m = listItem(lines[i]);
        if (m && /\d/.test(m[1]) === ordered) items.push([m[2]]);
        else if (!isBlank(lines[i]) && /^\s+\S/.test(lines[i]) && items.length) items[items.length - 1].push(lines[i].trim());
        else break;
        i++;
      }
      out.push({ t: ordered ? "ol" : "ul", start: ordered ? parseInt(li[1], 10) : 1, items: items.map((x) => inline(x.join("\n"))) });
      continue;
    }
    const p: string[] = [];
    while (i < lines.length && !isBlank(lines[i]) && (p.length === 0 || !startsBlock(lines[i]))) p.push(lines[i++]);
    out.push({ t: "p", c: inline(p.join("\n")) });
  }
  return out;
}

export function inline(s: string): Inline[] {
  const out: Inline[] = [];
  let text = "";
  const flush = () => {
    if (text) out.push({ t: "text", v: text });
    text = "";
  };
  let i = 0;
  while (i < s.length) {
    const rest = s.slice(i);
    let m: RegExpExecArray | null;
    if (s[i] === "\n") {
      flush();
      out.push({ t: "br" });
      i++;
    } else if ((m = /^`([^`\n]+)`/.exec(rest))) {
      flush();
      out.push({ t: "code", v: m[1] });
      i += m[0].length;
    } else if ((m = /^\*\*([^\n]+?)\*\*/.exec(rest)) || (m = /^__([^\n]+?)__/.exec(rest))) {
      flush();
      out.push({ t: "strong", c: inline(m[1]) });
      i += m[0].length;
    } else if ((m = /^\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/.exec(rest))) {
      flush();
      out.push({ t: "link", href: m[2], c: inline(m[1]) });
      i += m[0].length;
    } else if ((m = /^https?:\/\/[^\s<>()]+[^\s<>().,;:!?，。；：！？）]/.exec(rest)) && !/[\w/]$/.test(text)) {
      flush();
      out.push({ t: "link", href: m[0], c: [{ t: "text", v: m[0] }] });
      i += m[0].length;
    } else if ((m = /^\*([^*\s][^*\n]*?)\*/.exec(rest)) && !/\w$/.test(text)) {
      flush();
      out.push({ t: "em", c: inline(m[1]) });
      i += m[0].length;
    } else {
      text += s[i++];
    }
  }
  flush();
  return out;
}
