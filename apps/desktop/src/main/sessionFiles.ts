import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { FileRefKind, SessionService } from "@hermesh/proto/session";
import { app, BrowserWindow, session, shell, WebContentsView, type WebContents } from "electron";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { PageRect, PageSlot, SessionFileRef, SessionPageEvent } from "../shared/bridge";
import { freeName } from "./her";
import { socketPath } from "./paths";

// A coding session's files for the page (docs/session-files.md §3): a page over the window, read
// from the session's machine through this computer's hermeshd; a file in Quick Look, saved to
// Downloads, shown in Finder when the session works on this Mac. The page runs as FinxWork's page
// preview does (main/browser/pagePreview.ts): a view of its own, a process apart from the app, a
// session that keeps nothing and grants nothing; and here with no network at all. It reads only
// its own folder, through the hermesh-file scheme.

export const FILE_SCHEME = "hermesh-file";
const HOST = "files";
const CHUNK = 4 * 1024 * 1024;

function sessionService() {
  return createClient(SessionService, createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } }));
}

/** A reference the page sent, checked: strings, a path within the session's folder. */
export function refOf(v: unknown): SessionFileRef | null {
  if (!v || typeof v !== "object") return null;
  const { session: s, run, path: p } = v as Record<string, unknown>;
  if (typeof s !== "string" || !/^[\w-]{1,128}$/.test(s) || typeof run !== "string" || !/^[\w-]{0,128}$/.test(run)) return null;
  if (typeof p !== "string" || p === "" || p.length > 4096 || p.includes("\0")) return null;
  return { session: s, run, path: p };
}

const readPart = (ref: SessionFileRef, offset: number, length = CHUNK) =>
  sessionService().readFileBytes({ sessionId: ref.session, runId: ref.run, path: ref.path, offset: BigInt(offset), length });

/** A whole file, a part at a time. */
async function readAll(ref: SessionFileRef): Promise<{ data: Buffer; mime: string }> {
  const parts: Buffer[] = [];
  let offset = 0;
  let size = -1;
  let mime = "";
  while (size < 0 || offset < size) {
    const r = await readPart(ref, offset);
    size = Number(r.size);
    mime ||= r.mimeType;
    if (r.data.length === 0) break;
    parts.push(Buffer.from(r.data));
    offset += r.data.length;
  }
  return { data: Buffer.concat(parts), mime };
}

/** Where a file read for the app goes on this Mac: a folder of its own under the system's temporary folder. */
async function fetchTo(ref: SessionFileRef, name: string): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "hermesh-file-"));
  const to = path.join(dir, path.basename(name) || "file");
  await fs.writeFile(to, (await readAll(ref)).data);
  return to;
}

export async function quickLookSessionFile(owner: WebContents, ref: SessionFileRef, name: string) {
  const file = await fetchTo(ref, name);
  const win = BrowserWindow.fromWebContents(owner);
  if (process.platform === "darwin" && win) win.previewFile(file, name);
  else await shell.openPath(file);
}

export async function saveSessionFile(ref: SessionFileRef, name: string) {
  const to = await freeName(app.getPath("downloads"), path.basename(name) || "file");
  await fs.writeFile(to, (await readAll(ref)).data);
  shell.showItemInFolder(to);
}

/** Finder, for a session on this Mac: the file as it is now, where the machine says the session's folder is. */
export async function revealSessionFile(ref: SessionFileRef): Promise<boolean> {
  const res = await sessionService().resolveFiles({ sessionId: ref.session, paths: [ref.path] });
  const f = res.files[0];
  if (!f || res.folderGone || (f.kind !== FileRefKind.FILE && f.kind !== FileRefKind.FOLDER)) return false;
  const full = path.join(res.folder, f.path);
  if (!(await fs.stat(full).then(() => true, () => false))) return false;
  shell.showItemInFolder(full);
  return true;
}

/** The kinds a page may load from its folder (FinxWork's and t3code's rule): no dotfiles, nothing else. */
const webKinds = /\.(html?|css|js|mjs|json|map|png|jpe?g|gif|webp|svg|ico|avif|woff2?|ttf|otf|mp4|webm|mp3|wav|m4a|ogg|txt|csv|wasm)$/i;
const isPage = (p: string) => /\.html?$/i.test(p);
const dirOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/") + 1) : "");

/**
 * A page's folder it may read: the session, the turn, and the folder the page is in. Kept while
 * the app runs: a page's address can be opened again only through openSessionPage, which adds it.
 */
const folders = new Set<string>();
const folderKey = (s: string, run: string, dir: string) => `${s}\0${run}\0${dir}`;

