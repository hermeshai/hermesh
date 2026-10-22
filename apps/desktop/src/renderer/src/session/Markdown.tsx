import type { ReactNode } from "react";
import { markdown, type Block, type Inline } from "@hermesh/core";

/** An agent's reply as Markdown, made of text nodes only (never HTML); links open in the browser. */
export function Markdown({ text }: { text: string }) {
  return <div className="md sel-text">{markdown(text).map(block)}</div>;
}

function block(b: Block, i: number): ReactNode {
  switch (b.t) {
    case "p":
      return <p key={i}>{b.c.map(inline)}</p>;
    case "h":
      return b.level === 1 ? <h3 key={i}>{b.c.map(inline)}</h3> : b.level === 2 ? <h4 key={i}>{b.c.map(inline)}</h4> : <h5 key={i}>{b.c.map(inline)}</h5>;
    case "ul":
      return <ul key={i}>{b.items.map((it, j) => <li key={j}>{it.map(inline)}</li>)}</ul>;
    case "ol":
      return <ol key={i} start={b.start}>{b.items.map((it, j) => <li key={j}>{it.map(inline)}</li>)}</ol>;
    case "quote":
      return <blockquote key={i}>{b.c.map(inline)}</blockquote>;
    case "pre":
      return <pre key={i}><code>{b.v}</code></pre>;
  }
}

function inline(n: Inline, i: number): ReactNode {
  switch (n.t) {
    case "text":
      return n.v;
    case "br":
      return <br key={i} />;
    case "code":
      return <code key={i}>{n.v}</code>;
    case "strong":
      return <b key={i}>{n.c.map(inline)}</b>;
    case "em":
      return <em key={i}>{n.c.map(inline)}</em>;
    case "link":
      return <a key={i} href={n.href} target="_blank" rel="noreferrer">{n.c.map(inline)}</a>;
  }
}
