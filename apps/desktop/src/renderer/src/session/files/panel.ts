import { useSyncExternalStore } from "react";
import { fileView } from "@hermesh/her-client";
import type { ListFilesResponse, ReadFileResponse } from "@hermesh/proto/session";
import { sessions } from "../../api";
import { baseName, type SessionFile } from "./files";

// The side panel's tabs (docs/session-files.md §3.4, FinxWork's files/state.ts), by session: each
// tab a kind, and the folders of the tree, the folder as it is now. Kept outside the components, so a
// panel that is closed, or a session left for another, finds them as they were.

/** Lines to bring into view and mark; `seq` grows each time, so the same lines again still move there. */
export type Target = { line: number; endLine?: number; seq: number };

/**
 * What a tab shows. "files": a file at a turn ("" for as it is now), the tree beside it or not; with no
 * path, the tree alone (项目文件). "page": a .html running. Another kind (a terminal) is another tab.
 */
export type TabKind = "files" | "page";

export type Tab = {
  /** Its own, for as long as it is open: a files tab goes from file to file. */
  key: string;
  kind: TabKind;
  file: SessionFile;
  name: string;
  /** How it read, once it did; or why it did not. */
  read: { res: ReadFileResponse } | { error: unknown } | null;
  target: Target | null;
  /** Markdown and SVG: as they look, not their source. */
  preview: boolean;
  /** The turn of the reply it was opened from ("" from the tree): where "Show Version at Reply" goes back to. */
  replyRun: string;
  /** The tree beside the file (a files tab). */
  tree: boolean;
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
  expanded: Set<string>;
  dirs: Map<string, Dir>;
  rev: number;
};

const panels = new Map<string, Panel>();
const listeners = new Set<() => void>();
let seq = 0;
let tabs = 0;

const same = (a: SessionFile, b: SessionFile) => a.run === b.run && a.path === b.path;
const kindOf = (path: string): TabKind => (fileView(path) === "page" ? "page" : "files");

function changed(p: Panel) {
  p.rev++;
  for (const l of listeners) l();
}

export function panelOf(session: string): Panel {
  let p = panels.get(session);
  if (!p) {
    p = { session, open: false, max: false, tabs: [], active: null, expanded: new Set(), dirs: new Map(), rev: 0 };
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

/** Opened with nothing in it, it starts with 项目文件. */
export function setOpen(p: Panel, open: boolean) {
  if (open && p.tabs.length === 0) return openProject(p);
  p.open = open;
  if (!open) p.max = false;
  changed(p);
}

export function setMax(p: Panel, max: boolean) {
  p.max = max;
  changed(p);
}

export function setTree(p: Panel, tab: Tab, on: boolean) {
  tab.tree = on;
  changed(p);
}

export function setPreview(p: Panel, tab: Tab, on: boolean) {
  tab.preview = on;
  changed(p);
}

async function read(p: Panel, tab: Tab) {
  const file = tab.file;
  let got: Tab["read"];
  try {
    got = { res: await sessions.readFile({ sessionId: file.session, runId: file.run, path: file.path }) };
  } catch (error) {
    got = { error };
  }
  // The tab may have gone on to another file meanwhile.
  if (tab.file !== file) return;
  tab.read = got;
  changed(p);
}

/** A new tab, after the one in front. */
function add(p: Panel, kind: TabKind, file: SessionFile, tree: boolean): Tab {
  const tab: Tab = { key: `t${++tabs}`, kind, file, name: baseName(file.path), read: null, target: null, preview: true, replyRun: file.run, tree };
  const at = p.tabs.findIndex((t) => t.key === p.active);
  p.tabs.splice(at < 0 ? p.tabs.length : at + 1, 0, tab);
  if (file.path) void read(p, tab);
  return tab;
}

/** Opens a file a reply names in a tab of its own, at its lines; the tab it already has comes forward. */
export function openInPanel(file: SessionFile, at?: { line?: number; endLine?: number }) {
  const p = panelOf(file.session);
  const kind = kindOf(file.path);
  const tab = p.tabs.find((t) => t.kind === kind && same(t.file, file)) ?? add(p, kind, file, false);
  if (at?.line !== undefined) tab.target = { line: at.line, endLine: at.endLine, seq: ++seq };
  p.active = tab.key;
  p.open = true;
  revealInTree(p, file.path);
  changed(p);
}

/** A file picked in the tree: in the tab the tree is in, as a browser goes to a page; a .html is a page, a tab of its own. */
export function openFromTree(p: Panel, path: string) {
  const file = { session: p.session, run: "", path };
  const tab = p.tabs.find((t) => t.key === p.active);
  if (kindOf(path) === "page" || !tab || tab.kind !== "files") return openInPanel(file);
  go(p, tab, file);
  tab.replyRun = "";
  changed(p);
}

/** The tab shows another file (or another turn of it). */
function go(p: Panel, tab: Tab, file: SessionFile) {
  Object.assign(tab, { file, name: baseName(file.path), read: null, target: null, preview: true });
  void read(p, tab);
}

/** 项目文件: the tree alone, a tab of the files kind with no file yet. */
export function openProject(p: Panel) {
  const tab = p.tabs.find((t) => t.kind === "files" && !t.file.path) ?? add(p, "files", { session: p.session, run: "", path: "" }, true);
  p.active = tab.key;
  p.open = true;
  changed(p);
}

/** The tab's file read again: as it is now, or at another turn. */
export function switchRun(p: Panel, tab: Tab, run: string) {
  const file = { ...tab.file, run };
  const existing = p.tabs.find((t) => t !== tab && t.kind === tab.kind && same(t.file, file));
  if (existing) p.active = existing.key;
  else go(p, tab, file);
  changed(p);
}

export function activate(p: Panel, key: string) {
  p.active = key;
  const tab = p.tabs.find((t) => t.key === key);
  if (tab?.file.path) revealInTree(p, tab.file.path);
  changed(p);
}

export function closeTab(p: Panel, key: string) {
  const i = p.tabs.findIndex((t) => t.key === key);
  if (i < 0) return;
  p.tabs.splice(i, 1);
  if (p.active === key) p.active = p.tabs[Math.min(i, p.tabs.length - 1)]?.key ?? null;
  // The last one closed, the panel goes.
  if (p.tabs.length === 0) (p.open = false), (p.max = false);
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

/** Opens 项目文件 with the tree opened to a folder (a folder tag in a reply). */
export function showFolder(session: string, dir: string) {
  const p = panelOf(session);
  revealInTree(p, `${dir.replace(/\/+$/, "")}/x`);
  openProject(p);
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