export const pageURL = (ref: SessionFileRef) =>
  `${FILE_SCHEME}://${HOST}/${encodeURIComponent(ref.session)}/${encodeURIComponent(ref.run || "-")}/${ref.path.split("/").map(encodeURIComponent).join("/")}`;

function refOfURL(url: string): SessionFileRef | null {
  try {
    const u = new URL(url);
    if (u.protocol !== `${FILE_SCHEME}:` || u.host !== HOST) return null;
    const [s, run, ...rest] = u.pathname.slice(1).split("/").map(decodeURIComponent);
    const p = rest.join("/");
    return refOf({ session: s, run: run === "-" ? "" : run, path: p });
  } catch {
    return null;
  }
}

/** Whether the page in that folder may read this. */
function mayRead(ref: SessionFileRef): boolean {
  const parts = ref.path.split("/");
  if (parts.some((x) => x === "" || x === "." || x === ".." || x.startsWith("."))) return false;
  if (!webKinds.test(ref.path)) return false;
  for (let dir = dirOf(ref.path); ; dir = dirOf(dir.slice(0, -1))) {
    if (folders.has(folderKey(ref.session, ref.run, dir))) return true;
    if (dir === "") return false;
  }
}

// Scripts and forms, no origin of its own, nothing from anywhere but its folder (docs/session-files.md §9).
const CSP = `default-src ${FILE_SCHEME}: data: blob: 'unsafe-inline' 'unsafe-eval'; form-action 'none'; sandbox allow-scripts allow-forms`;

async function serve(req: Request): Promise<Response> {
  const ref = refOfURL(req.url);
  if (!ref || !mayRead(ref)) return new Response("not found", { status: 404 });
  // The page has no origin of its own (the sandbox): its fetch of a file beside it is cross-origin, and allowed.
  const headers: Record<string, string> = { "content-security-policy": CSP, "cache-control": "no-store", "x-content-type-options": "nosniff", "accept-ranges": "bytes", "access-control-allow-origin": "*" };
  const range = /^bytes=(\d+)-(\d*)$/.exec(req.headers.get("range") ?? "");
  try {
    const start = range ? Number(range[1]) : 0;
    const first = await readPart(ref, start);
    const size = Number(first.size);
    headers["content-type"] = first.mimeType || "application/octet-stream";
    if (range) {
      const end = Math.min(range[2] ? Number(range[2]) : size - 1, start + first.data.length - 1);
      headers["content-range"] = `bytes ${start}-${end}/${size}`;
      return new Response(Buffer.from(first.data.subarray(0, end - start + 1)), { status: 206, headers });
    }
    headers["content-length"] = String(size);
    let offset = first.data.length;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(first.data);
        if (offset >= size || first.data.length === 0) c.close();
      },
      async pull(c) {
        const r = await readPart(ref, offset);
        if (r.data.length === 0) return c.close();
        c.enqueue(r.data);
        offset += r.data.length;
        if (offset >= size) c.close();
      },
    });
    return new Response(body, { status: 200, headers });
  } catch (e) {
    const code = ConnectError.from(e).code;
    return new Response("unavailable", { status: code === Code.NotFound || code === Code.PermissionDenied ? 404 : 502 });
  }
}

/** Not `persist:`: it lives in memory, and a page shown leaves nothing behind. */
const partition = "hermesh-session-page";
let ready = false;
function pageSession() {
  const s = session.fromPartition(partition);
  if (ready) return s;
  ready = true;
  s.setPermissionRequestHandler((_wc, _permission, grant) => grant(false));
  s.setPermissionCheckHandler(() => false);
  s.on("will-download", (_e, item) => item.cancel());
  // No network: the page reads its folder and nothing else.
  s.webRequest.onBeforeRequest({ urls: ["http://*/*", "https://*/*", "ws://*/*", "wss://*/*", "ftp://*/*", "file://*/*"] }, (_d, cb) => cb({ cancel: true }));
  s.protocol.handle(FILE_SCHEME, serve);
  return s;
}

type Preview = { view: WebContentsView; win: BrowserWindow };
/**
 * The pages shown, by window and place: "dialog" (over the window) and "panel" (the file panel). The dialog's
 * is over the panel's, which is hidden while it shows: a view of the system's is drawn over the page's
 * dimmed backdrop, not under it.
 */
const previews = new Map<WebContents, Map<PageSlot, Preview>>();
const watched = new WeakSet<WebContents>();
const rounded = (r: PageRect) => ({ x: Math.round(r.x), y: Math.round(r.y), width: Math.max(1, Math.round(r.width)), height: Math.max(1, Math.round(r.height)) });
const webAddress = (url: string) => /^https?:\/\//i.test(url);

function slotsOf(owner: WebContents) {
  let m = previews.get(owner);
  if (!m) previews.set(owner, (m = new Map()));
  return m;
}

