import { memo } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remend from "remend";
import { splitBlocks, withoutOpenTable } from "@hermesh/core";

/**
 * An agent's reply as GitHub Markdown, rendered as React elements (raw HTML
 * stays text). Links open in the browser, http and https only; images show
 * their alt text, so a reply never loads anything.
 */
export function Markdown({ text }: { text: string }) {
  return (
    <div className="md sel-text">
      <ReactMarkdown remarkPlugins={plugins} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
}

/**
 * A reply the agent is still writing (docs/reply-streaming.md): its
 * finished blocks as they will stay, each parsed once, then the last one
 * with what is still open closed (remend), or without a table that has no
 * delimiter row yet, so nothing shows as raw Markdown on its way.
 */
export function DraftMarkdown({ text }: { text: string }) {
  const blocks = splitBlocks(text);
  const last = withoutOpenTable(blocks.pop() ?? "");
  return (
    <div className="md sel-text">
      {blocks.map((b, i) => (
        <Block key={i} text={b} />
      ))}
      {last.trim() !== "" && <Block key={blocks.length} text={remend(last, { linkMode: "text-only", katex: false })} />}
    </div>
  );
}

const Block = memo(function Block({ text }: { text: string }) {
  return (
    <ReactMarkdown remarkPlugins={plugins} components={components}>
      {text}
    </ReactMarkdown>
  );
});

const web = /^https?:\/\//i;

const components: Components = {
  a: ({ href, children }) =>
    href && web.test(href) ? <a href={href} target="_blank" rel="noreferrer">{children}</a> : <span>{children}</span>,
  img: ({ alt }) => <span>{alt}</span>,
  table: ({ children }) => (
    <div className="md-table">
      <table>{children}</table>
    </div>
  ),
};

type Node = { type: string; value?: string; url?: string; children?: Node[] };

/** Chinese text and punctuation: a bare link ends before them ("见 https://x.io，然后" links https://x.io). */
const wide = /[　-〿㐀-鿿＀-￯]/;

/**
 * Agents write for chat: a single newline is a line break, and a bare link
 * stops where Chinese text starts (GFM would carry it to the next space).
 */
function chatText() {
  return (tree: Node) => {
    const walk = (n: Node) => {
      if (!n.children) return;
      const out: Node[] = [];
      for (const c of n.children) {
        if (c.type === "text" && c.value?.includes("\n")) {
          c.value.split("\n").forEach((v, i) => {
            if (i > 0) out.push({ type: "break" });
            if (v) out.push({ type: "text", value: v });
          });
          continue;
        }
        const label = c.type === "link" && c.children?.length === 1 && c.children[0].type === "text" ? c.children[0].value ?? "" : "";
        const cut = label.search(wide);
        if (cut > 0 && (web.test(label) || label.startsWith("www."))) {
          const shown = label.slice(0, cut);
          out.push({ ...c, url: web.test(shown) ? shown : `http://${shown}`, children: [{ type: "text", value: shown }] });
          out.push({ type: "text", value: label.slice(cut) });
          continue;
        }
        walk(c);
        out.push(c);
      }
      n.children = out;
    };
    walk(tree);
  };
}

const plugins = [remarkGfm, chatText];
