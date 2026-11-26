import { Component, createContext, lazy, memo, Suspense, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remend from "remend";
import remarkMath from "remark-math";
import remarkDirective from "remark-directive";
import type { PluggableList } from "unified";
import type { Root as HtmlRoot, Element as HtmlElement, RootContent as HtmlContent } from "hast";
import { sameImagePath, sessionImageRef } from "@hermesh/her-client";
import { createMarkdownSlices, pendingText } from "./markdown/parse";
import { anchors, contentExtensions } from "./markdown/plugins";
import { texMath } from "./markdown/tex";
import { MarkdownTable } from "./markdown/Table";
import { CodeBlock, InlineCode, Link } from "./content";
import { FilesHomeContext } from "./files/files";
import { ReplyImagesContext, scanReplyImages, type ReplyImageItem } from "./files/images";
import { SessionImageSlot } from "./files/InlineImages";
import { DraftContext } from "./scope";

const MermaidBlock = lazy(() => import("./markdown/Mermaid"));
const SourceContext = createContext("");

/**
 * An agent's reply as GitHub Markdown with formulas, diagrams, callouts and footnotes, rendered as
 * React elements (raw HTML stays text, images show their alt text, so a reply never loads anything).
 * One component tree throughout streaming and completion: AST-aware slices keep settled blocks
 * memoized, and the block still being written is closed (remend) so nothing shows as raw Markdown
 * on its way (docs/reply-streaming.md); document-scoped references take the whole-document path.
 */
export function Markdown({ text, streaming = false, mode = "chat" }: { text: string; streaming?: boolean; mode?: "chat" | "document" | "compact" }) {
  const prefix = useId();
  const home = useContext(FilesHomeContext);
  const inSession = Boolean(home) && mode === "chat";
  const split = useRef(createMarkdownSlices());
  const blocks = useMemo(() => split.current(text, streaming), [text, streaming]);
  const replyImages = useMemo(() => (inSession ? scanReplyImages(text) : null), [text, inSession]);
  const explicitKey = replyImages ? replyImages.explicit.join("\0") : "";
  // Pre-compute per-block claimed link paths so `[shot.png](p)` repeated across two blocks in one reply only expands once.
  const blockAllowedLinks = useMemo(() => {
    if (!inSession || !replyImages) return blocks.map(() => "");
    const claimed = [...replyImages.explicit];
    return blocks.map((b) => {
      if (b.pending) return "";
      const local = scanReplyImages(b.text);
      const allowed: string[] = [];
      for (const g of local.gallery) {
        if (local.explicit.some((e) => sameImagePath(e, g.path))) continue;
        if (claimed.some((c) => sameImagePath(c, g.path))) continue;
        claimed.push(g.path);
        allowed.push(g.path);
      }
      return allowed.join("\0");
    });
  }, [blocks, inSession, replyImages]);

  return (
    <ReplyImagesContext.Provider value={replyImages}>
      <div className="md sel-text" data-mode={mode}>
        {blocks.map((block, idx) => (
          <Block
            key={block.id}
            text={block.text}
            pending={block.pending}
            prefix={prefix}
            offset={block.id}
            mode={mode}
            inSession={inSession}
            explicitKey={explicitKey}
            allowedLinksKey={blockAllowedLinks[idx] ?? ""}
          />
        ))}
      </div>
    </ReplyImagesContext.Provider>
  );
}

export function DraftMarkdown({ text }: { text: string }) {
  return <Markdown text={text} streaming />;
}

const Block = memo(function Block({
  text,
  pending,
  prefix,
  offset,
  mode,
  inSession,
  explicitKey,
  allowedLinksKey,
}: {
  text: string;
  pending: boolean;
  prefix: string;
  offset: number;
  mode: string;
  inSession: boolean;
  explicitKey: string;
  allowedLinksKey: string;
}) {
  const { t } = useTranslation("session");
  const [mathPlugin, setMathPlugin] = useState<PluggableList>([]);
  const hasMath = /\$\$|```math|~~~math|\\[([]/.test(text);
  useEffect(() => {
    if (!hasMath) return;
    let live = true;
    Promise.all([import("rehype-katex"), import("katex/dist/katex.min.css")])
      .then(([module]) => {
        if (live) setMathPlugin([[module.default, { output: "htmlAndMathml", throwOnError: false, strict: "ignore", trust: false, maxExpand: 1000, maxSize: 20, errorColor: "inherit" }]]);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [hasMath]);
  const rehypePlugins = useMemo<PluggableList>(() => {
    const list: PluggableList = [...mathPlugin, [anchors, { prefix, block: offset }]];
    if (inSession) list.push([rehypeSessionImages, { pending, explicitKey, allowedLinksKey }]);
    return list;
  }, [mathPlugin, prefix, offset, inSession, pending, explicitKey, allowedLinksKey]);
  const shown = pending ? remend(pendingText(text), { linkMode: "text-only", katex: true }) : text;
  return (
    <DraftContext.Provider value={pending}>
      <SourceContext.Provider value={text}>
        <RenderBoundary source={text}>
          <ReactMarkdown
            remarkPlugins={mode === "document" ? documentPlugins : plugins}
            rehypePlugins={rehypePlugins}
            components={components}
            urlTransform={asWritten}
            remarkRehypeOptions={{ footnoteLabel: t("markdown.footnotes"), footnoteBackLabel: t("markdown.backToReference") }}
          >
            {shown}
          </ReactMarkdown>
        </RenderBoundary>
      </SourceContext.Provider>
    </DraftContext.Provider>
  );
});

/** A block that fails to render shows its source instead, until its source changes. */
class RenderBoundary extends Component<{ source: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidUpdate(previous: { source: string }) {
    if (this.state.failed && previous.source !== this.props.source) this.setState({ failed: false });
  }
  render() {
    return this.state.failed ? <pre>{this.props.source}</pre> : this.props.children;
  }
}

const web = /^https?:\/\//i;

/** Targets as written: non-http targets stay intact for Link to inspect; only http and https ever become real links. */
const asWritten = (url: string) => url;

type HNode = { type: string; tagName?: string; value?: string; properties?: Record<string, unknown>; children?: HNode[] };
const textOf = (n: HNode): string => (n.type === "text" ? (n.value ?? "") : (n.children ?? []).map(textOf).join(""));

/** A `pre`'s code: its language (from `language-*`) and text. */
function codeOf(pre: HNode | undefined): { lang: string; text: string } | null {
  const code = pre?.children?.find((c) => c.type === "element" && c.tagName === "code");
  if (!code) return null;
  const names = code.properties?.className;
  const lang = (Array.isArray(names) ? names : [names]).map((c) => /^language-(\S+)/.exec(String(c ?? ""))?.[1]).find(Boolean) ?? "";
  return { lang, text: textOf(code).replace(/\n$/, "") };
}

const components: Components = {
  a: ({ href, children, node, ...props }) => {
    if (href?.startsWith("#")) return <Anchor href={href} {...props}>{children}</Anchor>;
    return <Link href={href}>{children}</Link>;
  },
  img: ({ alt }) => <span>{alt}</span>,
  div: ({ node, children, ...props }) => {
    const raw = (node?.properties as Record<string, unknown> | undefined)?.["data-simg"] ?? (props as Record<string, unknown>)["data-simg"];
    if (typeof raw === "string") return <SessionImageSlot raw={raw} />;
    return <div {...props}>{children}</div>;
  },
  // Inline code only: a block's is drawn by its `pre`, coloured when it names its language.
  code: ({ children }) => <InlineCode>{children}</InlineCode>,
  pre: ({ node, children }) => {
    const code = codeOf(node as HNode | undefined);
    if (code?.lang.toLowerCase() === "mermaid") return <Suspense fallback={<CodeBlock lang="mermaid" text={code.text} />}><MermaidBlock text={code.text} /></Suspense>;
    return code ? <CodeBlock lang={code.lang} text={code.text} /> : <pre>{children}</pre>;
  },
  table: ({ children, node }) => <Table sourceStart={node?.position?.start.offset} sourceEnd={node?.position?.end.offset}>{children}</Table>,
  details: ({ children, node }) => <Disclosure title={String(node?.properties?.dataTitle ?? node?.properties?.["data-title"] ?? "")}>{children}</Disclosure>,
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
        const label = c.type === "link" && c.children?.length === 1 && c.children[0].type === "text" ? (c.children[0].value ?? "") : "";
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

// A Markdown file reads as written: its single line breaks are not a chat's.
const documentPlugins: PluggableList = [[remarkGfm, { singleTilde: false }], [remarkMath, { singleDollarTextMath: false }], remarkDirective, texMath, contentExtensions];
const plugins: PluggableList = [...documentPlugins, chatText];

function makeImageSlot(items: ReplyImageItem[], fromLinks: boolean): HtmlElement {
  return {
    type: "element",
    tagName: "div",
    properties: { "data-simg": JSON.stringify({ items, fromLinks }) },
    children: [],
  };
}

function isIgnorableInline(n: HtmlContent): boolean {
  if (n.type === "text") return n.value.trim() === "";
  return n.type === "element" && n.tagName === "br";
}

function trimBr(nodes: HtmlContent[]): HtmlContent[] {
  let start = 0;
  let end = nodes.length;
  while (start < end && isIgnorableInline(nodes[start]!)) start++;
  while (end > start && isIgnorableInline(nodes[end - 1]!)) end--;
  return nodes.slice(start, end);
}

/**
 * Lifts session `![alt](path)` images out of `<p>` into block-level `<SessionImageSlot>` cards,
 * and attaches deduplicated `[label](path.png)` image strips below their enclosing block.
 */
function rehypeSessionImages(opts: { pending: boolean; explicitKey: string; allowedLinksKey: string }) {
  const explicit = opts.explicitKey ? opts.explicitKey.split("\0") : [];
  const allowedLinks = opts.allowedLinksKey ? opts.allowedLinksKey.split("\0") : [];
  return (tree: HtmlRoot) => {
    const claimed = [...explicit];
    const transformChildren = (parent: HtmlRoot | HtmlElement, topLevel: boolean) => {
      const next: HtmlContent[] = [];
      for (const child of parent.children as HtmlContent[]) {
        if (child.type !== "element") {
          next.push(child);
          continue;
        }
        if (child.tagName === "p") {
          const parts: HtmlContent[] = [];
          let proseBuf: HtmlContent[] = [];
          let imgBuf: ReplyImageItem[] = [];
          const flushProse = () => {
            const trimmed = trimBr(proseBuf);
            proseBuf = [];
            if (trimmed.length > 0) parts.push({ ...child, children: trimmed as HtmlElement["children"] });
          };
          const flushImgs = () => {
            if (imgBuf.length > 0) {
              parts.push(makeImageSlot(imgBuf, false));
              imgBuf = [];
            }
          };
          for (const inl of child.children as HtmlContent[]) {
            if (inl.type === "element" && inl.tagName === "img") {
              const src = String(inl.properties?.src ?? "");
              const alt = String(inl.properties?.alt ?? "").trim();
              const path = sessionImageRef(src);
              if (path) {
                flushProse();
                if (!imgBuf.some((x) => sameImagePath(x.path, path))) imgBuf.push({ path, caption: alt });
                continue;
              }
            }
            if (imgBuf.length > 0 && isIgnorableInline(inl)) continue;
            flushImgs();
            proseBuf.push(inl);
          }
          flushImgs();
          flushProse();
          next.push(...parts);
          if (topLevel && !opts.pending && allowedLinks.length > 0) {
            const linked = collectLinkedImages(child, allowedLinks, claimed);
            if (linked.length > 0) next.push(makeImageSlot(linked, true));
          }
          continue;
        }
        if (child.tagName === "ul" || child.tagName === "ol" || child.tagName === "blockquote") {
          transformChildren(child, false);
          next.push(child);
          if (topLevel && !opts.pending && allowedLinks.length > 0) {
            const linked = collectLinkedImages(child, allowedLinks, claimed);
            if (linked.length > 0) next.push(makeImageSlot(linked, true));
          }
          continue;
        }
        next.push(child);
      }
      parent.children = next as typeof parent.children;
    };
    transformChildren(tree, true);
  };
}

function collectLinkedImages(node: HtmlElement, allowedLinks: string[], claimed: string[]): ReplyImageItem[] {
  const out: ReplyImageItem[] = [];
  const walk = (el: HtmlContent) => {
    if (el.type !== "element" || el.tagName === "code" || el.tagName === "pre") return;
    if (el.tagName === "a") {
      const href = String(el.properties?.href ?? "");
      const path = sessionImageRef(href);
      if (
        path &&
        allowedLinks.some((a) => sameImagePath(a, path)) &&
        !claimed.some((c) => sameImagePath(c, path)) &&
        !out.some((o) => sameImagePath(o.path, path))
      ) {
        claimed.push(path);
        out.push({ path, caption: textOf(el as unknown as HNode).trim() });
      }
      return;
    }
    for (const c of el.children as HtmlContent[]) walk(c);
  };
  walk(node);
  return out;
}

function Table({ children, sourceStart, sourceEnd }: { children: ReactNode; sourceStart?: number; sourceEnd?: number }) {
  const source = useContext(SourceContext);
  return <MarkdownTable source={source.slice(sourceStart, sourceEnd)}>{children}</MarkdownTable>;
}

function Disclosure({ title, children }: { title: string; children: ReactNode }) {
  const { t } = useTranslation("session");
  return (
    <details className="md-details">
      <summary>{title || t("markdown.details")}</summary>
      {children}
    </details>
  );
}

/** A link within the reply (a footnote, a heading): it scrolls there, opening what folds it. */
function Anchor({ href, children, ...props }: { href: string; children?: ReactNode } & React.AnchorHTMLAttributes<HTMLAnchorElement>) {
  const draft = useContext(DraftContext);
  return (
    <a
      {...props}
      href={draft ? undefined : href}
      onClick={(e) => {
        e.preventDefault();
        if (draft) return;
        const root = e.currentTarget.closest(".md");
        let id = href.slice(1);
        try {
          id = decodeURIComponent(id);
        } catch {
          /* literal fragment */
        }
        const target = root?.querySelector<HTMLElement>(`[id="${CSS.escape(id)}"], [data-heading="${CSS.escape(id)}"]`);
        if (!target) return;
        for (let parent = target.parentElement; parent && parent !== root; parent = parent.parentElement) if (parent instanceof HTMLDetailsElement) parent.open = true;
        target.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
        target.tabIndex = -1;
        target.focus({ preventScroll: true });
      }}
    >
      {children}
    </a>
  );
}
