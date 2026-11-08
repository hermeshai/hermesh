import { test } from "node:test";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import Markdown from "react-markdown";
import gfm from "remark-gfm";
import math from "remark-math";
import directive from "remark-directive";
import { createMarkdownSlices, pendingText } from "./parse.ts";

const render = (text: string) => renderToStaticMarkup(createElement(Markdown, { remarkPlugins: [gfm, [math, { singleDollarTextMath: false }], directive], children: text })).replace(/>\s+</g, "><");
const cases = [
  "Intro\n\n1. first\n\n2. second\n\nEnd",
  "[!reference]\n\n[!reference]: https://example.com",
  "```bad`info\n[ref]\n\n[ref]: https://example.com",
  "`unclosed [ref]\n\n[ref]: https://example.com",
  "Read [doc][r].\n\n[r]: https://example.com",
  "A[^1].\n\n[^1]: source",
  "````md\n```js\n\nexample\n```\n````\n\nDone",
  "```text\n| a | b |\n```",
  "# 标题\n\n正文\n\n下一段。\n\n标题二\n---\n\n最后",
  "| A | B |\n|---|---|\n| a\\|b | `x` |\n\nEnd",
  "> one\n>\n> two\n\n> three\n\nEnd",
  "- first\n\n  second paragraph\n\n  - nested\n\n- next\n\nEnd",
  "$$\nx^2 + y^2\n$$\n\n$100 and $200",
  ':::details{title="Details"}\nInside\n\nMore\n:::\n\nEnd',
];
for (const text of cases) test(`chunk invariant: ${text.slice(0, 35)}`, () => {
  for (const size of [1, 3, 11, text.length]) {
    const split = createMarkdownSlices();
    for (let i = size; i < text.length; i += size) split(text.slice(0, i), true);
    const live = split(text, true);
    const final = split(text, false);
    assert.deepEqual(live.map((b) => b.id), final.map((b) => b.id));
    assert.equal(final.map((b) => render(b.text)).join(""), render(text));
  }
});

test("code and escaped pipes never disappear as provisional tables", () => {
  assert.equal(pendingText("```text\n| a | b |"), "```text\n| a | b |");
  assert.equal(pendingText("|a|b|"), "");
  const table = "|a|b|\n|-|-|\n|x\\|y|z|";
  assert.equal(pendingText(table), table);
});
test("a table whose delimiter row is still being written waits, however many of its lines came", () => {
  assert.equal(pendingText("Intro\n\n| 文件 | 改动 |\n|--"), "Intro\n\n");
  assert.equal(pendingText("Intro\n\n| 文件 | 改动 |\n|-"), "Intro\n\n");
  assert.equal(pendingText("Intro\n\n| 文件 | 改动 |\n"), "Intro\n\n");
  assert.equal(pendingText("看这里\n| 文件 | 改动 |\n|--"), "看这里");
  // Once its delimiter row is whole it is a table, shown as it grows.
  const head = "Intro\n\n| 文件 | 改动 |\n|---|---|";
  assert.equal(pendingText(head), head);
  assert.equal(pendingText(head + "\n| a.go |"), head + "\n| a.go |");
});
// A bracket mid-reply must not rebuild what is finished: ids stay, so React keeps the nodes.
for (const text of ["先看 `arr[0]` 的值。\n\n第二段\n\n第三段\n\n第四段", "写 [注意] 这句。\n\n第二段\n\n第三段\n\n第四段", "- [x] done\n\n第二段\n\n第三段\n\n第四段", "匹配 `[^a-z]` 的字符。\n\n第二段\n\n第三段\n\n第四段", "> [!NOTE]\n> 提示\n\n第二段\n\n第三段\n\n第四段", "```text\n[r]: https://example.com\n```\n\n第二段\n\n第三段\n\n第四段"]) test(`no definition, no whole-document parse: ${text.slice(0, 20)}`, () => {
  const split = createMarkdownSlices();
  let most = 0;
  for (let i = 1; i <= text.length; i++) {
    const blocks = split(text.slice(0, i), true).length;
    assert.ok(blocks >= most - 1, `${blocks} blocks after ${most} at ${i}`); // Only the last block may still merge.
    most = Math.max(most, blocks);
  }
  assert.ok(most >= 4);
});
test("a definition arriving late still resolves what was written before it", () => {
  for (const [text, html] of [["Read [doc][r].\n\nMiddle\n\n[r]: https://example.com", 'href="https://example.com"'], ["A[^1].\n\nMiddle\n\n[^1]: source", "data-footnote"], ["- x\n  [r]: https://example.com\n\nRead [r].", 'href="https://example.com"'], ["> [r]: https://example.com\n\nRead [r].", 'href="https://example.com"']] as const) {
    const split = createMarkdownSlices();
    for (let i = 1; i < text.length; i++) split(text.slice(0, i), true);
    const [only, ...rest] = split(text, false);
    assert.equal(rest.length, 0, text);
    assert.ok(render(only.text).includes(html), text);
  }
});
test("replaced source resets cached prefixes", () => {
  const split = createMarkdownSlices();
  split("# old\n\nold tail", true);
  assert.equal(split("# new\n\nnew tail", false).map((b) => render(b.text)).join(""), render("# new\n\nnew tail"));
});
