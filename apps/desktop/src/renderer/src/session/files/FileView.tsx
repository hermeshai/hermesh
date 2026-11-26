import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import { i18n } from "../../../../shared/i18n";
import { fileView } from "@hermesh/her-client";
import { FileContentKind, FileNow, type ReadFileResponse } from "@hermesh/proto/session";
import { PdfView } from "../../her/preview/PdfView";
import { Icon } from "../../icons";
import { useStore } from "../../store";
import { sessions } from "../../api";
import { Markdown } from "../Markdown";
import { refersToOwnFiles } from "../attachments";
import { readAll, sizeText, type SessionFile } from "./files";
import "../../her/preview/preview.css";
import "./files.css";

const CodeView = lazy(() => import("./CodeView"));

// What a side panel's tab shows of a session's file (docs/session-files.md §3.3, FinxWork's
// preview/FilePreview): a page as it runs, a PDF as its pages, a picture, Markdown as it reads, code at
// the lines a reply pointed at; the bar at its foot says when a reply's file is different now.

/** Why a file could not be read, as the owner reads it. */
export function fileError(e: unknown, machine: string): string {
  const t = i18n.getFixedT(null, "session");
  const err = ConnectError.from(e);
  if (err.code === Code.NotFound) return t("files.notFound");
  if (err.code === Code.PermissionDenied) return t("files.outside");
  if (err.code === Code.Unavailable || err.code === Code.DeadlineExceeded) return t("files.offline", { machine });
  return t("files.failed", { message: err.rawMessage });
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
 * A file the owner sent with a prompt, read from its blob: a page running (on its own: the files beside it were not
 * sent, and the bar under it says so when it refers to some), SVG as it looks, Markdown as it reads, code and text in
 * numbered lines.
 */
export function AttachmentBody({ file }: { file: SessionFile }) {
  const { t } = useTranslation("session");
  const [got, setGot] = useState<{ text: string; url: string | null } | { error: unknown } | null>(null);
  const view = fileView(file.path);
  useEffect(() => {
    let live = true;
    let url: string | null = null;
    setGot(null);
    sessions.downloadBlob({ sessionId: file.session, machineId: file.blob?.machine ?? "", blobSha256: file.blob?.sha ?? "" }).then(
      (r) => {
        if (!live) return;
        if (view === "svg") url = URL.createObjectURL(new Blob([r.data as BlobPart], { type: "image/svg+xml" }));
        setGot({ text: new TextDecoder().decode(r.data), url });
      },
      (error: unknown) => live && setGot({ error }),
    );
    return () => {
      live = false;
      if (url) URL.revokeObjectURL(url);
    };
  }, [file.blob?.sha]);
  if (!got) return <Loading />;
  if ("error" in got) return <Card name={file.path} text={fileError(got.error, "")} />;
  if (view === "page") {
    return (
      <div className="sfv-att">
        <PageBody file={file} />
        {refersToOwnFiles(got.text) && (
          <div className="sfv-bar" data-testid="attachment-alone">
            <span>{t("files.attachmentAlone")}</span>
          </div>
        )}
      </div>
    );
  }
  if (view === "svg" && got.url) return <div className="sfv-pic"><img src={got.url} alt={file.path} draggable={false} /></div>;
  if (view === "markdown") {
    return (
      <div className="fpv-read" data-testid="file-markdown">
        <div className="fpv-col">
          <Markdown text={got.text} mode="document" />
        </div>
      </div>
    );
  }
  return (
    <Suspense fallback={<Loading />}>
      <CodeView text={got.text} name={file.path} wrap={view === "text"} />
    </Suspense>
  );
}

/** Something of the app's over the window (a dialog, the Lightbox, a sheet): the page's view, drawn over the app, steps out of its way. */
const covered = () => document.querySelector('dialog[open], [role="dialog"]') !== null;

/**
 * A page as it runs, in a view of its own over this room (main/sessionFiles.ts): a process apart from
 * the app, with no network, reading its own folder from the session's machine. It opens once the
 * room is laid out, and hides while something of the app's is over it.
 */
export function PageBody({ file }: { file: SessionFile }) {
  const room = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const bridge = window.hermesh;
    const el = room.current;
    if (!bridge || !el) return;
    let opened = false;
    let live = true;
    const rect = () => {
      const r = el.getBoundingClientRect();
      return { x: r.x, y: r.y, width: r.width, height: r.height };
    };
    const place = () => {
      const now = rect();
      if (now.width === 0 || now.height === 0) return;
      if (opened) return bridge.placeSessionPage(covered() ? null : now);
      opened = true;
      void bridge.openSessionPage(file, now).then(() => live && covered() && bridge.placeSessionPage(null));
    };
    let frame = 0;
    const soon = () => void (frame ||= requestAnimationFrame(() => ((frame = 0), place())));
    const sized = new ResizeObserver(place);
    sized.observe(el);
    const overlays = new MutationObserver(soon);
    overlays.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["open", "role"] });
    window.addEventListener("resize", place);
    const off = bridge.onSessionPage((event) => bridge.openExternal(event.url));
    return () => {
      live = false;
      cancelAnimationFrame(frame);
      sized.disconnect();
      overlays.disconnect();
      window.removeEventListener("resize", place);
      off();
      bridge.closeSessionPage();
    };
  }, [file.run, file.path]);
  return <div ref={room} className="sfv-page" data-testid="panel-page" />;
}
