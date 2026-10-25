import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { applyWatch, emptyWorld, mergeSessions, type World } from "@hermesh/core";
import { SessionView } from "@hermesh/proto/session";
import { local } from "./api";

// What the window knows of the owner's world, as hermeshd pushes it
// (LocalService.Watch, docs/state-sync.md): everything at once, then each
// change as it happens, with no polling. The session list keeps the newest
// page and every change, and older pages as the list scrolls to its end,
// as a chat app keeps its conversations.

/** Sessions per page: more than a sidebar shows. */
export const sessionPage = 50;

export type WorldState = World & {
  /** hermeshd cannot be reached (the watch failed; it is watched again). */
  down: boolean;
  loadingOlder: boolean;
};

export function useWorld(): WorldState & { loadOlder: () => void; refreshStatus: () => Promise<void> } {
  const [state, setState] = useState<WorldState>({ ...emptyWorld, down: false, loadingOlder: false });
  const current = useRef(state);
  current.current = state;

  useEffect(() => {
    const abort = new AbortController();
    const { signal } = abort;
    (async () => {
      while (!signal.aborted) {
        try {
          for await (const m of local.watch({}, { signal })) {
            setState((s) => {
              const w = applyWatch(s, m);
              return w === s && !s.down ? s : { ...s, ...w, down: false };
            });
          }
        } catch {
          // Watched again below: hermeshd restarted, or this watch fell behind.
          if (!signal.aborted) setState((s) => (s.down ? s : { ...s, down: true }));
        }
        await new Promise((r) => setTimeout(r, 1000));
      }
    })();
    return () => abort.abort();
  }, []);

  const loadOlder = useCallback(async () => {
    const before = current.current.next;
    if (!current.current.loaded || !before || current.current.loadingOlder) return;
    setState((s) => ({ ...s, loadingOlder: true }));
    try {
      const r = await local.listSessions({ limit: sessionPage, before, view: SessionView.BASIC });
      // A reset meanwhile may have moved where older pages go on.
      setState((s) => ({ ...s, sessions: mergeSessions(s.sessions, r.sessions), next: s.next === before ? r.next : s.next, loadingOlder: false }));
    } catch {
      await new Promise((r) => setTimeout(r, 1000)); // then the list may ask again
      setState((s) => ({ ...s, loadingOlder: false }));
    }
  }, []);

  // The status at once, for a step that waits on it (signing in); Watch
  // brings it too, a moment later.
  const refreshStatus = useCallback(async () => {
    try {
      const status = await local.getStatus({});
      setState((s) => ({ ...s, status, down: false }));
    } catch {
      setState((s) => (s.down ? s : { ...s, down: true }));
    }
  }, []);

  return useMemo(() => ({ ...state, loadOlder, refreshStatus }), [state, loadOlder, refreshStatus]);
}
