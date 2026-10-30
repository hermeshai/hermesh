import { useSyncExternalStore } from "react";

// Where the first run is after this computer became a trusted device:
// setting up this Mac, connecting an agent, waiting for its first task. A
// Mac that joined an owner only sets itself up: connecting an agent and the
// first task are the account's, done (or being done) on the device that set
// it up.
// Kept across restarts (like the Mac app's "firstRun" default); null once
// done, and then the main window shows.

export type Progress = { step: "prepare"; joined?: boolean } | { step: "agent" } | { step: "first"; agent: string } | null;

const key = "firstRun";
const listeners = new Set<() => void>();
let cached: { raw: string | null; value: Progress } | undefined;

function read(): Progress {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(key);
  } catch {
    // Without storage the first run simply ends with this window.
  }
  if (cached?.raw !== raw) {
    let value: Progress = null;
    try {
      value = raw ? (JSON.parse(raw) as Progress) : null;
    } catch {
      value = null;
    }
    cached = { raw, value };
  }
  return cached.value;
}

export function setProgress(p: Progress) {
  try {
    if (p) localStorage.setItem(key, JSON.stringify(p));
    else localStorage.removeItem(key);
  } catch {
    // See read.
  }
  listeners.forEach((f) => f());
}

export function useProgress(): Progress {
  return useSyncExternalStore(
    (f) => {
      listeners.add(f);
      return () => listeners.delete(f);
    },
    read,
  );
}
