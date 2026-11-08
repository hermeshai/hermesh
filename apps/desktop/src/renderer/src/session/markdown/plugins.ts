import type { Root, RootContent } from "mdast";
import type { Root as HtmlRoot, Element } from "hast";

type Node = Root | RootContent;
function walk(node: Node, fn: (node: Node) => void) {
  fn(node);
  if ("children" in node) for (const child of node.children) walk(child as Node, fn);
}

/** Fixed components only; directives never enable arbitrary HTML or scripts. */
export function contentExtensions() {
  return (tree: Root) => walk(tree, (node) => {
    if (node.type === "blockquote") {
      const first = node.children[0];
      const text = first?.type === "paragraph" ? first.children[0] : undefined;
      const mark = text?.type === "text" ? /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\](?:\n|$)/.exec(text.value) : null;
      if (mark && text?.type === "text") {
        text.value = text.value.slice(mark[0].length);
        node.data = { ...node.data, hProperties: { className: ["md-callout"], "data-kind": mark[1].toLowerCase() } };
      }
    }
    const directive = node as unknown as { type: string; name: string; attributes?: Record<string, string>; data?: Record<string, unknown> };
    if (directive.type === "containerDirective" && directive.name === "details") {
      directive.data = { hName: "details", hProperties: { "data-title": directive.attributes?.title ?? "" } };
    }
    // KaTeX is bounded before it gets to expand any input.
    const math = node as unknown as { type: string; value?: string; lang?: string; data?: unknown };
    if ((math.type === "math" || math.type === "inlineMath" || (math.type === "code" && math.lang === "math")) && (math.value?.length ?? 0) > 5000) {
      math.type = math.type === "inlineMath" ? "inlineCode" : "code";
      delete math.data;
      math.lang = "latex";
    }
  });
}

const textOf = (node: Element | HtmlRoot | { type: string; value?: string }): string =>
  "children" in node ? node.children.map((c) => textOf(c as Element)).join("") : ("value" in node ? node.value ?? "" : "");
export const slug = (text: string) => text.toLowerCase().trim().replace(/[^\p{L}\p{N}\s_-]/gu, "").replace(/\s+/g, "-");

/** Namespace footnotes/heading IDs per message, including every backreference. */
export function anchors(options: { prefix: string; block: number }) {
  return (tree: HtmlRoot) => {
    let heading = 0;
    const visit = (node: HtmlRoot | Element) => {
      if (node.type === "element") {
        const p = node.properties;
        if (typeof p.id === "string") p.id = `${options.prefix}-${p.id}`;
        if (typeof p.href === "string" && p.href.startsWith("#user-content-")) p.href = `#${options.prefix}-${p.href.slice(1)}`;
        if (Array.isArray(p.ariaDescribedBy)) p.ariaDescribedBy = p.ariaDescribedBy.map((id) => `${options.prefix}-${id}`);
        if (/^h[1-6]$/.test(node.tagName) && !p.id) {
          p["data-heading"] = slug(textOf(node));
          p.id = `${options.prefix}-h-${options.block}-${heading++}`;
          p.tabIndex = -1;
        }
      }
      for (const child of node.children) if (child.type === "element") visit(child);
    };
    visit(tree);
  };
}