function drop(owner: WebContents, slot: PageSlot) {
  const slots = previews.get(owner);
  const shown = slots?.get(slot);
  if (!shown) return;
  slots!.delete(slot);
  const wc = shown.view.webContents;
  if (!wc.isDestroyed() && wc.isFocused() && !owner.isDestroyed()) owner.focus();
  if (!shown.win.isDestroyed()) shown.win.contentView.removeChildView(shown.view);
  if (!wc.isDestroyed()) wc.close();
  // The dialog gone, the panel's page shows again.
  if (slot === "dialog") slots!.get("panel")?.view.setVisible(true);
}

function watch(owner: WebContents) {
  if (watched.has(owner)) return;
  watched.add(owner);
  owner.once("destroyed", () => (drop(owner, "dialog"), drop(owner, "panel")));
  owner.on("did-navigate", () => (closeSessionPage(owner, "dialog"), closeSessionPage(owner, "panel")));
}

/** Shows a session's page in the rectangle given, in place of any before it in that place. */
export async function openSessionPage(owner: WebContents, ref: SessionFileRef, rect: PageRect, slot: PageSlot): Promise<boolean> {
  if (!isPage(ref.path)) return false;
  drop(owner, slot);
  const win = BrowserWindow.fromWebContents(owner);
  if (!win) return false;
  watch(owner);
  pageSession();
  folders.add(folderKey(ref.session, ref.run, dirOf(ref.path)));
  const view = new WebContentsView({ webPreferences: { partition, sandbox: true, contextIsolation: true, nodeIntegration: false, spellcheck: false, disableDialogs: true } });
  view.setBackgroundColor("#ffffff");
  view.setBorderRadius(slot === "dialog" ? 8 : 0);
  const wc = view.webContents;
  const tell = (event: SessionPageEvent) => void (!owner.isDestroyed() && owner.send("session-page", { ...event, slot }));
  wc.setWindowOpenHandler(({ url }) => {
    if (webAddress(url)) tell({ kind: "link", url });
    return { action: "deny" };
  });
  wc.on("will-navigate", (e, to) => {
    const next = refOfURL(to);
    if (next && next.session === ref.session && next.run === ref.run && isPage(next.path) && mayRead(next)) return;
    e.preventDefault();
    if (webAddress(to)) tell({ kind: "link", url: to });
  });
  wc.on("will-attach-webview", (e) => e.preventDefault());
  // Once the page has the keys, Esc would never reach the dialog.
  wc.on("before-input-event", (e, input) => {
    if (input.type !== "keyDown" || input.key !== "Escape") return;
    e.preventDefault();
    tell({ kind: "close" });
  });
  const slots = slotsOf(owner);
  slots.set(slot, { view, win });
  win.contentView.addChildView(view);
  view.setBounds(rounded(rect));
  if (slot === "dialog") slots.get("panel")?.view.setVisible(false);
  else if (slots.has("dialog")) view.setVisible(false);
  void wc.loadURL(pageURL(ref)).catch(() => {});
  return true;
}

export function placeSessionPage(owner: WebContents, rect: PageRect, slot: PageSlot) {
  previews.get(owner)?.get(slot)?.view.setBounds(rounded(rect));
}

export function closeSessionPage(owner: WebContents, slot: PageSlot) {
  drop(owner, slot);
}

/** The page and the web files of its folder (to three levels, 300 files, 64 MB), in the default browser. */
export async function openSessionPageInBrowser(ref: SessionFileRef) {
  if (!isPage(ref.path)) throw new Error("not a page");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "hermesh-page-"));
  const base = dirOf(ref.path);
  let files = 0;
  let bytes = 0;
  const copy = async (dir: string, depth: number) => {
    const list = await sessionService().listFiles({ sessionId: ref.session, runId: ref.run, dir: dir.replace(/\/$/, "") });
    for (const e of list.entries) {
      if (e.name.startsWith(".") || files >= 300 || bytes > 64 * 1024 * 1024) continue;
      const rel = dir + e.name;
      if (e.dir) {
        if (depth < 3) await copy(rel + "/", depth + 1);
        continue;
      }
      if (!webKinds.test(e.name)) continue;
      const { data } = await readAll({ ...ref, path: rel });
      const to = path.join(root, ...rel.slice(base.length).split("/"));
      await fs.mkdir(path.dirname(to), { recursive: true });
      await fs.writeFile(to, data);
      files++;
      bytes += data.length;
    }
  };
  await copy(base, 0);
  const page = path.join(root, ...ref.path.slice(base.length).split("/"));
  if (!(await fs.stat(page).then(() => true, () => false))) await fs.writeFile(page, (await readAll(ref)).data);
  const err = await shell.openPath(page);
  if (err) throw new Error(err);
}
