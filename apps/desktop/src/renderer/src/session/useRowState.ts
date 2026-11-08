import { useCallback, useState } from "react";
import { useContentSession } from "./scope";

// What a row keeps while it is not drawn (docs/desktop-performance.md §7): a long conversation
// draws only the rows near what is in sight, so a group of calls opened, scrolled far away and
// come back to is still open. Kept per session, for the sessions opened last.

const bySession = new Map<string, Map<string, unknown>>();
/** Sessions whose rows' state is kept: as many as the conversations kept to go back to, and some. */
const KEEP = 20;

function stateOf(session: string): Map<string, unknown> {
  let rows = bySession.get(session);
  if (rows) {
    bySession.delete(session);
    bySession.set(session, rows);
    return rows;
  }
  rows = new Map();
  bySession.set(session, rows);
  if (bySession.size > KEEP) bySession.delete(bySession.keys().next().value!);
  return rows;
}

/**
 * useState for something a row shows, kept under `id` (the row's key or its call's id) in the session the row
 * belongs to. Without an id it is plain state: for what is always drawn, the control area at the bottom.
 */
export function useRowState<T>(id: string | undefined, initial: T): [T, (next: T | ((was: T) => T)) => void] {
  const session = useContentSession();
  const [value, setValue] = useState<T>(() => (id !== undefined && bySession.get(session)?.has(id) ? (bySession.get(session)!.get(id) as T) : initial));
  const set = useCallback(
    (next: T | ((was: T) => T)) =>
      setValue((was) => {
        const now = typeof next === "function" ? (next as (was: T) => T)(was) : next;
        if (id !== undefined) stateOf(session).set(id, now);
        return now;
      }),
    [id, session],
  );
  return [value, set];
}
