import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { FilesReadResult } from "@hermesh/her-protocol";
import { Icon } from "../../icons";
import { Markdown } from "../../session/Markdown";
import { Dialog } from "../../session/markdown/Dialog";
import { errorText } from "../../store";
import { useHer } from "../context";
import { sizeText } from "../format";
import type { FileScope } from "../Messages";
import { PdfView } from "./PdfView";
import "./preview.css";

// A file of the conversation to read, over the window: a PDF as its pages, Markdown as it reads
// (or as its source). The file stays on the assistant's machine; Save puts a copy in Downloads.

/** The files a tile opens here rather than saving them to Downloads. */
export const previews = (name: string) => isPdf(name) || isMarkdown(name);
const isPdf = (name: string) => /\.pdf$/i.test(name);
const isMarkdown = (name: string) => /\.(md|markdown)$/i.test(name);

export function FilePreview({ scope, path, name, onSave, onClose }: { scope: FileScope; path: string; name: string; onSave: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const [source, setSource] = useState(false);
  // Over the page, not inside the bubble it came from; its keys stay its own (an approval waiting under it answers to Enter).
  return createPortal(
    <Dialog label={name} width="min(var(--md-rich-width), calc(100vw - 96px))" className="fpv" onClose={onClose}>
      <div className="fpv-in" onKeyDown={(e) => e.key !== "Escape" && e.stopPropagation()}>
        <div className="fpv-head">
          <span className="fpv-name" title={name}>
            {name}
          </span>
          {isMarkdown(name) && (
            <button className={`tb ${source ? "on" : ""}`} aria-label={t("preview.source")} title={source ? t("preview.rendered") : t("preview.source")} aria-pressed={source} onClick={() => setSource(!source)}>
              <Icon name="code" />
            </button>
          )}
          <button className="tb" aria-label={t("preview.save")} title={t("preview.save")} onClick={onSave}>
            <Icon name="arrowD" />
          </button>
          <button className="tb" aria-label={t("preview.close")} title={t("preview.close")} onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        {/* The first thing with the keys, so the arrows and Page Down read on from the start. */}
        <div className="fpv-body" tabIndex={-1} ref={(el) => el?.setAttribute("autofocus", "")}>
          {isPdf(name) ? <PdfBody bot={scope.bot} path={path} name={name} /> : <TextBody scope={scope} path={path} name={name} source={source} />}
        </div>
      </div>
    </Dialog>,
    document.body,
  );
}

/** A PDF, fetched whole from the assistant's machine, then shown by pdf.js. */
function PdfBody({ bot, path, name }: { bot: string; path: string; name: string }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const [read, setRead] = useState<Uint8Array | { error: string } | null>(null);
  useEffect(() => {
    let live = true;
    const reading = window.hermesh?.readHerFile(her.machine.machineId, bot, path) ?? Promise.reject(new Error("no app"));
    reading.then(
      (bytes) => live && setRead(bytes),
      (e) => live && setRead({ error: errorText(e) }),
    );
    return () => {
      live = false;
    };
  }, [her.machine.machineId, bot, path]);
  if (!read) return <Loading />;
  if ("error" in read) return <div className="fpv-msg">{t("preview.failed", { name, error: read.error })}</div>;
  return <PdfView bytes={read} name={name} />;
}

/** Markdown, read through the assistant's connection: as it reads, or its source. */
function TextBody({ scope, path, name, source }: { scope: FileScope; path: string; name: string; source: boolean }) {
  const { t } = useTranslation("her");
  const [read, setRead] = useState<FilesReadResult | { error: string } | null>(null);
  useEffect(() => {
    const rpc = scope.rpc;
    if (!rpc) return;
    let live = true;
    rpc.request("files/read", { session_id: scope.session, path }).then(
      (r) => live && setRead(r),
      (e) => live && setRead({ error: errorText(e) }),
    );
    return () => {
      live = false;
    };
  }, [scope.rpc, scope.session, path]);
  if (!read) return <Loading />;
  if ("error" in read) return <div className="fpv-msg">{t("preview.failed", { name, error: read.error })}</div>;
  if (read.kind === "too_large") return <div className="fpv-msg">{t("preview.tooLarge", { size: sizeText(read.size) })}</div>;
  const text = read.text ?? "";
  return (
    <div className="fpv-read">
      <div className="fpv-col">{source ? <pre className="fpv-text sel-text">{text}</pre> : <Markdown text={text} mode="document" />}</div>
    </div>
  );
}

function Loading() {
  return (
    <div className="fpv-msg">
      <span className="mini-spin" />
    </div>
  );
}
