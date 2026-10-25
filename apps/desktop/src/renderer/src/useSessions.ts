import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Unreachable } from "@hermesh/proto/local";
import type { Session, SessionCursor } from "@hermesh/proto/session";
import { mergeSessions, relistedCursor } from "@hermesh/core";
import { local } from "./api";

// The session list, as a chat app keeps its conversations: the newest page
// at once, older pages as the list scrolls to its end, and every change
// pushed by hermeshd (WatchSessions) as it happens, no polling. When the
// watch (re)starts or machines come and go, the first page is listed
// again, so nothing missed meanwhile stays missed.

/** Sessions per page: more than a sidebar shows. */
export const sessionPage = 50;

export type Sessions = {
  sessions: Session[];
  /** Machines whose sessions cannot be read now. */
  unreachable: Unreachable[];
  /** The first page arrived. */
  loaded: boolean;
  /** Where older sessions start; undefined when there are none. */
  next?: SessionCursor;
  loadingOlder: boolean;
};

const none: Sessions = { sessions: [], unreachable: [], loaded: false, loadingOlder: false };

/** Watches hermeshd's sessions while enabled (set up); loadOlder lists the next page. */
export function useSessions(enabled: boolean): Sessions & { loadOlder: () => void } {
  const [state, setState] = useState<Sessions>(none);
  const current = useRef(state);
  current.current = state;

  useEffect(() => {
    if (!enabled) return;
    const abort = new AbortController();
    const { signal } = abort;
    (async () => {
      while (!signal.aborted) {
        try {
          for await (const m of local.watchSessions({}, { signal })) {
            if (m.machinesChanged) {
              const r = await local.listSessions({ limit: sessionPage }, { signal });
              setState((s) => ({
                ...s,
                sessions: mergeSessions(s.sessions, r.sessions),
                unreachable: m.unreachable,
                next: s.loaded ? relistedCursor(s.next, r.next) : r.next,
                loaded: true,
              }));
            }
            if (m.sessions.length > 0 || m.deletedSessionIds.length > 0)
              setState((s) => ({ ...s, sessions: mergeSessions(s.sessions, m.sessions, m.deletedSessionIds) }));
          }
        } catch {
          // Watched again below: hermeshd restarted, or this watch fell behind.
        }
        await new Promise((r) => setTimeout(r, 1000));
      }
    })();
    return () => abort.abort();
  }, [enabled]);

  const loadOlder = useCallback(async () => {
    const before = current.current.next;
    if (!current.current.loaded || !before || current.current.loadingOlder) return;
    setState((s) => ({ ...s, loadingOlder: true }));
    try {
      const r = await local.listSessions({ limit: sessionPage, before });
      // A relist meanwhile may have moved where older pages go on.
      setState((s) => ({ ...s, sessions: mergeSessions(s.sessions, r.sessions), next: s.next === before ? r.next : s.next, loadingOlder: false }));
    } catch {
      await new Promise((r) => setTimeout(r, 1000)); // then the list may ask again
      setState((s) => ({ ...s, loadingOlder: false }));
    }
  }, []);

  return useMemo(() => ({ ...state, loadOlder }), [state, loadOlder]);
}
