import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import type { PageSlot } from "../../../../shared/bridge";
import { i18n } from "../../../../shared/i18n";
import { fileView } from "@hermesh/her-client";
import { FileContentKind, FileNow, type ReadFileResponse } from "@hermesh/proto/session";
import { PdfView } from "../../her/preview/PdfView";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore } from "../../store";
import { sessions } from "../../api";
import { Markdown } from "../Markdown";
import { Dialog } from "../markdown/Dialog";
import { readAll, sizeText, type SessionFile } from "./files";
import { closeShownFile, fileMenu, useShownFile, type Shown } from "./open";
import "../../her/preview/preview.css";
import "./files.css";

const CodeView = lazy(() => import("./CodeView"));

// A session's file over the window (docs/session-files.md §3.3, FinxWork's preview/FilePreview): a
// page as it runs, a PDF as its pages, a picture, Markdown as it reads, code at the lines a reply
// pointed at. It reads the file as the reply's turn left it; the bar at its foot says so when the
// file is different now, and shows the current one.

export function FileViewHost() {
  const shown = useShownFile();
  return shown ? <FileView key={shown.seq} shown={shown} onClose={closeShownFile} /> : null;
}

type Read = { res: ReadFileResponse } | { error: string } | null;

/** Why a file could not be read, as the owner reads it. */
export function fileError(e: unknown, machine: string): string {
  const t = i18n.getFixedT(null, "session");
  const err = ConnectError.from(e);
  if (err.code === Code.NotFound) return t("files.notFound");
  if (err.code === Code.PermissionDenied) return t("files.outside");
  if (err.code === Code.Unavailable || err.code === Code.DeadlineExceeded) return t("files.offline", { machine });
  return t("files.failed", { message: err.rawMessage });
}

function FileView({ shown, onClose }: { shown: Shown; onClose: () => void }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const [run, setRun] = useState(shown.file.run);
  const [source, setSource] = useState(false);
  // Taking the whole window (a page, a picture, a PDF are read wide), and back.
  const [max, setMax] = useState(false);
  const [read, setRead] = useState<Read>(null);
  const file: SessionFile = { ...shown.file, run };
  const view = fileView(shown.file.path);
  useEffect(() => {
    let live = true;
    setRead(null);
    sessions.readFile({ sessionId: file.session, runId: file.run, path: file.path }).then(
      (res) => live && setRead({ res }),
      (e: unknown) => live && setRead({ error: errorText(e) }),
    );
    return () => {
      live = false;
    };
  }, [run]);

  const errorText = (e: unknown) => fileError(e, machineName(st, shown.home.machine));

  const res = read && "res" in read ? read.res : null;
  // A page is what it runs as: its source is in the file panel.
  const sourceable = view === "markdown" || view === "svg";
  return createPortal(
    <Dialog label={shown.name} width={max ? "100vw" : "min(var(--md-rich-width), calc(100vw - 96px))"} className={`fpv sfv ${max ? "max" : ""}`} onClose={onClose}>
      <div className="fpv-in" data-testid="file-view" data-path={shown.file.path}>
        <div className="fpv-head">
          <span className="fpv-name" title={shown.file.path}>
            {shown.name}
          </span>
          {sourceable && (
            <button className={`tb ${source ? "on" : ""}`} aria-label={t("files.source")} title={source ? t("files.preview") : t("files.source")} aria-pressed={source} onClick={() => setSource(!source)}>
              <Icon name="code" />
            </button>
          )}
          {view === "page" && (
            <button className="tb" aria-label={t("files.openBrowser")} title={t("files.openBrowser")} onClick={() => void window.hermesh?.openSessionPageInBrowser(file)}>
              <Icon name="web" />
            </button>
          )}
          <button className={`tb ${max ? "on" : ""}`} aria-label={max ? t("files.restore") : t("files.fullWindow")} title={max ? t("files.restore") : t("files.fullWindow")} aria-pressed={max} onClick={() => setMax(!max)} data-testid="file-view-max">
            <Icon name={max ? "shrink" : "expand"} />
          </button>
          <button className="tb" aria-label={t("files.more")} title={t("files.more")} onClick={() => void fileMenu(st, shown.home, file, res?.folder ?? "", { line: shown.line })}>
            <Icon name="more" />
          </button>
          <button className="tb" aria-label={t("files.close")} title={t("files.close")} onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div className="fpv-body" tabIndex={-1} ref={(el) => el?.setAttribute("autofocus", "")}>
          {!read ? (
            <Loading />
          ) : "error" in read ? (
            <Card name={shown.name} text={read.error} />
          ) : view === "page" && !source ? (
            <PageBody file={file} slot="dialog" onClose={onClose} />
          ) : view === "pdf" ? (
            <PdfBody file={file} name={shown.name} />
          ) : (
            <TextBody res={read.res} file={file} name={shown.name} line={shown.line} endLine={shown.endLine} source={source} />
          )}
        </div>
        {res && <VersionBar res={res} run={run} turnRun={shown.file.run} onRun={setRun} />}
      </div>
    </Dialog>,
    document.body,
  );
}

