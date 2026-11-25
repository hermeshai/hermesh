import { useSyncExternalStore } from "react";
import type { ListFilesResponse, ReadFileResponse } from "@hermesh/proto/session";
import { sessions } from "../../api";
import { baseName, type SessionFile } from "./files";

// The file panel's files (docs/session-files.md §3.4, FinxWork's files/state.ts), by session: the
// open tabs, each a file at a turn ("" for as it is now), and the folders of the tree, the folder as
// it is now. Kept outside the components, so a panel that is closed, or a session left for another,
// finds them as they were.

/** Lines to bring into view and mark; `seq` grows each time, so the same lines again still move there. */
export type Target = { line: number; endLine?: number; seq: number };

export type Tab = {
  key: string;
  file: SessionFile;
  name: string;
  /** How it read, once it did; or why it did not. */
  read: { res: ReadFileResponse } | { error: unknown } | null;
  target: Target | null;
  /** Markdown and SVG: as they look, not their source. */
  preview: boolean;
  /** The turn of the reply it was opened from ("" from the tree): where "Show Version at Reply" goes back to. */
  replyRun: string;
};

/** A folder of the tree: its entries once read. */
export type Dir = { list: ListFilesResponse | null; error: unknown };

export type Panel = {
  session: string;
  open: boolean;
  /** Taking the window but the side bar. */
  max: boolean;
  tabs: Tab[];
  active: string | null;
  /** The tree is shown; null until the panel first shows, which decides by its width. */
  tree: boolean | null;
  expanded: Set<string>;
  dirs: Map<string, Dir>;
  rev: number;
};

const panels = new Map<string, Panel>();
const listeners = new Set<() => void>();
let seq = 0;

const keyOf = (f: SessionFile) => `${f.run}\0${f.path}`;

function changed(p: Panel) {
  p.rev++;
  for (const l of listeners) l();
}

export function panelOf(session: string): Panel {
  let p = panels.get(session);
  if (!p) {
    p = { session, open: false, max: false, tabs: [], active: null, tree: null, expanded: new Set(), dirs: new Map(), rev: 0 };
    panels.set(session, p);
  }
  return p;
}

export function usePanel(session: string): Panel {
  const p = panelOf(session);
  useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => p.rev,
  );
  return p;
}

export function setOpen(p: Panel, open: boolean) {
  p.open = open;
  if (!open) p.max = false;
  changed(p);
}

export function setMax(p: Panel, max: boolean) {
  p.max = max;
  changed(p);
}

export function setTree(p: Panel, on: boolean) {
  p.tree = on;
  changed(p);
}

export function setPreview(p: Panel, tab: Tab, on: boolean) {
  tab.preview = on;
  changed(p);
}

async function read(p: Panel, tab: Tab) {
  try {
    tab.read = { res: await sessions.readFile({ sessionId: tab.file.session, runId: tab.file.run, path: tab.file.path }) };
  } catch (error) {
    tab.read = { error };
  }
  changed(p);
}

/** Opens a file in the panel, at its lines; the tab it already has comes forward. */
export function openInPanel(file: SessionFile, at?: { line?: number; endLine?: number }) {
  const p = panelOf(file.session);
  const key = keyOf(file);
  let tab = p.tabs.find((t) => t.key === key);
  if (!tab) {
    tab = { key, file, name: baseName(file.path), read: null, target: null, preview: true, replyRun: file.run };
    p.tabs.push(tab);
    void read(p, tab);
  }
  if (at?.line !== undefined) tab.target = { line: at.line, endLine: at.endLine, seq: ++seq };
  p.active = key;
  p.open = true;
  revealInTree(p, file.path);
  changed(p);
}

/** The tab's file read again: as it is now, or at another turn. */
export function switchRun(p: Panel, tab: Tab, run: string) {
  const file = { ...tab.file, run };
  const existing = p.tabs.find((t) => t.key === keyOf(file));
  if (existing) {
    p.active = existing.key;
    changed(p);
    return;
  }
  const next: Tab = { ...tab, key: keyOf(file), file, read: null };
  p.tabs.splice(p.tabs.indexOf(tab), 1, next);
  p.active = next.key;
  void read(p, next);
  changed(p);
}

export function activate(p: Panel, key: string) {
  p.active = key;
  const tab = p.tabs.find((t) => t.key === key);
  if (tab) revealInTree(p, tab.file.path);
  changed(p);
}

export function closeTab(p: Panel, key: string) {
  const i = p.tabs.findIndex((t) => t.key === key);
  if (i < 0) return;
  p.tabs.splice(i, 1);
  if (p.active === key) p.active = p.tabs[Math.min(i, p.tabs.length - 1)]?.key ?? null;
  changed(p);
}

/** The folders above a path, from the session's folder down. */
function parents(path: string): string[] {
  const parts = path.split("/").slice(0, -1);
  return parts.map((_, i) => parts.slice(0, i + 1).join("/"));
}

/** The tree opened down to a file or folder. */
function revealInTree(p: Panel, path: string) {
  for (const d of parents(path)) {
    p.expanded.add(d);
    if (!p.dirs.has(d)) void listDir(p, d);
  }
}

/** Opens the panel with the tree opened to a folder (a folder tag in a reply). */
export function showFolder(session: string, dir: string) {
  const p = panelOf(session);
  p.open = true;
  p.tree = true;
  revealInTree(p, `${dir.replace(/\/+$/, "")}/x`);
  changed(p);
}

/** Reads a folder's entries (again). */
export async function listDir(p: Panel, dir: string) {
  if (!p.dirs.has(dir)) p.dirs.set(dir, { list: null, error: null });
  try {
    p.dirs.set(dir, { list: await sessions.listFiles({ sessionId: p.session, dir }), error: null });
  } catch (error) {
    p.dirs.set(dir, { list: p.dirs.get(dir)?.list ?? null, error });
  }
  changed(p);
}

export function toggleDir(p: Panel, dir: string) {
  if (p.expanded.has(dir)) p.expanded.delete(dir);
  else {
    p.expanded.add(dir);
    if (!p.dirs.has(dir)) void listDir(p, dir);
  }
  changed(p);
}

/** The folders shown read again (a turn ended, the window came back). */
export function refreshTree(p: Panel) {
  for (const d of ["", ...p.expanded]) if (p.dirs.has(d)) void listDir(p, d);
}
