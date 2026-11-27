import { useContext, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { FileRef } from "@hermesh/her-client";
import { FileRefKind } from "@hermesh/proto/session";
import { FileIcon } from "../../icons";
import { useStore } from "../../store";
import { FilesHomeContext, FilesRunContext, useFileInfo } from "./files";
import { ReplyImagesContext } from "./images";
import { fileMenu, openSessionFile, openSessionFolder } from "./open";

/**
 * A file or folder a reply points at, as a tag (FinxWork's content-actions §4.1): its icon, name and
 * lines; a click opens it, a secondary click offers the rest. From inline code it shows only once
 * the machine says the path is there; from a link it shows anyway, grey when there is nothing there
 * or it is outside the session's folder.
 */
export function FileTag({ target, code, children }: { target: FileRef; code?: string; children?: ReactNode }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const home = useContext(FilesHomeContext);
  const run = useContext(FilesRunContext);
  const replyImages = useContext(ReplyImagesContext);
  const info = useFileInfo(target.path);
  const found = info?.kind === FileRefKind.FILE || info?.kind === FileRefKind.FOLDER;
  if (!home || (code !== undefined && !found)) return code !== undefined ? <code>{code}</code> : <span>{children}</span>;
  const missing = info?.kind === FileRefKind.MISSING;
  const outside = info?.kind === FileRefKind.OUTSIDE;
  const folder = info ? info.kind === FileRefKind.FOLDER : /[\\/]$/.test(target.path);
  const path = info && !outside && !missing ? info.path : target.path;
  const file = { session: home.session, run, path };
  const lines = target.line === undefined ? "" : target.endLine ? t("files.lines", { line: target.line, end: target.endLine }) : t("files.line", { line: target.line });
  const why = missing ? t("files.notFound") : outside ? t("files.outside") : "";
  const open = () => {
    if (missing || outside) return;
    if (folder) return openSessionFolder(home, path);
    openSessionFile(st, home, file, { line: target.line, endLine: target.endLine, gallery: replyImages?.gallery });
  };
  return (
    <span
      className={`fref ${code !== undefined ? "mono" : ""} ${missing || outside ? "gone" : ""}`}
      role="button"
      tabIndex={0}
      data-testid="file-tag"
      data-path={path}
      title={[why, path, lines].filter(Boolean).join(" · ")}
      onClick={(e) => {
        // Not when the click ends a selection that takes the tag in.
        const sel = window.getSelection();
        if (sel && !sel.isCollapsed && sel.containsNode(e.currentTarget, true)) return;
        open();
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") (e.preventDefault(), open());
      }}
      onContextMenu={(e) => {
        e.preventDefault();
        e.stopPropagation();
        if (missing || outside || folder) return;
        void fileMenu(st, home, file, info?.folder ?? "", { open, line: target.line });
      }}
    >
      <FileIcon name={path} dir={folder} />
      <span className="fref-n">{code !== undefined ? target.path : children}</span>
      {target.line !== undefined && <span className="fref-l">{`:${target.line}${target.endLine ? `-${target.endLine}` : ""}`}</span>}
    </span>
  );
}
