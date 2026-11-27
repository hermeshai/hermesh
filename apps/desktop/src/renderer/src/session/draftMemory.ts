import type { AttachmentRef } from "@hermesh/proto/session";
import type { ChoiceStore } from "./choiceMemory.ts";

// Kept free of runtime proto imports so Node's strip-only test runner runs draft tests directly.

export const NEW_SESSION_DRAFT = "new";
export const MAX_DRAFTS = 50;

const storageKey = "session-drafts";

export interface DraftStore extends ChoiceStore {
  removeItem?(k: string): void;
}

export type DraftAttachment = Pick<AttachmentRef, "blobSha256" | "name" | "mimeType" | "sizeBytes">;

export interface ComposerDraft {
  readonly text: string;
  readonly attachments: readonly DraftAttachment[];
}

export const EMPTY_DRAFT: ComposerDraft = Object.freeze({ text: "", attachments: Object.freeze([]) });

interface StoredAttachment {
  blobSha256: string;
  name: string;
  mimeType: string;
  sizeBytes: string;
}

interface StoredDraftEntry {
  key: string;
  text: string;
  attachments: StoredAttachment[];
}

export function sessionDraftKey(sessionId?: string): string {
  return sessionId ? `session:${sessionId}` : NEW_SESSION_DRAFT;
}

export function isDraftEmpty(draft: ComposerDraft): boolean {
  return draft.text.trim() === "" && draft.attachments.length === 0;
}

function cleanAttachment(a: DraftAttachment): DraftAttachment {
  return {
    blobSha256: a.blobSha256,
    name: a.name,
    mimeType: a.mimeType,
    sizeBytes: typeof a.sizeBytes === "bigint" ? a.sizeBytes : BigInt(a.sizeBytes ?? 0),
  };
}

function mergeAttachments(first: readonly DraftAttachment[], second: readonly DraftAttachment[]): DraftAttachment[] {
  const seen = new Set<string>();
  const out: DraftAttachment[] = [];
  for (const a of [...first, ...second]) {
    if (!a.blobSha256 || seen.has(a.blobSha256)) continue;
    seen.add(a.blobSha256);
    out.push(cleanAttachment(a));
  }
  return out;
}

function parseBigInt(raw: unknown): bigint {
  if (typeof raw === "bigint") return raw;
  if (typeof raw === "number" && Number.isFinite(raw) && raw >= 0) return BigInt(Math.trunc(raw));
  if (typeof raw === "string" && /^\d+$/.test(raw)) return BigInt(raw);
  return 0n;
}

function parseEntries(raw: string | null): Map<string, ComposerDraft> {
  const map = new Map<string, ComposerDraft>();
  if (!raw) return map;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return map;
    for (const item of parsed) {
      if (!item || typeof item !== "object") continue;
      const entry = item as Partial<StoredDraftEntry>;
      if (typeof entry.key !== "string" || !entry.key) continue;
      const text = typeof entry.text === "string" ? entry.text : "";
      const attachments: DraftAttachment[] = Array.isArray(entry.attachments)
        ? entry.attachments
            .filter((x): x is StoredAttachment => !!x && typeof x === "object" && typeof (x as StoredAttachment).blobSha256 === "string" && (x as StoredAttachment).blobSha256 !== "")
            .map((x) => ({
              blobSha256: x.blobSha256,
              name: typeof x.name === "string" ? x.name : "",
              mimeType: typeof x.mimeType === "string" ? x.mimeType : "application/octet-stream",
              sizeBytes: parseBigInt(x.sizeBytes),
            }))
        : [];
      const draft: ComposerDraft = { text, attachments };
      if (!isDraftEmpty(draft)) {
        map.delete(entry.key);
        map.set(entry.key, draft);
      }
    }
  } catch {
    // Corrupted storage falls back to an empty draft map.
  }
  return map;
}

function serializeEntries(map: Map<string, ComposerDraft>): string {
  const out: StoredDraftEntry[] = [];
  for (const [key, draft] of map) {
    if (isDraftEmpty(draft)) continue;
    out.push({
      key,
      text: draft.text,
      attachments: draft.attachments.map((a) => ({
        blobSha256: a.blobSha256,
        name: a.name,
        mimeType: a.mimeType,
        sizeBytes: a.sizeBytes.toString(),
      })),
    });
  }
  return JSON.stringify(out);
}

const defaultStore = (): DraftStore | undefined => (typeof localStorage === "undefined" ? undefined : localStorage);

