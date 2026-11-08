import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { SkillFileInfo, SkillsFilesResult, SkillsReadResult } from "@hermesh/her-protocol";
import { Lightbox } from "../components";
import { errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";

// A skill's files: its folder as a tree on the left, the
// open file on the right. A Markdown or SVG file shows rendered, with a switch to its source;
// an image shows as itself, larger on a click; other text shows as it is; any other file that
// is not text says so.

type Node = SkillFileInfo & { name: string; kids: Node[] };
type Read = { path: string; result?: SkillsReadResult; error?: string };

/** SKILL.md first, then folders, then files, each by name. */
const rank = (n: Node) => (n.path === "SKILL.md" ? 0 : n.dir ? 1 : 2);

function tree(files: SkillFileInfo[]): Node[] {
  const root: Node[] = [];
  const dirs = new Map<string, Node[]>([["", root]]);
  // Sorted by path, so a folder comes before what is in it.
  for (const f of files) {
    const cut = f.path.lastIndexOf("/");
    const node: Node = { ...f, name: f.path.slice(cut + 1), kids: [] };
    (dirs.get(cut < 0 ? "" : f.path.slice(0, cut)) ?? root).push(node);
    if (f.dir) dirs.set(f.path, node.kids);
  }
  const sort = (nodes: Node[]) => {
    nodes.sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
    for (const n of nodes) sort(n.kids);
  };
  sort(root);
  return root;
}

const isMd = (path: string) => /\.(md|markdown)$/i.test(path);
const isSvg = (path: string) => /\.svg$/i.test(path);

/** The text of a Markdown file without its header, which the page already shows. */
function body(text: string): string {
  return text.replace(/^---\r?\n[\s\S]*?\r?\n---(\r?\n|$)/, "");
}

/** The text of a Markdown file, its header set apart. */
function Source({ text }: { text: string }) {
  const m = /^(---\r?\n[\s\S]*?\r?\n---)(\r?\n|$)/.exec(text);
  return (
    <pre className="cz-code">
      {m ? (
        <>
          <span className="fm">{m[1]}</span>
          {text.slice(m[1].length)}
        </>
      ) : (
        text
      )}
    </pre>
  );
}

export function SkillContents({ dir, files, error }: { dir: string; files: SkillsFilesResult | null; error: string | null }) {
  const { t } = useTranslation("customize");
  const { rpc } = useHer();
  const nodes = useMemo(() => (files ? tree(files.files) : []), [files]);
  const [open, setOpen] = useState("SKILL.md");
  const [closed, setClosed] = useState<ReadonlySet<string>>(new Set());
  const [source, setSource] = useState(false);
  const [read, setRead] = useState<Read | null>(null);
  const [large, setLarge] = useState<string | null>(null);
  // SKILL.md came with the list; any other file is read when it is opened.
  const skillMd: Read | null = open === "SKILL.md" && files ? { path: open, result: { text: files.skill_md, truncated: files.truncated, binary: false } } : null;
  useEffect(() => {
    if (!rpc || open === "SKILL.md") return;
    let current = true;
    setRead(null);
    rpc.request("skills/read", { path: dir, file: open }).then(
      (result) => current && setRead({ path: open, result }),
      (e) => current && setRead({ path: open, error: errorText(e) }),
    );
    return () => {
      current = false;
    };
  }, [dir, open, rpc]);
  if (!files) return <div className="cz-wait">{error ? t("skill.noFiles", { error }) : t("loading")}</div>;

  const rows: { node: Node; depth: number }[] = [];
  const walk = (list: Node[], depth: number) => {
    for (const node of list) {
      rows.push({ node, depth });
      if (node.dir && !closed.has(node.path)) walk(node.kids, depth + 1);
    }
  };
  walk(nodes, 0);
  const toggle = (path: string) =>
    setClosed((s) => {
      const next = new Set(s);
      if (!next.delete(path)) next.add(path);
      return next;
    });

  const shown = skillMd ?? (read?.path === open ? read : null);
  const text = shown?.result && !shown.result.binary ? shown.result.text : null;
  const image = shown?.result?.image;
  const md = isMd(open);
  const svg = isSvg(open);
  // An image the page shows: one her read, or an SVG's text in preview.
  const picture = image ? `data:${image.mime_type};base64,${image.data}` : svg && text !== null && !source ? `data:image/svg+xml;charset=utf-8,${encodeURIComponent(text)}` : null;
  let view;
  if (!shown) view = <div className="cz-wait">{t("loading")}</div>;
  else if (shown.error) view = <div className="cz-vnote">{t("skill.noFiles", { error: shown.error })}</div>;
  else if (picture)
    view = (
      <button className="cz-vimg" title={t("skill.enlarge")} aria-label={t("skill.enlarge")} onClick={() => setLarge(picture)}>
        <img src={picture} alt={open} draggable={false} />
      </button>
    );
  else if (text === null) view = <div className="cz-vnote">{t("skill.binary")}</div>;
  else if (md && !source)
    view = (
      <div className="cz-vmd md">
        <Markdown remarkPlugins={[remarkGfm]}>{body(text)}</Markdown>
      </div>
    );
  else if (md) view = <Source text={text} />;
  else view = <pre className="cz-code">{text}</pre>;

  // A skill that is only its SKILL.md needs no tree.
  const single = files.files.length === 1;
  return (
    <div className={`cz-contents ${single ? "single" : ""}`}>
      {!single && (
        <nav className="cz-tree" aria-label={t("skill.filesTitle")}>
          {rows.map(({ node, depth }) => (
            <button
              key={node.path}
              className={`cz-trow ${node.path === open ? "on" : ""}`}
              style={{ paddingLeft: 8 + depth * 14 }}
              title={node.path}
              aria-expanded={node.dir ? !closed.has(node.path) : undefined}
              onClick={() => (node.dir ? toggle(node.path) : setOpen(node.path))}
            >
              {node.dir ? <Icon name={closed.has(node.path) ? "chevR" : "chevD"} /> : <i className="cz-tsp" />}
              <span>{node.name}</span>
            </button>
          ))}
        </nav>
      )}
      <section className="cz-viewer">
        <div className="cz-vbar">
          <span className="mono" title={open}>
            /{open}
          </span>
          {(md || svg) && text !== null && (
            <div className="seg cz-segsm" role="group">
              <button className={source ? "" : "on"} title={t("skill.preview")} aria-label={t("skill.preview")} aria-pressed={!source} onClick={() => setSource(false)}>
                <Icon name="eye" />
              </button>
              <button className={source ? "on" : ""} title={t("skill.raw")} aria-label={t("skill.raw")} aria-pressed={source} onClick={() => setSource(true)}>
                <Icon name="code" />
              </button>
            </div>
          )}
        </div>
        <div className="cz-vbody">
          {shown?.result?.truncated && <div className="cz-vnote cut">{t("skill.truncated")}</div>}
          {view}
        </div>
      </section>
      {large && <Lightbox src={large} onClose={() => setLarge(null)} />}
    </div>
  );
}
