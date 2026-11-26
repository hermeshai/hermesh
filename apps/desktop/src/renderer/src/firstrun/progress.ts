import { useSyncExternalStore } from "react";

// Where the first run is (docs/cold-start-prd.md §5): welcome, this Mac's agent providers, Her,
// signing in, done. Kept across restarts; null once done, and then the main window shows. A
// first run left at the welcome starts over there (nothing is kept for it).
//
// firstRunDone: this computer finished it once (or was set up before it existed), so it never
// shows again; erasing everything forgets that.

export type Step = "welcome" | "team" | "her" | "far" | "done";
export type Progress = { step: Step } | null;

const key = "firstRun";
const doneKey = "firstRunDone";
const steps: readonly Step[] = ["welcome", "team", "her", "far", "done"];
const listeners = new Set<() => void>();
let cached: { raw: string | null; done: string | null; value: Progress } | undefined;

function get(k: string): string | null {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}

function read(): Progress {
  const raw = get(key);
  const done = get(doneKey);
  if (cached?.raw !== raw || cached.done !== done) {
    let value: Progress = null;
    try {
      const p = raw ? (JSON.parse(raw) as { step?: string }) : null;
      // A step of the first run before this one (prepare, agent, first) is not taken up again.
      value = p && steps.includes(p.step as Step) ? { step: p.step as Step } : null;
    } catch {
      value = null;
    }
    cached = { raw, done, value };
  }
  return cached.value;
}

function notify() {
  listeners.forEach((f) => f());
}

export function setProgress(p: Progress) {
  try {
    if (p && p.step !== "welcome") localStorage.setItem(key, JSON.stringify(p));
    else localStorage.removeItem(key);
  } catch {
    // Without storage the first run simply ends with this window.
  }
  cached = undefined;
  notify();
}

/** Whether this computer finished the first run (or never needs it). */
export function firstRunDone(): boolean {
  return get(doneKey) === "1";
}

/** Marks the first run finished (on), or forgets it (erasing everything). */
export function setFirstRunDone(on: boolean) {
  try {
    if (on) localStorage.setItem(doneKey, "1");
    else localStorage.removeItem(doneKey);
  } catch {
    // See setProgress.
  }
  cached = undefined;
  notify();
}

const subscribe = (f: () => void) => {
  listeners.add(f);
  return () => void listeners.delete(f);
};

/** The step kept; null at the welcome, or once done. */
export function useProgress(): Progress {
  return useSyncExternalStore(subscribe, read);
}

/** Whether the first run's done mark is set, as it changes. */
export function useFirstRunDone(): boolean {
  return useSyncExternalStore(subscribe, firstRunDone);
}
