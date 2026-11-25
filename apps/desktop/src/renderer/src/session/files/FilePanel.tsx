import { useEffect, useLayoutEffect } from "react";
import { useTranslation } from "react-i18next";
import { fileView } from "@hermesh/her-client";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore } from "../../store";
import type { FilesHome } from "./files";
import { Card, fileError, Loading, PageBody, PdfBody, TextBody, VersionBar } from "./FileView";
import { fileMenu } from "./open";
import { activate, closeTab, refreshTree, setMax, setOpen, setPreview, setTree, switchRun, usePanel, type Tab } from "./panel";
import { usePanelWidth } from "./panelWidth";
import { Tree } from "./Tree";

// The file panel (docs/session-files.md §3.4, FinxWork's files/FilePanel.tsx): the session's open
// files as tabs, with the file's tools (preview or source, its menu, the tree) at the strip's end;
// the file on the left, read-only, with the folder tree on its right; the bar under it says when a
// reply's file is as its turn left it.

/** What the conversation keeps beside the panel at least. */
const conversationRoom = () => 160;

export function FilePanel({ home, running }: { home: FilesHome; running: boolean }) {
  const { t } = useTranslation("session");
  const p = usePanel(home.session);
  const tab = p.tabs.find((x) => x.key === p.active) ?? null;
  const { panel, width, resizing, style, grip } = usePanelWidth("hermesh.filePanelWidth", conversationRoom);
  const bare = p.tabs.length === 0;
  const wide = p.max && !bare;

  // The tree, the first time: beside the code when there is room for both.
  useLayoutEffect(() => {
    if (p.tree === null) setTree(p, bare || (panel.current?.getBoundingClientRect().width ?? 0) >= 600);
  }, [p]);
  // The folder read again when a turn ends, and when the window comes back.
  useEffect(() => {
    if (!running) refreshTree(p);
  }, [running]);
  useEffect(() => {
    const back = () => refreshTree(p);
    window.addEventListener("focus", back);
    return () => window.removeEventListener("focus", back);
  }, [p]);

  if (!p.open) return null;
  return (
    <aside
      ref={panel}
      className={`sfp ${bare ? "bare" : wide ? "max" : width !== null ? "sized" : ""} ${resizing ? "resizing" : ""}`}
      style={bare ? undefined : style(wide)}
      aria-label={t("files.title")}
      data-testid="file-panel"
    >
      <div className="sfp-strip">
        <div className="sfp-tabs" role="tablist">
          {p.tabs.map((x) => (
            <div key={x.key} role="tab" aria-selected={x === tab} className={`sfp-tab ${x === tab ? "on" : ""}`} title={x.file.path} data-path={x.file.path} onClick={() => activate(p, x.key)}>
              <Icon name="doc" />
              <span className="sfp-tab-t">{x.name}</span>
              <button
                className="sfp-tab-x"
                aria-label={t("files.closeTab", { name: x.name })}
                title={t("files.closeTab", { name: x.name })}
                onClick={(e) => {
                  e.stopPropagation();
                  closeTab(p, x.key);
                }}
              >
                <Icon name="x" />
              </button>
            </div>
          ))}
        </div>
        {tab && <Tools home={home} tab={tab} />}
        <button className={`tb ${p.tree ? "on" : ""}`} aria-label={t("files.toggleTree")} title={t("files.toggleTree")} aria-pressed={!!p.tree} onClick={() => setTree(p, !p.tree)}>
          <Icon name="tree" />
        </button>
        {!bare && (
          <>
            <span className="sfp-sep" />
            <button className="tb" aria-label={wide ? t("files.restore") : t("files.maximize")} title={wide ? t("files.restore") : t("files.maximize")} onClick={() => setMax(p, !wide)}>
              <Icon name={wide ? "shrink" : "expand"} />
            </button>
          </>
        )}
        {/* The conversation bar's Files button, here while the panel is open: it puts the panel away. */}
        <button className="tb on" aria-label={t("files.hide")} title={t("files.hide")} aria-pressed onClick={() => setOpen(p, false)} data-testid="file-panel-close">
          <Icon name="folder" />
        </button>
      </div>
      <div className="sfp-body">
        {!bare && <div className="sfp-main">{tab ? <Body home={home} tab={tab} /> : <div className="sfp-empty">{t("files.pick")}</div>}</div>}
        {(bare || p.tree === true) && <Tree p={p} activePath={tab?.file.path ?? null} />}
      </div>
      {tab?.read && "res" in tab.read && <VersionBar res={tab.read.res} run={tab.file.run} turnRun={tab.replyRun} onRun={(run) => switchRun(p, tab, run)} />}
      {!wide && !bare && <div className="sfp-grip" role="separator" aria-orientation="vertical" aria-label={t("files.resize")} tabIndex={0} {...grip} />}
    </aside>
  );
}

/** The file's tools at the strip's end: preview or source, the page over the window, its menu. */
function Tools({ home, tab }: { home: FilesHome; tab: Tab }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const p = usePanel(home.session);
  const view = fileView(tab.file.path);
  const folder = tab.read && "res" in tab.read ? tab.read.res.folder : "";
  return (
    <div className="sfp-tools">
      {(view === "markdown" || view === "svg") && (
        <button className={`tb ${tab.preview ? "" : "on"}`} aria-label={t("files.source")} title={tab.preview ? t("files.source") : t("files.preview")} aria-pressed={!tab.preview} onClick={() => setPreview(p, tab, !tab.preview)}>
          <Icon name="code" />
        </button>
      )}
      <button className="tb" aria-label={t("files.more")} title={t("files.more")} onClick={() => void fileMenu(st, home, tab.file, folder, { line: tab.target?.line })}>
        <Icon name="more" />
      </button>
    </div>
  );
}

/** The open file: its code, what it looks like (Markdown, SVG, a picture, a PDF), or why it is not shown. */
function Body({ home, tab }: { home: FilesHome; tab: Tab }) {
  const st = useStore();
  if (!tab.read) return <Loading />;
  if ("error" in tab.read) return <Card name={tab.name} text={fileError(tab.read.error, machineName(st, home.machine))} />;
  const view = fileView(tab.file.path);
  if (view === "pdf") return <PdfBody key={tab.key} file={tab.file} name={tab.name} />;
  // A page runs here, as over the window: the panel's ⤢ takes the window with it.
  if (view === "page") return <PageBody key={tab.key} file={tab.file} slot="panel" />;
  return (
    <div className="sfp-file" key={`${tab.key}\0${tab.target?.seq ?? 0}`}>
      <TextBody res={tab.read.res} file={tab.file} name={tab.name} line={tab.target?.line} endLine={tab.target?.endLine} source={!tab.preview} />
    </div>
  );
}
