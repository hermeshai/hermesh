import { createContext, useEffect, useSyncExternalStore } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { sameImagePath } from "@hermesh/her-client";
import { FileContentKind } from "@hermesh/proto/session";
import { sessions } from "../../api";
import {
  extractPureImageSlice,
  isTallStageImage,
  isUltraLongImage,
  scanReplyImages,
  shortImageTag,
  TALL_STAGE_RATIO,
  ULTRA_LONG_RATIO,
  type ReplyImageItem,
  type ReplyImages,
} from "../markdown/parse";
import type { SessionFile } from "./files";

export {
  extractPureImageSlice,
  isTallStageImage,
  isUltraLongImage,
  scanReplyImages,
  shortImageTag,
  TALL_STAGE_RATIO,
  ULTRA_LONG_RATIO,
  type ReplyImageItem,
  type ReplyImages,
};

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

export function requestSessionImage(file: SessionFile, retryOffline = false, urgent = false) {
  const key = keyOf(file);
  const had = store.get(key);
  if (had) {
    if (had.status === "offline" && retryOffline) {
      store.delete(key);
    } else {
      if (urgent && queuedKeys.has(key)) {
        const idx = queue.findIndex((q) => q.key === key);
        if (idx > 0) {
          const [picked] = queue.splice(idx, 1);
          queue.unshift(picked!);
        }
      }
      return;
    }
  }
  if (inFlightKeys.has(key)) return;
  if (queuedKeys.has(key)) {
    if (urgent) {
      const idx = queue.findIndex((q) => q.key === key);
      if (idx > 0) {
        const [picked] = queue.splice(idx, 1);
        queue.unshift(picked!);
      }
    }
    return;
  }
  touchLRU(key, { status: "loading" });
  queuedKeys.add(key);
  if (urgent) queue.unshift({ key, file });
  else queue.push({ key, file });
  notify();
  pump();
}

export function useSessionImage(file: SessionFile | null, active: boolean, online: boolean, urgent = false): ImageState | undefined {
  const key = file ? keyOf(file) : "";
  const state = useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => (key ? store.get(key) : undefined),
  );
  useEffect(() => {
    if (!file || !active) return;
    requestSessionImage(file, state?.status === "offline" && online, urgent);
  }, [key, active, online, urgent, state?.status === "offline"]);
  return state;
}

export function useAnyTallSessionImage(session: string, run: string, paths: string[]): boolean {
  return useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => {
      if (!session || paths.length === 0) return false;
      const prefix = `${session}\0${run}\0`;
      for (const p of paths) {
        const direct = store.get(`${prefix}${p}`);
        if (direct?.status === "ready" && isTallStageImage(direct.width, direct.height)) return true;
      }
      for (const [k, v] of store) {
        if (!k.startsWith(prefix) || v.status !== "ready" || !isTallStageImage(v.width, v.height)) continue;
        if (paths.some((p) => sameImagePath(v.resolvedPath, p))) return true;
      }
      return false;
    },
  );
}
