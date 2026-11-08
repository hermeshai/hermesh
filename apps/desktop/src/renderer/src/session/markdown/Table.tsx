import { useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../icons";
import { useStore } from "../../store";
import { tableMenu } from "../content";
import { DraftContext } from "../scope";
import { Dialog } from "./Dialog";

const tsvCell = (value: string) => /[\t\n"]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value;

/** A table as wide as its content, from the prose's left edge: columns only grow while it is being written, so finishing moves nothing. */
export function MarkdownTable({ children, source }: { children: ReactNode; source: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const draft = useContext(DraftContext);
  const scroll = useRef<HTMLDivElement>(null);
  const widths = useRef<number[]>([]);
  const [expanded, setExpanded] = useState(false);
  const expandButton = useRef<HTMLButtonElement>(null);
  const wasExpanded = useRef(false);
  useEffect(() => {
    if (wasExpanded.current && !expanded) expandButton.current?.focus();
    wasExpanded.current = expanded;
  }, [expanded]);
  const [overflow, setOverflow] = useState(false);
  const [copied, setCopied] = useState(false);
  useLayoutEffect(() => {
    const el = scroll.current;
    if (!el) return;
    const head = [...el.querySelectorAll<HTMLTableCellElement>("th")];
    if (draft) {
      if (widths.current.length !== head.length) widths.current = [];
      head.forEach((cell, i) => {
        widths.current[i] = Math.max(widths.current[i] ?? 0, Math.ceil(cell.getBoundingClientRect().width));
        cell.style.minWidth = `${widths.current[i]}px`;
      });
    }
    const measure = () => {
      // The widths a finished table kept only stay while they fit; narrowing the window lets it wrap again.
      if (!draft && el.scrollWidth > el.clientWidth + 1) head.forEach((cell) => { cell.style.minWidth = ""; });
      setOverflow(el.scrollWidth > el.clientWidth + 1);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    if (el.firstElementChild) observer.observe(el.firstElementChild);
    return () => observer.disconnect();
  }, [children, draft, expanded]);
  /** Tab-separated for Excel and plain text, a table for whatever takes rich text: the paste picks. */
  const copy = async () => {
    const table = scroll.current?.querySelector("table");
    if (!table) return;
    const tsv = [...table.rows].map((row) => [...row.cells].map((cell) => tsvCell(cell.innerText)).join("\t")).join("\n");
    const rich = table.cloneNode(true) as HTMLTableElement;
    rich.querySelectorAll("th").forEach((cell) => { cell.style.minWidth = ""; });
    try {
      await navigator.clipboard.write([new ClipboardItem({ "text/plain": new Blob([tsv], { type: "text/plain" }), "text/html": new Blob([rich.outerHTML], { type: "text/html" }) })]);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch { st.show(t("markdown.copyFailed")); }
  };
  const table = <div ref={scroll} className="md-table-scroll" role={overflow ? "region" : undefined} aria-label={overflow ? t("markdown.table") : undefined} tabIndex={overflow ? 0 : undefined}><table>{children}</table></div>;
  const controls = <div className="md-tools md-table-tools">
    {!draft && <button onClick={() => void copy()} title={t("markdown.copyTable")} aria-label={t("markdown.copyTable")}><Icon name={copied ? "check" : "copy"} /></button>}
    <button ref={expandButton} onClick={() => setExpanded(!expanded)} title={t(expanded ? "markdown.close" : "markdown.expand")} aria-label={t(expanded ? "markdown.close" : "markdown.expand")}><Icon name={expanded ? "x" : "expand"} /></button>
  </div>;
  return <div
    className="md-table md-wide"
    onContextMenu={(e) => {
      if (draft) return;
      e.preventDefault();
      e.stopPropagation();
      void tableMenu(st, () => void copy(), source);
    }}
  >
    {expanded
      ? <><button className="md-table-return" onClick={() => setExpanded(false)}>{t("markdown.tableOpen")}</button><Dialog label={t("markdown.table")} width={1100} className="md-expanded" onClose={() => setExpanded(false)}><div className="md">{controls}{table}</div></Dialog></>
      : <div className="md-table-box">{controls}{table}</div>}
  </div>;
}
