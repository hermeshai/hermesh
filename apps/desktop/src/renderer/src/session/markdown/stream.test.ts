import { test } from "node:test";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import Markdown from "react-markdown";
import gfm from "remark-gfm";
import type { Element, Root as HtmlRoot } from "hast";
import { nextRowEnd, silkWipe, WIPE_MS, type SilkWipeState } from "./stream.ts";

function renderWithWipe(md: string, state: SilkWipeState, now: number): { html: string; tree: HtmlRoot } {
  let captured!: HtmlRoot;
  const capture = () => (tree: HtmlRoot) => {
    captured = structuredClone(tree);
  };
  const html = renderToStaticMarkup(
    createElement(Markdown, {
      remarkPlugins: [[gfm, { singleTilde: false }]],
      rehypePlugins: [[silkWipe, { state, now: () => now }], capture],
      children: md,
    }),
  );
  return { html, tree: captured };
}

function findElements(node: HtmlRoot | Element, tag: string): Element[] {
  const found: Element[] = [];
  if (node.type === "element" && node.tagName === tag) found.push(node);
  for (const c of node.children) {
    if (c.type === "element") found.push(...findElements(c, tag));
  }
  return found;
}

const stripTags = (html: string) => html.replace(/<[^>]+>/g, "");

test("nextRowEnd cuts immediately at newline and skips leading blank lines", () => {
  const text = "## 标题\n\n第一行内容\n第二行内容";
  const first = nextRowEnd(text, 0, false);
  assert.equal(text.slice(0, first!), "## 标题\n");
  const second = nextRowEnd(text, first!, false);
  assert.equal(text.slice(first!, second!), "\n第一行内容\n");
  assert.equal(nextRowEnd("短句但没换行", 0, false), null);
  assert.equal(nextRowEnd("短句但没换行", 0, true), 6);
});

test("nextRowEnd cuts long single-line prose into full visual rows without splitting inline code or links", () => {
  const cjk = "这是一整排没有换行符但长度已经超过三十四个汉字的流式输出段落，用来验证按视觉整排蓄水切分时不会把句子切碎。";
  const cut = nextRowEnd(cjk, 0, false);
  assert.ok(cut !== null && cut >= 28 && cut < cjk.length, `expected full row cut, got ${cut}`);

  // Long link URL does not count toward visible width or get split mid-URL.
  const withLink = "请先查看 [设计文档](https://example.com/very/long/path/that/exceeds/seventy/ascii/characters/easily) 再继续";
  assert.equal(nextRowEnd(withLink, 0, false), null);

  // Inline code at the row boundary stays whole.
  const withCode = "这里先铺垫二十六个汉字让游标刚好走到代码胶囊边缘然后看 `apps/desktop/src/SessionPage.tsx` 后面还有文字";
  const codeCut = nextRowEnd(withCode, 0, false)!;
  const head = withCode.slice(0, codeCut);
  assert.equal((head.match(/`/g) ?? []).length % 2, 0, `cut inside inline code: ${head}`);
});

test("nextRowEnd holds back raw dangling syntax on timeout flush", () => {
  assert.equal(nextRowEnd("**", 0, true), null);
  assert.equal(nextRowEnd("| 表头", 0, true), null);
  assert.equal(nextRowEnd("先看 *", 0, true), 3);
});

test("silkWipe groups consecutive inline siblings of one wave into a single span and keeps inline code atomic", () => {
  const state: SilkWipeState = { seen: 0, waves: [] };
  const row1 = "先看 **加粗重点** 与 `src/Markdown.tsx` 的实现。";
  const r1 = renderWithWipe(row1, state, 100);

  // Entire single-wave row (text + <strong> + text + <code> + text) merges into ONE .md-wipe span!
  assert.equal((r1.html.match(/class="md-wipe"/g) ?? []).length, 1);
  // Inline <code> still has a single plain text child so InlineCode FileTag detection works.
  const codes = findElements(r1.tree, "code");
  assert.equal(codes.length, 1);
  assert.equal(codes[0]!.children.length, 1);
  assert.equal(codes[0]!.children[0]!.type, "text");
  assert.equal(stripTags(r1.html), "先看 加粗重点 与 src/Markdown.tsx 的实现。");

  // Re-rendering the exact same content mid-wipe does not duplicate waves or spans.
  const rSame = renderWithWipe(row1, state, 250);
  assert.equal((rSame.html.match(/class="md-wipe"/g) ?? []).length, 1);

  // Appending a second row while the first is still wiping yields 2 stable .md-wipe spans.
  const row2 = row1 + "紧接着第二排内容到达并独立扫出。";
  const r2 = renderWithWipe(row2, state, 300);
  assert.equal((r2.html.match(/class="md-wipe"/g) ?? []).length, 2);
  assert.equal(stripTags(r2.html), "先看 加粗重点 与 src/Markdown.tsx 的实现。紧接着第二排内容到达并独立扫出。");

  // Once WIPE_MS elapses after the last wave, all .md-wipe spans unwrap back to 0.
  const settled = renderWithWipe(row2, state, 300 + WIPE_MS + 10);
  assert.equal((settled.html.match(/class="md-wipe"/g) ?? []).length, 0);
  assert.equal(stripTags(settled.html), "先看 加粗重点 与 src/Markdown.tsx 的实现。紧接着第二排内容到达并独立扫出。");
});
