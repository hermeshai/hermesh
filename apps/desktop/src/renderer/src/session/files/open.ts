import { useSyncExternalStore } from "react";
import { fileOpens } from "@hermesh/her-client";
import type { MenuItem } from "../../../../shared/bridge";
import { i18n } from "../../../../shared/i18n";
import type { Store } from "../../store";
import { baseName, type FilesHome, type SessionFile } from "./files";
import { openInPanel, showFolder } from "./panel";

// Where a session's file opens when clicked (docs/session-files.md §3.2): a result to look at
// over the window, code at its lines, what the app does not show in Quick Look.

const t = i18n.getFixedT(null, "session");

/** A file shown over the window. `seq` grows each time, so the same file again still opens. */
export type Shown = { file: SessionFile; home: FilesHome; name: string; line?: number; endLine?: number; seq: number };

let shown: Shown | null = null;
let seq = 0;
const listeners = new Set<() => void>();
const changed = () => listeners.forEach((l) => l());

export function useShownFile(): Shown | null {
  return useSyncExternalStore((l) => (listeners.add(l), () => listeners.delete(l)), () => shown);
}

export function closeShownFile() {
  shown = null;
  changed();
}

/** Opens a session's file the way its kind and lines say. */
export function openSessionFile(st: Store, home: FilesHome, file: SessionFile, at?: { line?: number; endLine?: number }) {
  const name = baseName(file.path);
  const to = fileOpens(file.path, at?.line);
  if (to === "quicklook") {
    void window.hermesh?.quickLookSessionFile(file, name).catch((e: unknown) => st.show(failure(e)));
    return;
  }
  // Code and anything a reply points at a line of: the file panel, beside the conversation.
  if (to === "panel") return openInPanel(file, at);
  shown = { file, home, name, line: at?.line, endLine: at?.endLine, seq: ++seq };
  changed();
}

/** A folder a reply points at: the file panel, its tree opened to it. */
export function openSessionFolder(home: FilesHome, path: string) {
  showFolder(home.session, path);
}

function failure(e: unknown): string {
  return t("files.failed", { message: e instanceof Error ? e.message : String(e) });
}

/** A file's menu, on a tag and in the preview: open it, copy where it is, keep a copy, find it. */
export async function fileMenu(st: Store, home: FilesHome, file: SessionFile, folder: string, opts: { open?: () => void; line?: number } = {}) {
  const name = baseName(file.path);
  const full = folder ? `${folder.replace(/\/+$/, "")}/${file.path}` : file.path;
  const items: MenuItem[] = [
    ...(opts.open ? [{ id: "open", label: t("files.open") }, { separator: true } as const] : []),
    { id: "copyPath", label: t("files.copyPath") },
    { id: "copyRel", label: t("files.copyRelPath") },
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
