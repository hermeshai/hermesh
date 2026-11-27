// Pure search-palette helpers (kept free of runtime proto enum imports so
// Node's strip-only test runner executes them directly).
import type { SessionFilter } from "@hermesh/core";

export type HighlightPart = { text: string; match: boolean };

/** Splits text into non-matching and matching runs for case-insensitive query highlighting. */
export function splitHighlight(text: string, query: string): HighlightPart[] {
  if (!text) return [];
  const needle = query.trim();
  if (!needle) return [{ text, match: false }];
  const escaped = needle.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const parts = text.split(new RegExp(`(${escaped})`, "gi"));
  const out: HighlightPart[] = [];
  const lower = needle.toLowerCase();
  for (const part of parts) {
    if (!part) continue;
    out.push({ text: part, match: part.toLowerCase() === lower });
  }
  return out;
}

export function hasActiveSearchFilter(f: SessionFilter): boolean {
  return Boolean(f.grant || f.project || f.agent || f.machine || f.archived);
}

/**
 * Essential filter keys shown in the toolbar:
 * - project & agent whenever any exist (or already active)
 * - machine only when >1 environment exists (or already active)
 * - grant hidden by default; shown as a removable chip when deep-linked (e.g. from Settings › Grants)
 */
export function visibleFilterKeys(opts: {
  projectsCount: number;
  agentsCount: number;
  machinesCount: number;
  filter: SessionFilter;
}): ("project" | "agent" | "machine" | "grant")[] {
  const { projectsCount, agentsCount, machinesCount, filter } = opts;
  const keys: ("project" | "agent" | "machine" | "grant")[] = [];
  if (projectsCount > 0 || filter.project) keys.push("project");
  if (agentsCount > 0 || filter.agent) keys.push("agent");
  if (machinesCount > 1 || filter.machine) keys.push("machine");
  if (filter.grant) keys.push("grant");
  return keys;
}

/** Hide generic quick actions when the query is empty and the owner is narrowing tasks by a filter. */
export function shouldShowActions(query: string, filter: SessionFilter): boolean {
  return query.trim() !== "" || !hasActiveSearchFilter(filter);
}

export type SessionBranchInput = {
  branch?: string;
  workspaceStrategy?: { branch?: string };
  gitStatus?: { branch?: string };
};

export function sessionBranch(s: SessionBranchInput): string {
  return s.gitStatus?.branch || s.workspaceStrategy?.branch || s.branch || "";
}

export type SessionExcerpt = {
  kind: "waiting" | "failed" | "running" | "latest";
  text: string;
};

export type SessionExcerptInput = {
  waiting: boolean;
  failed: boolean;
  active: boolean;
  error?: string;
  result?: string;
  progress?: {
    lastMessageExcerpt?: string;
    currentTool?: string;
    lastFailure?: { message?: string };
    pendingQuestion?: { items?: { text?: string }[] };
  };
};

const cleanExcerpt = (raw: string | undefined): string => (raw ? raw.replace(/\s+/g, " ").trim() : "");

export function buildSessionExcerpt(s: SessionExcerptInput): SessionExcerpt | null {
  if (s.waiting) {
    const text = cleanExcerpt(s.progress?.pendingQuestion?.items?.[0]?.text || s.progress?.lastMessageExcerpt);
    return text ? { kind: "waiting", text } : null;
  }
  if (s.failed) {
    const text = cleanExcerpt(s.progress?.lastFailure?.message || s.error || s.progress?.lastMessageExcerpt);
    return text ? { kind: "failed", text } : null;
  }
  if (s.active) {
    const text = cleanExcerpt(s.progress?.lastMessageExcerpt || s.progress?.currentTool);
    return text ? { kind: "running", text } : null;
  }
  const text = cleanExcerpt(s.progress?.lastMessageExcerpt || s.result);
  return text ? { kind: "latest", text } : null;
}
