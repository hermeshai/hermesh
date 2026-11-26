import { createContext, useContext, useEffect, useSyncExternalStore } from "react";
import { FileRefKind, type ResolvedFile } from "@hermesh/proto/session";
import { sessions } from "../../api";

// A session's files as its replies point at them (docs/session-files.md §3, §6): which of the
// paths a reply writes are files, asked of the session's machine once per reply, a frame's
// worth at a time, and kept. Nothing is read until a file is opened.

/** The session a conversation shows: whose machine its files are on, and whether that is this Mac. */
export type FilesHome = { session: string; machine: string; local: boolean };
export const FilesHomeContext = createContext<FilesHome | null>(null);
/** The turn a reply belongs to: its links open the files as that turn left them. */
export const FilesRunContext = createContext("");

/** A file of a session, at a turn ("" for as it is now). */
export type SessionFile = {
  session: string;
  run: string;
  path: string;
  /** A file the owner sent with a prompt, read from its blob on the machine; `path` is its name. */
  blob?: { machine: string; sha: string };
};

/** What the machine said a path is; null while it is asked. */
export type FileInfo = { kind: FileRefKind; path: string; size: bigint; turn: number; folder: string };

const known = new Map<string, FileInfo>();
const listeners = new Set<() => void>();
const queued = new Map<string, Set<string>>();
let scheduled = false;
/** A path read as it is now (its turn had no checkpoint yet) is asked again after this long. */
const NOW_FOR = 10_000;
const askedAt = new Map<string, number>();

const keyOf = (session: string, run: string, path: string) => `${session}\0${run}\0${path}`;

function changed() {
  for (const l of listeners) l();
}

async function ask(session: string, run: string, paths: string[]) {
  try {
    const res = await sessions.resolveFiles({ sessionId: session, runId: run, paths });
    res.files.forEach((f: ResolvedFile, i: number) => {
      known.set(keyOf(session, run, paths[i]!), { kind: f.kind, path: f.path, size: f.size, turn: f.turn, folder: res.folder });
    });
  } catch {
    // The machine away: the paths stay links that try when clicked.
    for (const p of paths) known.set(keyOf(session, run, p), { kind: FileRefKind.UNSPECIFIED, path: p, size: 0n, turn: 0, folder: "" });
  }
  changed();
}

function flush() {
  scheduled = false;
  for (const [group, paths] of queued) {
    const [session, run] = group.split("\0") as [string, string];
    void ask(session, run, [...paths]);
  }
  queued.clear();
}

function want(session: string, run: string, path: string) {
  const key = keyOf(session, run, path);
  const had = known.get(key);
  if (had && !(had.turn === 0 && Date.now() - (askedAt.get(key) ?? 0) > NOW_FOR)) return;
  if (askedAt.has(key) && Date.now() - askedAt.get(key)! < NOW_FOR && !had) return;
  askedAt.set(key, Date.now());
  const group = `${session}\0${run}`;
  if (!queued.has(group)) queued.set(group, new Set());
  queued.get(group)!.add(path);
  if (!scheduled) {
    scheduled = true;
    requestAnimationFrame(flush);
  }
}

/** What `path` (as a reply wrote it) is in this reply's session and turn; undefined while asked, or outside a session. */
export function useFileInfo(path: string | null): FileInfo | undefined {
  const home = useContext(FilesHomeContext);
  const run = useContext(FilesRunContext);
  const key = home && path ? keyOf(home.session, run, path) : "";
  useEffect(() => {
    if (home && path) want(home.session, run, path);
  }, [key]);
  return useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => (key ? known.get(key) : undefined),
  );
}

/** Whether a path that was found is something to open. */
export const opens = (info: FileInfo | undefined) =>
  !info || info.kind === FileRefKind.FILE || info.kind === FileRefKind.FOLDER || info.kind === FileRefKind.UNSPECIFIED;

/** The file's name. */
export const baseName = (path: string) => path.replace(/[\\/]+$/, "").split(/[\\/]/).pop() ?? path;

/** Bytes as people read them. */
export function sizeText(n: bigint | number): string {
  const b = Number(n);
  if (b < 1024) return `${b} B`;
  const units = ["KB", "MB", "GB"];
  let v = b / 1024;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[u]}`;
}

/** A whole file's bytes, a part at a time. */
export async function readAll(f: SessionFile): Promise<{ data: Uint8Array; mime: string; turn: number }> {
  const parts: Uint8Array[] = [];
  let offset = 0n;
  let size = -1n;
  let mime = "";
  let turn = 0;
  while (size < 0n || offset < size) {
    const r = await sessions.readFileBytes({ sessionId: f.session, runId: f.run, path: f.path, offset });
    size = r.size;
    mime ||= r.mimeType;
    turn = r.turn;
    if (r.data.length === 0) break;
    parts.push(r.data);
    offset += BigInt(r.data.length);
  }
  const data = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    data.set(p, at);
    at += p.length;
  }
  return { data, mime, turn };
}