export function createDraftManager(store = defaultStore(), maxDrafts = MAX_DRAFTS) {
  // Active typing state in memory (including transient whitespace before words are typed),
  // while persisted storage keeps only non-empty drafts bounded by LRU.
  let persisted: Map<string, ComposerDraft> | null = null;
  const live = new Map<string, ComposerDraft>();
  const listeners = new Set<() => void>();

  const ensureLoaded = (): Map<string, ComposerDraft> => {
    if (!persisted) {
      let raw: string | null = null;
      try {
        raw = store?.getItem(storageKey) ?? null;
      } catch {
        raw = null;
      }
      persisted = parseEntries(raw);
      while (persisted.size > maxDrafts) {
        persisted.delete(persisted.keys().next().value!);
      }
    }
    return persisted;
  };

  const flush = (map: Map<string, ComposerDraft>) => {
    try {
      if (map.size === 0) {
        if (store?.removeItem) store.removeItem(storageKey);
        else store?.setItem(storageKey, "[]");
      } else {
        store?.setItem(storageKey, serializeEntries(map));
      }
    } catch {
      // Without storage, drafts still stick in memory while the window is open.
    }
  };

  const notify = () => {
    for (const l of listeners) l();
  };

  const read = (key: string): ComposerDraft => {
    if (!key) return EMPTY_DRAFT;
    const active = live.get(key);
    if (active) return active;
    return ensureLoaded().get(key) ?? EMPTY_DRAFT;
  };

  const save = (key: string, next: ComposerDraft) => {
    if (!key) return;
    const map = ensureLoaded();
    const prev = read(key);
    const normalized: ComposerDraft = {
      text: next.text,
      attachments: mergeAttachments(next.attachments, []),
    };
    const sameAttachments =
      prev.attachments.length === normalized.attachments.length &&
      prev.attachments.every((a, i) => a.blobSha256 === normalized.attachments[i]?.blobSha256);
    if (prev.text === normalized.text && sameAttachments) return;

    if (normalized.text === "" && normalized.attachments.length === 0) {
      live.delete(key);
      if (map.delete(key)) flush(map);
      notify();
      return;
    }

    if (isDraftEmpty(normalized)) {
      // Whitespace-only while typing: keep live so Space isn't swallowed, but don't persist blank entries.
      live.set(key, normalized);
      if (map.delete(key)) flush(map);
      notify();
      return;
    }

    live.delete(key);
    map.delete(key);
    map.set(key, normalized);
    while (map.size > maxDrafts) {
      const oldest = map.keys().next().value!;
      map.delete(oldest);
      live.delete(oldest);
    }
    flush(map);
    notify();
  };

  const setText = (key: string, text: string) => {
    const cur = read(key);
    save(key, { text, attachments: cur.attachments });
  };

  const addAttachment = (key: string, att: DraftAttachment) => {
    const cur = read(key);
    if (cur.attachments.some((a) => a.blobSha256 === att.blobSha256)) return;
    save(key, { text: cur.text, attachments: [...cur.attachments, cleanAttachment(att)] });
  };

  const removeAttachment = (key: string, blobSha256: string) => {
    const cur = read(key);
    if (!cur.attachments.some((a) => a.blobSha256 === blobSha256)) return;
    save(key, { text: cur.text, attachments: cur.attachments.filter((a) => a.blobSha256 !== blobSha256) });
  };

  const clear = (key: string) => {
    if (!key) return;
    const map = ensureLoaded();
    const hadLive = live.delete(key);
    const hadPersisted = map.delete(key);
    if (hadPersisted) flush(map);
    if (hadLive || hadPersisted) notify();
  };

  const restore = (key: string, failed: ComposerDraft): ComposerDraft => {
    if (!key || isDraftEmpty(failed)) return read(key);
    const cur = read(key);
    const merged: ComposerDraft = {
      text: [failed.text, cur.text].filter((s) => s.trim() !== "").join("\n"),
      attachments: mergeAttachments(failed.attachments, cur.attachments),
    };
    save(key, merged);
    return read(key);
  };

  const move = (fromKey: string, toKey: string) => {
    if (!fromKey || !toKey || fromKey === toKey) return;
    const carried = read(fromKey);
    clear(fromKey);
    if (isDraftEmpty(carried)) return;
    const existing = read(toKey);
    save(toKey, {
      text: [existing.text, carried.text].filter((s) => s.trim() !== "").join("\n"),
      attachments: mergeAttachments(existing.attachments, carried.attachments),
    });
  };

  const clearAll = () => {
    const map = ensureLoaded();
    const had = live.size > 0 || map.size > 0;
    live.clear();
    map.clear();
    flush(map);
    if (had) notify();
  };

  const subscribe = (listener: () => void): (() => void) => {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  };

  return {
    read,
    save,
    setText,
    addAttachment,
    removeAttachment,
    clear,
    restore,
    move,
    forgetSession: (sessionId: string) => sessionId && clear(sessionDraftKey(sessionId)),
    clearAll,
    subscribe,
  };
}

const drafts = createDraftManager();

export const readDraft = drafts.read;
export const saveDraft = drafts.save;
export const setDraftText = drafts.setText;
export const addDraftAttachment = drafts.addAttachment;
export const removeDraftAttachment = drafts.removeAttachment;
export const clearDraft = drafts.clear;
export const restoreDraft = drafts.restore;
export const moveDraft = drafts.move;
export const forgetSessionDraft = drafts.forgetSession;
export const clearAllDrafts = drafts.clearAll;
export const subscribeDrafts = drafts.subscribe;