/** What a turn's file is against the folder now, and the way to the other. */
export function VersionBar({ res, run, turnRun, onRun }: { res: ReadFileResponse; run: string; turnRun: string; onRun: (run: string) => void }) {
  const { t } = useTranslation("session");
  let text = "";
  let action: { label: string; to: string } | null = null;
  if (run && res.turn > 0) {
    if (res.now === FileNow.CHANGED) [text, action] = [t("files.turnChanged", { turn: res.turn }), { label: t("files.showNow"), to: "" }];
    else if (res.now === FileNow.GONE) text = t("files.turnGone", { turn: res.turn });
    else if (res.now === FileNow.UNKNOWN) text = t("files.turnFolderGone", { turn: res.turn });
  } else if (!run && turnRun) {
    [text, action] = [t("files.nowShown"), { label: t("files.showTurn"), to: turnRun }];
  }
  if (!text) return null;
  return (
    <div className="sfv-bar" data-testid="file-version">
      <span>{text}</span>
      {action && (
        <button className="btn" onClick={() => onRun(action.to)}>
          {action.label}
        </button>
      )}
    </div>
  );
}

export function Loading() {
  return (
    <div className="fpv-msg">
      <span className="mini-spin" />
    </div>
  );
}

export function Card({ name, text, children }: { name: string; text: string; children?: React.ReactNode }) {
  return (
    <div className="sfv-card" data-testid="file-card">
      <Icon name="doc" />
      <b>{name}</b>
      <p>{text}</p>
      {children && <div className="sfv-card-b">{children}</div>}
    </div>
  );
}

/** Text as it reads (Markdown, SVG) or as code; a picture as it looks; else why not, and Save. */
export function TextBody({ res, file, name, line, endLine, source }: { res: ReadFileResponse; file: SessionFile; name: string; line?: number; endLine?: number; source: boolean }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const view = fileView(file.path);
  const shown = { name, line, endLine };
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (res.kind !== FileContentKind.IMAGE && !(view === "svg" && !source)) return;
    const blob = res.kind === FileContentKind.IMAGE ? new Blob([res.image as BlobPart], { type: res.mimeType }) : new Blob([res.text], { type: "image/svg+xml" });
    const u = URL.createObjectURL(blob);
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [res, source]);
  const save = (
    <button className="btn" onClick={() => void window.hermesh?.saveSessionFile(file, shown.name).then(() => st.show(t("files.saved")))}>
      {t("files.save")}
    </button>
  );
  if (res.kind === FileContentKind.IMAGE || (view === "svg" && !source)) {
    return <div className="sfv-pic">{url && <img src={url} alt={shown.name} draggable={false} data-testid="file-image" />}</div>;
  }
  if (res.kind === FileContentKind.BINARY) return <Card name={shown.name} text={t("files.binary")}>{save}</Card>;
  if (res.kind === FileContentKind.TOO_LARGE) return <Card name={shown.name} text={t("files.tooLarge", { size: sizeText(res.size) })}>{save}</Card>;
  if (view === "markdown" && !source && shown.line === undefined) {
    return (
      <div className="fpv-read" data-testid="file-markdown">
        <div className="fpv-col">
          <Markdown text={res.text} mode="document" />
        </div>
      </div>
    );
  }
  return (
    <Suspense fallback={<Loading />}>
      <CodeView text={res.text} name={shown.name} line={shown.line} endLine={shown.endLine} wrap={view === "markdown" || view === "text"} />
    </Suspense>
  );
}

/** A PDF, read whole from the session's machine, as pdf.js shows it. */
export function PdfBody({ file, name }: { file: SessionFile; name: string }) {
  const [bytes, setBytes] = useState<Uint8Array | { error: string } | null>(null);
  useEffect(() => {
    let live = true;
    readAll(file).then(
      (r) => live && setBytes(r.data),
      (e: unknown) => live && setBytes({ error: String(e) }),
    );
    return () => {
      live = false;
    };
  }, [file.run]);
  if (!bytes) return <Loading />;
  if ("error" in bytes) return <Card name={name} text={bytes.error} />;
  return <PdfView bytes={bytes} name={name} />;
}

/**
 * A page as it runs, in a view of its own over this room (main/sessionFiles.ts): a process apart from
 * the app, with no network, reading its own folder from the session's machine. It opens once the
 * dialog has laid the room out.
 */
export function PageBody({ file, slot, onClose }: { file: SessionFile; slot: PageSlot; onClose?: () => void }) {
  const room = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const bridge = window.hermesh;
    const el = room.current;
    if (!bridge || !el) return;
    let opened = false;
    const rect = () => {
      const r = el.getBoundingClientRect();
      return { x: r.x, y: r.y, width: r.width, height: r.height };
    };
    const place = () => {
      const now = rect();
      if (now.width === 0 || now.height === 0) return;
      if (opened) return bridge.placeSessionPage(now, slot);
      opened = true;
      void bridge.openSessionPage(file, now, slot);
    };
    const observer = new ResizeObserver(place);
    observer.observe(el);
    window.addEventListener("resize", place);
    const off = bridge.onSessionPage((event) => {
      if ((event.slot ?? "dialog") !== slot) return;
      if (event.kind === "close") onClose?.();
      else if (event.kind === "link") bridge.openExternal(event.url);
    });
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", place);
      off();
      bridge.closeSessionPage(slot);
    };
  }, [file.run, file.path]);
  return <div ref={room} className={`sfv-page ${slot}`} data-testid={slot === "panel" ? "panel-page" : "file-page"} />;
}
