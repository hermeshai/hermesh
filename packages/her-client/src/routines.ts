import type { RoutineRunInfo, RoutineSummary, SessionInfo, TranscriptItem } from "@hermesh/her-protocol";
import { fromSnapshot, type SessionView } from "./session.ts";

// What the routines panel works out (docs/desktop/bot-mode-design.md §6.21),
// from the bot's row and the service's records alone, so it can be checked
// without a window.

/** The dot on the clock at the top of a bot's page: something failed, else something runs. */
export function routineDot(routines: RoutineSummary[] | null | undefined): "failed" | "running" | null {
  if (routines?.some((r) => r.last_run?.outcome === "failed")) return "failed";
  if (routines?.some((r) => r.running)) return "running";
  return null;
}

/** The list's groups: what runs on top, then what is paused, then one-offs that are over. */
export function routineGroups(routines: RoutineSummary[]): { active: RoutineSummary[]; paused: RoutineSummary[]; finished: RoutineSummary[] } {
  return {
    active: routines.filter((r) => r.state === "active"),
    paused: routines.filter((r) => r.state === "paused"),
    finished: routines.filter((r) => r.state === "finished"),
  };
}

/** A row's second line, the first that applies. */
export type RoutineLine =
  | { kind: "running"; since: Date | null }
  | { kind: "failed"; at: Date }
  | { kind: "next"; when: string; at: Date }
  | { kind: "when"; when: string };

export function routineLine(r: RoutineSummary): RoutineLine {
  if (r.running) return { kind: "running", since: r.last_run ? new Date(r.last_run.started) : null };
  if (r.last_run?.outcome === "failed") return { kind: "failed", at: new Date(r.last_run.started) };
  if (r.next_run) return { kind: "next", when: r.when, at: new Date(r.next_run) };
  return { kind: "when", when: r.when };
}

/** One line of a routine's runs: a run, or several quiet ones in a row told as one. */
export type RunRow = { kind: "run"; run: RoutineRunInfo } | { kind: "quiet"; key: string; runs: RoutineRunInfo[] };

/** Fewer than this many quiet runs in a row are shown one by one. */
const FOLD_FROM = 3;

/** The runs as rows, newest first: quiet ones in a row fold together, unless `open` has the row's key. */
export function runRows(runs: RoutineRunInfo[], open: ReadonlySet<string> = new Set()): RunRow[] {
  const rows: RunRow[] = [];
  let i = 0;
  while (i < runs.length) {
    let end = i;
    while (end < runs.length && runs[end].outcome === "quiet") end++;
    if (end - i >= FOLD_FROM && !open.has(runs[i].id)) {
      rows.push({ kind: "quiet", key: runs[i].id, runs: runs.slice(i, end) });
      i = end;
    } else if (end > i) {
      for (const run of runs.slice(i, end)) rows.push({ kind: "run", run });
      i = end;
    } else {
      rows.push({ kind: "run", run: runs[i] });
      i++;
    }
  }
  return rows;
}

/** When a run is told by: when it started, or when it was due if it never did. */
export function runTime(run: RoutineRunInfo): Date {
  return new Date(run.started ?? run.scheduled);
}

/** How long it ran, in ms, one still running up to `now`; null when it never started, or nothing says how long. */
export function runTook(run: RoutineRunInfo, now?: Date): number | null {
  if (!run.started) return null;
  const end = run.ended ? Date.parse(run.ended) : run.outcome ? null : (now?.getTime() ?? null);
  return end === null ? null : Math.max(0, end - Date.parse(run.started));
}

/** How late it started, in ms; null when it was on time. */
export function runLate(run: RoutineRunInfo): number | null {
  if (!run.late || !run.started) return null;
  return Date.parse(run.started) - Date.parse(run.scheduled);
}

/** A finished run's steps as a session's, to show them the way a sub-agent's are. */
export function viewOfRun(info: SessionInfo, run: string, items: TranscriptItem[]): SessionView {
  return fromSnapshot({
    session: { ...info, session_id: run, title: null, archived: false },
    last_seq: 0,
    transcript: { items },
    usage: { per_model: {} },
  });
}
