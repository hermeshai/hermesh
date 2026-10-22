import { describe, expect, test } from "vitest";
import { inline, markdown } from "./markdown";

describe("markdown", () => {
  test("blocks: paragraphs, headings, lists, quotes, code", () => {
    const b = markdown("已把搜索框重做了：\n\n## 改了什么\n- `a.tsx`：入口\n- **样式** 更细\n  续行\n\n1. 第一\n2. 第二\n\n> 注意\n\n```ts\nconst a = 1;\n```\n结尾");
    expect(b.map((x) => x.t)).toEqual(["p", "h", "ul", "ol", "quote", "pre", "p"]);
    const ul = b[2];
    expect(ul.t === "ul" && ul.items.length).toBe(2);
    expect(ul.t === "ul" && ul.items[1]).toEqual([{ t: "strong", c: [{ t: "text", v: "样式" }] }, { t: "text", v: " 更细" }, { t: "br" }, { t: "text", v: "续行" }]);
    expect(b[5]).toEqual({ t: "pre", lang: "ts", v: "const a = 1;" });
  });

  test("inline: code, bold, italic, links; HTML stays text", () => {
    expect(inline("看 [PR #647](https://github.com/x/y/pull/647) 和 https://a.dev/x。")).toEqual([
      { t: "text", v: "看 " },
      { t: "link", href: "https://github.com/x/y/pull/647", c: [{ t: "text", v: "PR #647" }] },
      { t: "text", v: " 和 " },
      { t: "link", href: "https://a.dev/x", c: [{ t: "text", v: "https://a.dev/x" }] },
      { t: "text", v: "。" },
    ]);
    expect(inline("a *b* c")).toEqual([{ t: "text", v: "a " }, { t: "em", c: [{ t: "text", v: "b" }] }, { t: "text", v: " c" }]);
    expect(inline("<b>x</b> [y](javascript:alert(1))")).toEqual([{ t: "text", v: "<b>x</b> [y](javascript:alert(1))" }]);
    expect(inline("2*3*4")).toEqual([{ t: "text", v: "2*3*4" }]);
  });

  test("a plain reply is one paragraph with its line breaks", () => {
    expect(markdown("好了\n改好了")).toEqual([{ t: "p", c: [{ t: "text", v: "好了" }, { t: "br" }, { t: "text", v: "改好了" }] }]);
  });
});
