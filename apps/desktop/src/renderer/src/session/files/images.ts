import { createContext, useEffect, useSyncExternalStore } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { resolveDocPath, sameImagePath, sessionImageRef } from "@hermesh/her-client";
import { FileContentKind } from "@hermesh/proto/session";
import { sessions } from "../../api";
import type { SessionFile } from "./files";

export type ReplyImageItem = { path: string; caption: string };
export type ReplyImages = { explicit: string[]; gallery: ReplyImageItem[] };

export const ReplyImagesContext = createContext<ReplyImages | null>(null);

export type ImageState =
  | { status: "loading" }
  | { status: "ready"; url: string; width: number; height: number; size: bigint; turn: number; folder: string; resolvedPath: string }
  | { status: "offline" }
  | { status: "missing" }
  | { status: "tooLarge"; size: bigint }
  | { status: "error"; message: string };

const MAX_ENTRIES = 80;
const MAX_IN_FLIGHT = 3;

const store = new Map<string, ImageState>();
const listeners = new Set<() => void>();
const queue: { key: string; file: SessionFile }[] = [];
const queuedKeys = new Set<string>();
const inFlightKeys = new Set<string>();
let activeCount = 0;

const keyOf = (f: SessionFile) => `${f.session}\0${f.run}\0${f.path}`;

function notify() {
  for (const l of listeners) l();
}

function touchLRU(key: string, value: ImageState) {
  if (store.has(key)) store.delete(key);
  store.set(key, value);
  while (store.size > MAX_ENTRIES) {
    const oldest = store.keys().next().value;
    if (oldest === undefined) break;
    const prev = store.get(oldest);
    store.delete(oldest);
    if (prev?.status === "ready") URL.revokeObjectURL(prev.url);
  }
}

export function decodeDimensions(url: string): Promise<{ width: number; height: number }> {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = () => resolve({ width: img.naturalWidth || 0, height: img.naturalHeight || 0 });
    img.onerror = () => resolve({ width: 0, height: 0 });
    img.src = url;
  });
}

function pump() {
  while (activeCount < MAX_IN_FLIGHT && queue.length > 0) {
    const next = queue.shift()!;
    queuedKeys.delete(next.key);
    if (inFlightKeys.has(next.key)) continue;
    const cur = store.get(next.key);
    if (cur && cur.status !== "loading") continue;
    inFlightKeys.add(next.key);
    activeCount++;
    void loadOne(next.key, next.file).finally(() => {
      inFlightKeys.delete(next.key);
      activeCount--;
      pump();
    });
  }
}

async function loadOne(key: string, file: SessionFile) {
  try {
    const res = await sessions.readFile({ sessionId: file.session, runId: file.run, path: file.path });
    if (res.kind === FileContentKind.IMAGE && res.image.length > 0) {
      const url = URL.createObjectURL(new Blob([res.image as BlobPart], { type: res.mimeType || "image/png" }));
      const { width, height } = await decodeDimensions(url);
      touchLRU(key, {
        status: "ready",
        url,
        width,
        height,
        size: res.size,
        turn: res.turn,
        folder: res.folder,
        resolvedPath: res.path || file.path,
      });
    } else if (res.kind === FileContentKind.TOO_LARGE) {
      touchLRU(key, { status: "tooLarge", size: res.size });
    } else {
      touchLRU(key, { status: "missing" });
    }
  } catch (e) {
    const err = ConnectError.from(e);
    if (err.code === Code.Unavailable || err.code === Code.DeadlineExceeded) {
      touchLRU(key, { status: "offline" });
    } else if (err.code === Code.NotFound || err.code === Code.PermissionDenied) {
      touchLRU(key, { status: "missing" });
    } else {
      touchLRU(key, { status: "error", message: err.rawMessage });
    }
  }
  notify();
}

export function requestSessionImage(file: SessionFile, retryOffline = false) {
  const key = keyOf(file);
  const had = store.get(key);
  if (had) {
    if (had.status === "offline" && retryOffline) {
      store.delete(key);
    } else {
      return;
    }
  }
  if (inFlightKeys.has(key) || queuedKeys.has(key)) return;
  touchLRU(key, { status: "loading" });
  queuedKeys.add(key);
  queue.push({ key, file });
  notify();
  pump();
}

export function useSessionImage(file: SessionFile | null, active: boolean, online: boolean): ImageState | undefined {
  const key = file ? keyOf(file) : "";
  const state = useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => (key ? store.get(key) : undefined),
  );
  useEffect(() => {
    if (!file || !active) return;
    requestSessionImage(file, state?.status === "offline" && online);
  }, [key, active, online, state?.status === "offline"]);
  return state;
}

/**
 * Scans settled Markdown text (skipping fenced and inline code) to find `![alt](src)` explicit
 * images and ordered unique gallery items so `[label](path)` never duplicates `![alt](path)`.
 */
export function scanReplyImages(text: string, basePath = ""): ReplyImages {
  // Strip fenced code blocks and single-line inline code spans before scanning links/images.
  const stripped = text
    .replace(/^(?:`{3,}|~{3,})[\s\S]*?^(?:`{3,}|~{3,})/gm, "")
    .replace(/`[^`\n]+`/g, "");
  const found: { bang: boolean; label: string; path: string }[] = [];
  const re = /(!?)\[([^\]]*)\]\(([^)\n]+)\)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(stripped)) !== null) {
    let raw = m[3]!.trim();
    if (raw.startsWith("<") && raw.includes(">")) raw = raw.slice(1, raw.indexOf(">"));
    else {
      const titleIdx = raw.search(/\s+["']/);
      if (titleIdx > 0) raw = raw.slice(0, titleIdx);
    }
    const rawPath = sessionImageRef(raw);
    const path = rawPath ? resolveDocPath(basePath, rawPath) : null;
    if (path) found.push({ bang: m[1] === "!", label: m[2]!.trim(), path });
  }
  const explicit: string[] = [];
  for (const f of found) {
    if (f.bang && !explicit.some((p) => sameImagePath(p, f.path))) explicit.push(f.path);
  }
  const gallery: ReplyImageItem[] = [];
  for (const f of found) {
    if (!f.bang && explicit.some((p) => sameImagePath(p, f.path))) continue;
    if (gallery.some((g) => sameImagePath(g.path, f.path))) continue;
    gallery.push({ path: f.path, caption: f.label });
  }
  return { explicit, gallery };
}
