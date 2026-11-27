import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";
import { fileAppearance, splitFileName } from "@hermesh/her-client";
import { FileIcon, Icon } from "../../icons";
import { sizeText } from "./files";
import { listDir, openFromTree, toggleDir, type Panel } from "./panel";

// The session's folder as a tree (FinxWork's files/Tree.tsx): each folder read when it is opened,
// the rows to show worked out from what is open, and only the rows in sight drawn. The folder as
// it is now; when the worktree is gone, as the last turn left it.

type Row = { path: string; name: string; dir: boolean; size: bigint; depth: number; parent: string };

const ROW = 26;
const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

function rowsOf(p: Panel): Row[] {
  const rows: Row[] = [];
  const walk = (dir: string, depth: number) => {
    for (const e of p.dirs.get(dir)?.list?.entries ?? []) {
      const path = join(dir, e.name);
      rows.push({ path, name: e.name, dir: e.dir, size: e.size, depth, parent: dir });
      if (e.dir && p.expanded.has(path)) walk(path, depth + 1);
    }
  };
  walk("", 0);
  return rows;
}

/** `wide`: the tree alone in its tab (项目文件), files with their sizes. */
export function Tree({ p, activePath, wide = false }: { p: Panel; activePath: string | null; wide?: boolean }) {
  const { t } = useTranslation("session");
  const scroller = useRef<HTMLDivElement>(null);
  const rows = useMemo(() => rowsOf(p), [p.rev]);
  const [focus, setFocus] = useState(0);
  const v = useVirtualizer({ count: rows.length, getScrollElement: () => scroller.current, estimateSize: () => ROW, overscan: 8, getItemKey: (i) => rows[i]?.path ?? i });
  useEffect(() => void (p.dirs.has("") || listDir(p, "")), [p.session]);
  const active = activePath ? rows.findIndex((r) => r.path === activePath) : -1;
  useEffect(() => {
    if (active < 0) return;
    setFocus(active);
    v.scrollToIndex(active, { align: "auto" });
  }, [active, activePath]);
  const open = (r: Row) => (r.dir ? toggleDir(p, r.path) : openFromTree(p, r.path));
  const move = (i: number) => {
    const next = Math.max(0, Math.min(rows.length - 1, i));
    setFocus(next);
    v.scrollToIndex(next, { align: "auto" });
  };
  const root = p.dirs.get("");
  const turn = root?.list?.folderGone ? root.list.turn : 0;
  return (
    <div
      className={`sft ${wide ? "wide" : ""}`}
      ref={scroller}
      role="tree"
      aria-label={t("files.tree")}
      tabIndex={0}
      data-testid="file-tree"
      onKeyDown={(e) => {
        const r = rows[focus];
        if (e.key === "ArrowDown") move(focus + 1);
        else if (e.key === "ArrowUp") move(focus - 1);
        else if (e.key === "ArrowRight" && r?.dir && !p.expanded.has(r.path)) toggleDir(p, r.path);
        else if (e.key === "ArrowLeft" && r) {
          if (r.dir && p.expanded.has(r.path)) toggleDir(p, r.path);
          else move(rows.findIndex((x) => x.path === r.parent));
        } else if (e.key === "Enter" && r) open(r);
        else return;
        e.preventDefault();
      }}
    >
      {turn > 0 && <div className="sft-note">{t("files.treeGone", { turn })}</div>}
      {root?.error != null && !root.list && <div className="sft-note">{t("files.treeFailed")}</div>}
      <div style={{ height: v.getTotalSize(), position: "relative" }}>
        {v.getVirtualItems().map((item) => {
          const r = rows[item.index];
          if (!r) return null;
          const expanded = r.dir && p.expanded.has(r.path);
          const appearance = fileAppearance(r.name, r.dir);
          const parts = splitFileName(r.name, r.dir);
          return (
            <div
              key={item.key}
              role="treeitem"
              aria-level={r.depth + 1}
              aria-expanded={r.dir ? expanded : undefined}
              aria-selected={r.path === activePath}
              title={r.path}
              data-path={r.path}
              className={`sft-row ${r.dir ? "dir" : ""} ${appearance.dimmed ? "dim" : ""} ${r.path === activePath ? "on" : ""} ${item.index === focus ? "focus" : ""}`}
              style={{ transform: `translateY(${item.start}px)`, paddingLeft: 8 + r.depth * 14 }}
              onClick={() => {
                setFocus(item.index);
                open(r);
              }}
            >
              {r.depth > 0 &&
                Array.from({ length: r.depth }, (_, d) => <span key={d} className="sft-guide" style={{ left: 13 + d * 14 }} />)}
              {r.dir ? <Icon name="chevD" className={`chev ${expanded ? "open" : ""}`} /> : <span className="sft-sp" />}
              <FileIcon name={r.name} dir={r.dir} open={expanded} />
              <span className="sft-name">
                {parts.stem}
                {parts.sub && <span className="sft-sub">{parts.sub}</span>}
                {parts.ext && <span className="sft-ext">{parts.ext}</span>}
              </span>
              {wide && !r.dir && <span className="sft-size">{sizeText(r.size)}</span>}
            </div>
          );
        })}
      </div>
    </div>
  );
}
