import { useSyncExternalStore } from "react";
import { sessions } from "../api";
import { errorText } from "../store";

export type InlineRunStatus = "running" | "done" | "stopped" | "error";

export type InlineRunState = {
  status: InlineRunStatus;
  command: string;
  output: string;
  exitCode: number;
  startedAt: number;
  durationMs: number;
  timedOut: boolean;
  truncated: boolean;
  folded: boolean;
  sentToChat: boolean;
  errorMessage?: string;
};

const MAX_INLINE_RUNS = 64;
const runs = new Map<string, InlineRunState>();
const controllers = new Map<string, AbortController>();
const listeners = new Set<() => void>();

const keyOf = (sessionId: string, blockText: string) => `${sessionId}\0${blockText}`;

function emit() {
  for (const l of listeners) l();
}

function setRun(key: string, next: InlineRunState) {
  if (!runs.has(key) && runs.size >= MAX_INLINE_RUNS) {
    for (const [k, v] of runs) {
      if (v.status !== "running") {
        runs.delete(k);
        break;
      }
    }
  }
  runs.set(key, next);
  emit();
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

export function useInlineRun(sessionId: string, blockText: string): InlineRunState | undefined {
  const key = sessionId ? keyOf(sessionId, blockText) : "";
  return useSyncExternalStore(subscribe, () => (key ? runs.get(key) : undefined));
}

export function startInlineRun(sessionId: string, blockText: string, command: string): void {
  if (!sessionId || !command.trim()) return;
  const key = keyOf(sessionId, blockText);
  controllers.get(key)?.abort();

  const ctl = new AbortController();
  controllers.set(key, ctl);
  const startedAt = Date.now();

  setRun(key, {
    status: "running",
    command,
    output: "",
    exitCode: 0,
    startedAt,
    durationMs: 0,
    timedOut: false,
    truncated: false,
    folded: false,
    sentToChat: false,
  });

  void (async () => {
    let acc = "";
    try {
      for await (const ev of sessions.runCommand({ sessionId, command }, { signal: ctl.signal })) {
        if (controllers.get(key) !== ctl) return;
        if (ev.outputDelta) {
          acc += ev.outputDelta;
        }
        const cur = runs.get(key);
        const folded = cur?.folded ?? false;
        if (ev.done) {
          controllers.delete(key);
          setRun(key, {
            status: "done",
            command,
            output: acc,
            exitCode: ev.exitCode,
            startedAt,
            durationMs: Number(ev.durationMs) || Math.max(1, Date.now() - startedAt),
            timedOut: ev.timedOut,
            truncated: ev.truncated,
            folded,
            sentToChat: false,
          });
          return;
        }
        setRun(key, {
          status: "running",
          command,
          output: acc,
          exitCode: 0,
          startedAt,
          durationMs: Date.now() - startedAt,
          timedOut: false,
          truncated: ev.truncated,
          folded,
          sentToChat: false,
        });
      }
      if (controllers.get(key) === ctl) {
        controllers.delete(key);
        const cur = runs.get(key);
        if (cur && cur.status === "running") {
          setRun(key, { ...cur, status: "done", durationMs: Date.now() - startedAt });
        }
      }
    } catch (e) {
      if (controllers.get(key) !== ctl) return;
      controllers.delete(key);
      const cur = runs.get(key);
      const folded = cur?.folded ?? false;
      if (ctl.signal.aborted) {
        setRun(key, {
          status: "stopped",
          command,
          output: acc,
          exitCode: -1,
          startedAt,
          durationMs: Math.max(1, Date.now() - startedAt),
          timedOut: false,
          truncated: false,
          folded,
          sentToChat: false,
        });
      } else {
        setRun(key, {
          status: "error",
          command,
          output: acc,
          exitCode: -1,
          startedAt,
          durationMs: Math.max(1, Date.now() - startedAt),
          timedOut: false,
          truncated: false,
          folded,
          sentToChat: false,
          errorMessage: errorText(e),
        });
      }
    }
  })();
}

export function stopInlineRun(sessionId: string, blockText: string): void {
  const key = keyOf(sessionId, blockText);
  const ctl = controllers.get(key);
  if (!ctl) return;
  controllers.delete(key);
  ctl.abort();
  const cur = runs.get(key);
  if (cur && cur.status === "running") {
    setRun(key, {
      ...cur,
      status: "stopped",
      exitCode: -1,
      durationMs: Math.max(1, Date.now() - cur.startedAt),
    });
  }
}

export function toggleInlineRunFold(sessionId: string, blockText: string): void {
  const key = keyOf(sessionId, blockText);
  const cur = runs.get(key);
  if (!cur) return;
  setRun(key, { ...cur, folded: !cur.folded });
}

export function markInlineRunSent(sessionId: string, blockText: string): void {
  const key = keyOf(sessionId, blockText);
  const cur = runs.get(key);
  if (!cur) return;
  setRun(key, { ...cur, sentToChat: true });
}
