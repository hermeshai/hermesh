import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { fileView } from "@hermesh/her-client";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore } from "../../store";
import { FilesHomeContext, FilesRunContext, sizeText, type FilesHome } from "./files";
import { AttachmentBody, Card, fileError, Loading, PageBody, PdfBody, TextBody, VersionBar } from "./FileView";
import { fileMenu } from "./open";
import { activate, closeTab, openProject, refreshTree, setMax, setPreview, setTree, switchRun, usePanel, type Panel, type Tab } from "./panel";
import { usePanelWidth } from "./panelWidth";
import { Tree } from "./Tree";

// The side panel (docs/session-files.md §3.4): the session's right, as tabs, each of a kind. A files
// tab is a file with the folder's tree beside it, shown or not (with no file, the tree alone: 项目文件);
// a page tab is a .html running. The strip: the tabs, ＋ for a new one, ⤢ to take the window. Under
// it, the tab's own bar; at its foot, the version bar of a reply's file. The toolbar's button shows it.

/** What the conversation keeps beside the panel at least. */
const conversationRoom = () => 160;

export function SidePanel({ home, running }: { home: FilesHome; running: boolean }) {
  const { t } = useTranslation("session");
  const p = usePanel(home.session);
  const tab = p.tabs.find((x) => x.key === p.active) ?? null;
  const { panel, width, resizing, style, grip } = usePanelWidth("hermesh.filePanelWidth", conversationRoom);
  const add = useRef<HTMLButtonElement>(null);

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
  // A new tab, of a kind: today only 项目文件; a terminal or a browser joins this menu.
  const newTab = async () => {
    const r = add.current?.getBoundingClientRect();
    const id = await window.hermesh?.popupMenu([{ id: "files", label: t("files.tree") }], r ? { x: Math.round(r.left), y: Math.round(r.bottom + 4) } : undefined);
    if (id === "files") openProject(p);
  };
  return (
    <aside
      ref={panel}
      className={`sfp ${p.max ? "max" : width !== null ? "sized" : ""} ${resizing ? "resizing" : ""}`}
      style={style(p.max)}
      aria-label={t("panel.title")}
      data-testid="side-panel"
    >
      <div className="sfp-strip drag">
        <div className="sfp-tabs" role="tablist">
          {p.tabs.map((x) => (
            <div
              key={x.key}
              role="tab"
              aria-selected={x === tab}
              className={`sfp-tab no-drag ${x === tab ? "on" : ""}`}
              title={x.file.path || t("files.tree")}
              data-path={x.file.path}
              data-kind={x.kind}
              onClick={() => activate(p, x.key)}
            >
              <Icon name={x.kind === "page" || (x.kind === "attachment" && fileView(x.file.path) === "page") ? "web" : x.file.path ? "doc" : "tree"} />
              <span className="sfp-tab-t">{x.file.path ? x.name : t("files.tree")}</span>
              <button
                className="sfp-tab-x"
                aria-label={t("files.closeTab", { name: x.file.path ? x.name : t("files.tree") })}
                title={t("files.closeTab", { name: x.file.path ? x.name : t("files.tree") })}
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
        <button ref={add} className="tb sfp-add" aria-label={t("panel.newTab")} title={t("panel.newTab")} onClick={() => void newTab()} data-testid="side-panel-new">
          <Icon name="plus" />
        </button>
        <span className="sfp-fill" />
        <button className="tb" aria-label={p.max ? t("files.restore") : t("files.maximize")} title={p.max ? t("files.restore") : t("files.maximize")} onClick={() => setMax(p, !p.max)}>
          <Icon name={p.max ? "shrink" : "expand"} />
        </button>
      </div>
      {tab && <Bar home={home} p={p} tab={tab} />}
      <FilesHomeContext.Provider value={home}>
        <FilesRunContext.Provider value={tab?.file.run ?? ""}>
          <div className="sfp-body">{tab && <Body home={home} p={p} tab={tab} />}</div>
        </FilesRunContext.Provider>
      </FilesHomeContext.Provider>
      {tab?.file.path && tab.kind !== "attachment" && tab.read && "res" in tab.read && <VersionBar res={tab.read.res} run={tab.file.run} turnRun={tab.replyRun} onRun={(run) => switchRun(p, tab, run)} />}
      {!p.max && <div className="sfp-grip" role="separator" aria-orientation="vertical" aria-label={t("files.resize")} tabIndex={0} {...grip} />}
    </aside>
  );
}

/** The tab's own bar: what it is, and its tools. */
function Bar({ home, p, tab }: { home: FilesHome; p: Panel; tab: Tab }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const path = tab.file.path;
  const folder = p.dirs.get("")?.list?.folder ?? "";
  if (!path) return <div className="sfp-bar"><span className="sfp-path" title={folder}><span dir="ltr">{folder}</span></span></div>;
  const view = fileView(path);
  if (tab.kind === "attachment") {
    return (
      <div className="sfp-bar">
        <span className="sfp-path" data-testid="attachment-bar">
          <span dir="ltr">{t("files.attachment", { size: sizeText(tab.size ?? 0n) })}</span>
        </span>
        {view === "page" && (
          <button className="tb" aria-label={t("files.openBrowser")} title={t("files.openBrowser")} onClick={() => void window.hermesh?.openSessionPageInBrowser(tab.file)}>
            <Icon name="web" />
          </button>
        )}
        <button
          className="tb"
          aria-label={t("files.more")}
          title={t("files.more")}
          onClick={async () => {
            const id = await window.hermesh?.popupMenu([{ id: "save", label: t("files.save") }]);
            if (id === "save") void window.hermesh?.saveSessionFile(tab.file, tab.name).then(() => st.show(t("files.saved")));
          }}
        >
          <Icon name="more" />
        </button>
      </div>
    );
  }
  // Cut at the start when it is long: the file's name is what matters.
  const where = (
    <span dir="ltr">
      {path.slice(0, path.lastIndexOf("/") + 1)}
      <b>{tab.name}</b>
    </span>
  );
  const more = (
    <button className="tb" aria-label={t("files.more")} title={t("files.more")} onClick={() => void fileMenu(st, home, tab.file, tab.read && "res" in tab.read ? tab.read.res.folder : "", { line: tab.target?.line })}>
      <Icon name="more" />
    </button>
  );
  if (tab.kind === "page") {
    return (
      <div className="sfp-bar">
        <span className="sfp-addr" title={path}>{where}</span>
        <button className="tb" aria-label={t("files.openBrowser")} title={t("files.openBrowser")} onClick={() => void window.hermesh?.openSessionPageInBrowser(tab.file)}>
          <Icon name="web" />
        </button>
        {more}
      </div>
    );
  }
  return (
    <div className="sfp-bar">
      <span className="sfp-path" title={path}>{where}</span>
      {(view === "markdown" || view === "svg") && (
        <button className={`tb ${tab.preview ? "" : "on"}`} aria-label={t("files.source")} title={tab.preview ? t("files.source") : t("files.preview")} aria-pressed={!tab.preview} onClick={() => setPreview(p, tab, !tab.preview)}>
          <Icon name="code" />
        </button>
      )}
      <button className={`tb ${tab.tree ? "on" : ""}`} aria-label={t("files.toggleTree")} title={t("files.toggleTree")} aria-pressed={tab.tree} onClick={() => setTree(p, tab, !tab.tree)}>
        <Icon name="tree" />
      </button>
      {more}
    </div>
  );
}

/** The tab: 项目文件's tree; a file, the tree beside it if shown; a page running. */
function Body({ home, p, tab }: { home: FilesHome; p: Panel; tab: Tab }) {
  if (!tab.file.path) return <Tree p={p} activePath={null} wide />;
  if (tab.kind === "attachment") return <AttachmentBody key={tab.key} file={tab.file} />;
  if (tab.kind === "page") return <FileBody home={home} tab={tab} />;
  return (
    <>
      <div className="sfp-main">
        <FileBody home={home} tab={tab} />
      </div>
      {tab.tree && <Tree p={p} activePath={tab.file.path} />}
    </>
  );
}

/** The open file: its code, what it looks like (Markdown, SVG, a picture, a PDF, a page), or why it is not shown. */
function FileBody({ home, tab }: { home: FilesHome; tab: Tab }) {
  const st = useStore();
  if (!tab.read) return <Loading />;
  if ("error" in tab.read) return <Card name={tab.name} text={fileError(tab.read.error, machineName(st, home.machine))} />;
  const view = fileView(tab.file.path);
  if (view === "pdf") return <PdfBody key={tab.file.path} file={tab.file} name={tab.name} />;
  if (view === "page") return <PageBody file={tab.file} />;
  return (
    <div className="sfp-file" key={`${tab.file.run}\0${tab.file.path}\0${tab.target?.seq ?? 0}`}>
      <TextBody res={tab.read.res} file={tab.file} name={tab.name} line={tab.target?.line} endLine={tab.target?.endLine} source={!tab.preview} />
    </div>
  );
}
