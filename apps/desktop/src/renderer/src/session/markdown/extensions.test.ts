import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import Markdown from "react-markdown";
import math from "remark-math";
import katex from "rehype-katex";
import directive from "remark-directive";
import gfm from "remark-gfm";
import { texMath } from "./tex.ts";
import { contentExtensions, anchors } from "./plugins.ts";
const render = (text: string) => renderToStaticMarkup(createElement(Markdown, {
  remarkPlugins: [[math, { singleDollarTextMath: false }], texMath, directive, gfm, contentExtensions],
  rehypePlugins: [[katex, { trust: false, throwOnError: false }], [anchors, { prefix: "msg1", block: 0 }]], children: text,
}));
// The page's stylesheet and the plugin's own KaTeX can be different versions, whose class
// names differ: every subscript and fraction then draws at full size, with no error anywhere.
test("KaTeX's stylesheet styles the classes the plugin renders", () => {
  const css = readFileSync(createRequire(import.meta.url).resolve("katex/dist/katex.min.css"), "utf8");
  const sized = /class="(\S+) (reset-size\d+) (size\d+)/.exec(render(String.raw`\(x_i\)`));
  assert.ok(sized, "a subscript renders with sizing classes");
  assert.ok(css.includes(`.${sized[1]}.${sized[2]}.${sized[3]}`), `no rule for .${sized[1]}.${sized[2]}.${sized[3]}`);
});
test("TeX delimiters and display math work without treating prices as formulas", () => {
  const html = render('Price $100 and $200. Inline \\(x^2\\).\n\n\\[x + y\\]\n\n$$\nx^2\n$$');
  assert.equal((html.match(/class="katex"/g) ?? []).length, 3);
  assert.ok(html.includes("$100 and $200"));
  assert.ok(html.includes("<math"));
});
test("math syntax stays literal inside code and escaped backslashes", () => {
  const html = render('`\\(x\\)`\n\n```text\n\\[x\\]\n```\n\n\\\\(literal\\\\)');
  assert.ok(!html.includes('class="katex"'));
  assert.ok(html.includes('\\(x\\)'));
});
test("large formulas fall back to code before KaTeX", () => {
  for (const source of ['$$\n' + 'x'.repeat(5001) + '\n$$', '```math\n' + 'x'.repeat(5001) + '\n```']) assert.ok(!render(source).includes('class="katex"'));
});
test("callouts and fixed disclosure directives preserve content", () => {
  const html = render('> [!WARNING]\n> Check inputs.\n\n:::details{title="More"}\nDetails\n:::');
  assert.ok(html.includes('data-kind="warning"'));
  assert.ok(html.includes('<details data-title="More">'));
  assert.ok(html.includes('Details'));
});
test("text that only looks like a directive stays as written", () => {
  const html = render("Next runs at 19:56. Meet at 10:30am, ratio 3:2, a:b[c]{d=e}\n\n::leaf\n\nand **bold:x**.");
  for (const kept of ["19:56.", "10:30am", "3:2", "a:b[c]{d=e}", "::leaf", "bold:x"]) assert.ok(html.includes(kept), `${kept} in ${html}`);
});
test("footnotes and backreferences use a message namespace", () => {
  const html = render('A[^1]\n\n[^1]: source');
  assert.ok(html.includes('href="#msg1-user-content-fn-1"'));
  assert.ok(html.includes('id="msg1-user-content-fn-1"'));
  assert.ok(html.includes('href="#msg1-user-content-fnref-1"'));
});
