import { test } from "node:test";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import Markdown from "react-markdown";
import gfm from "remark-gfm";
import math from "remark-math";
import directive from "remark-directive";
import {
  createMarkdownSlices,
  extractPureImageSlice,
  isTallStageImage,
  isUltraLongImage,
  pendingText,
  scanReplyImages,
  shortImageTag,
  unwrapCarousels,
} from "./parse.ts";

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

test("unwrapCarousels merges pure-image slides and separates mixed slides without touching outer or inner code fences", () => {
  const pure = "Before\n\n````carousel\n![Dark](/tmp/dark.png)\n<!-- slide -->\n![Light](/tmp/light.png)\n````\n\nAfter";
  assert.equal(unwrapCarousels(pure), "Before\n\n![Dark](/tmp/dark.png)\n![Light](/tmp/light.png)\n\nAfter");

  const mixed = "````carousel\n![Shot](/tmp/a.png)\n<!-- slide -->\n```python\nprint('<!-- slide -->')\n```\n````";
  assert.equal(unwrapCarousels(mixed), "![Shot](/tmp/a.png)\n\n---\n\n```python\nprint('<!-- slide -->')\n```");

  const quoted = "`````markdown\n````carousel\n![Literal](/tmp/a.png)\n````\n`````";
  assert.equal(unwrapCarousels(quoted), quoted);

  const streaming = "````carousel\n![One](/tmp/1.png)\n<!-- slide -->\n![Two](/tmp/2.png)";
  assert.equal(unwrapCarousels(streaming), "![One](/tmp/1.png)\n![Two](/tmp/2.png)");

  const spaced = "See ![shot](/Users/me/Library/Application Support/Hermesh/shot.png) and `[raw](/Application Support/x.png)`";
  assert.equal(
    unwrapCarousels(spaced),
    "See ![shot](/Users/me/Library/Application%20Support/Hermesh/shot.png) and `[raw](/Application Support/x.png)`",
  );

  const nested = "````carousel\n![方案 A [推荐 1]](/Users/me/Library/Application Support/demo (1).png)\n<!-- slide -->\n![方案 B [推荐 2]](/tmp/demo (2).png)\n````";
  assert.equal(
    unwrapCarousels(nested),
    "![方案 A [推荐 1]](/Users/me/Library/Application%20Support/demo%20(1).png)\n![方案 B [推荐 2]](/tmp/demo%20(2).png)",
  );
});

test("extractPureImageSlice recognizes standalone image blocks and pure image lists without false positives", () => {
  assert.deepEqual(extractPureImageSlice("![One [1/2]](/tmp/shot (1).png)\n"), {
    fromList: false,
    items: [{ path: "/tmp/shot (1).png", caption: "One [1/2]" }],
  });
  assert.deepEqual(extractPureImageSlice("- ![A](imgs/a.png)\n- ![B](file:///tmp/b.webp)", "/work/docs"), {
    fromList: true,
    items: [
      { path: "/work/docs/imgs/a.png", caption: "A" },
      { path: "/tmp/b.webp", caption: "B" },
    ],
  });
  assert.deepEqual(extractPureImageSlice("1. ![First](/tmp/1.jpg)\n2. ![Second](/tmp/2.gif)"), {
    fromList: true,
    items: [
      { path: "/tmp/1.jpg", caption: "First" },
      { path: "/tmp/2.gif", caption: "Second" },
    ],
  });

  // False-positive guards: external URLs, SVGs, line refs, task lists, quotes, indented code, mixed prose.
  for (const nope of [
    "![CI](https://example.com/badge.png)",
    "![Icon](assets/logo.svg)",
    "[Code](src/app.ts:12)",
    "- [x] ![Done](/tmp/a.png)\n- [ ] ![Todo](/tmp/b.png)",
    "> ![Quoted](/tmp/a.png)",
    "    ![IndentedCode](/tmp/a.png)",
    "先看这张图 ![Inline](/tmp/a.png)",
    "- 方案说明文字\n- ![OnlySecond](/tmp/b.png)",
  ]) {
    assert.equal(extractPureImageSlice(nope), null, nope);
  }

  const scanned = scanReplyImages("![主图 [1/2]](/tmp/a (1).png)\n\n对比 [文件](/tmp/a (1).png) 与 [另一张](/tmp/b.png)");
  assert.deepEqual(scanned.explicit, ["/tmp/a (1).png"]);
  assert.deepEqual(scanned.gallery, [
    { path: "/tmp/a (1).png", caption: "主图 [1/2]" },
    { path: "/tmp/b.png", caption: "另一张" },
  ]);

  assert.equal(shortImageTag("0 · 现状对照：桌面端双黑砖碰撞", "demo.png"), "现状对照");
  assert.equal(shortImageTag("方案 A（默认收起 · 推荐 1）：连体抽屉托盘", "demo-a.png"), "方案 A");
  assert.equal(shortImageTag("方案一 [默认收起]：连体抽屉托盘", "stage_1.png"), "方案一 [默认收起]");
  assert.equal(shortImageTag("stage_1.png", "stage_1.png"), "");
  assert.equal(shortImageTag("", "stage_1.png"), "");

  assert.equal(isTallStageImage(1206, 2622), true);
  assert.equal(isTallStageImage(1000, 1000), true);
  assert.equal(isTallStageImage(2544, 1390), false);
  assert.equal(isTallStageImage(0, 200), false);

  assert.equal(isUltraLongImage(750, 3150), true);
  assert.equal(isUltraLongImage(1206, 2622), false);
  assert.equal(isUltraLongImage(2544, 1390), false);
});
