import { fileOpens, isSessionImage } from "@hermesh/her-client";
import type { MenuItem } from "../../../../shared/bridge";
import { i18n } from "../../../../shared/i18n";
import type { Store } from "../../store";
import { baseName, type FilesHome, type SessionFile } from "./files";
import { openFileInLightbox } from "./ImageLightbox";
import type { ReplyImageItem } from "./images";
import { openInPanel, showFolder } from "./panel";

// Where a session's file opens when clicked (docs/session-files.md §3.2): a picture in the Lightbox,
// what the app does not show in Quick Look, the rest in a tab of the side panel.

const t = i18n.getFixedT(null, "session");

/** Opens a session's file the way its kind and lines say. */
export function openSessionFile(
  st: Store,
  home: FilesHome,
  file: SessionFile,
  at?: { line?: number; endLine?: number; gallery?: ReplyImageItem[] | null },
) {
  if (isSessionImage(file.path, at?.line)) {
    openFileInLightbox(home, file, at?.gallery);
    return;
  }
  if (fileOpens(file.path, at?.line) === "quicklook") {
    void window.hermesh?.quickLookSessionFile(file, baseName(file.path)).catch((e: unknown) => st.show(failure(e)));
    return;
  }
  openInPanel(file, at);
}

/** A folder a reply points at: 项目文件, its tree opened to it. */
export function openSessionFolder(home: FilesHome, path: string) {
  showFolder(home.session, path);
}

function failure(e: unknown): string {
  return t("files.failed", { message: e instanceof Error ? e.message : String(e) });
}

/** A file's menu, on a tag and on a tab: open it, copy where it is, keep a copy, find it. */
export async function fileMenu(st: Store, home: FilesHome, file: SessionFile, folder: string, opts: { open?: () => void; line?: number } = {}) {
  const name = baseName(file.path);
  const isAbs = /^([/\\]|~\/|[A-Za-z]:[/\\])/.test(file.path);
  const full = folder && !isAbs ? `${folder.replace(/\/+$/, "")}/${file.path}` : file.path;
  const items: MenuItem[] = [
    ...(opts.open ? [{ id: "open", label: t("files.open") }, { separator: true } as const] : []),
    { id: "copyPath", label: t("files.copyPath") },
    ...(!isAbs ? [{ id: "copyRel", label: t("files.copyRelPath") }] : []),
    { separator: true },
    { id: "save", label: t("files.save") },
    ...(home.local ? [{ id: "reveal", label: t("files.revealFinder") }] : []),
  ];
  const id = await window.hermesh?.popupMenu(items);
  const copy = (text: string) => void navigator.clipboard.writeText(text).then(() => st.show(t("content.copied")));
  if (id === "open") opts.open?.();
  else if (id === "copyPath") copy(opts.line ? `${full}:${opts.line}` : full);
  else if (id === "copyRel") copy(opts.line ? `${file.path}:${opts.line}` : file.path);
  else if (id === "save") void window.hermesh?.saveSessionFile(file, name).then(() => st.show(t("files.saved")), (e: unknown) => st.show(failure(e)));
  else if (id === "reveal") void window.hermesh?.revealSessionFile(file).then((ok) => ok || st.show(t("files.notFound")));
}
