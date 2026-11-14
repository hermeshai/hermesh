// Where the main window opens (docs/her-team.md §4.1, §14.1), as on the iPhone: Her when she runs on a machine;
// her first run while she runs nowhere and the owner has not said "Not Now" on this Mac; Work otherwise (no machine
// yet, a machine only, or Not Now). A window that quit on a session opens on it again, with its unfinished answer
// (decided by the owner on 2026-11-15): null, the window stays where it was.

type Env = { her?: { enabled: boolean } };

export function startPage(o: { machines: Env[]; machineOnly: boolean; notNow: boolean; onSession: boolean }): "her" | "overview" | null {
  if (o.onSession) return null;
  if (o.machineOnly) return "overview";
  if (o.machines.some((m) => m.her?.enabled)) return "her";
  return o.machines.length > 0 && !o.notNow ? "her" : "overview";
}

const key = "her-not-now";

/** The owner said "Not Now" to Her's first run on this Mac. */
export function notNow(): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

/** Says "Not Now", or forgets it (once a machine runs Her). */
export function setNotNow(on: boolean) {
  try {
    if (on) localStorage.setItem(key, "1");
    else localStorage.removeItem(key);
  } catch {
    // A convenience: without storage, the first run asks again.
  }
}
