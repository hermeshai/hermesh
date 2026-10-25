// A reply the agent is still writing, cut into blocks that render the same
// alone (docs/reply-streaming.md): the finished ones are parsed once, and
// only the last one changes as the reply grows.

const fence = /^ {0,3}(```|~~~)/;

/**
 * Splits Markdown into blocks that parse the same alone: at a blank line
 * followed by an unindented line, outside code fences (an indented line
 * may go on a list item).
 */
export function splitBlocks(text: string): string[] {
  const blocks: string[] = [];
  let cur: string[] = [];
  let open = "";
  let blank = false;
  for (const line of text.split("\n")) {
    const f = fence.exec(line);
    if (open === "" && blank && line.trim() !== "" && !/^\s/.test(line) && cur.length) {
      blocks.push(cur.join("\n"));
      cur = [];
    }
    if (f) open = open === "" ? f[1] : f[1] === open ? "" : open;
    blank = open === "" && line.trim() === "";
    cur.push(line);
  }
  blocks.push(cur.join("\n"));
  return blocks;
}

const cells = (row: string) => row.trim().replace(/^\|/, "").replace(/\|$/, "").split("|");

/** The block without a table at its end whose delimiter row (|---|---|) has not come yet. */
export function withoutOpenTable(block: string): string {
  const lines = block.split("\n");
  let start = lines.length;
  while (start > 0 && /^\s*\|/.test(lines[start - 1])) start--;
  const table = lines.slice(start);
  if (table.length === 0) return block;
  const ready = table.length >= 2 && cells(table[1]).length === cells(table[0]).length && cells(table[1]).every((c) => /^\s*:?-+:?\s*$/.test(c));
  return ready ? block : lines.slice(0, start).join("\n");
}
