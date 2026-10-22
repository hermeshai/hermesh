import { useEffect, useRef, useState } from "react";
import type { SessionEvent } from "@hermesh/proto/session";
import { local } from "../api";

/**
 * A session's events, followed while the page shows it: the stream picks up
 * after the last event it got when it breaks (as the Mac app did). last is
 * the session's last event as the list knows it: the events up to it show
 * as soon as they are all in.
 */
export function useSessionEvents(sessionId: string, last: bigint): SessionEvent[] {
  const [got, setGot] = useState<{ id: string; events: SessionEvent[] }>({ id: sessionId, events: [] });
  const known = useRef(last);
  known.current = last;
  useEffect(() => {
    const stop = new AbortController();
    // Events arrive one by one; they are added in small batches, but what
    // was there when the page opened at once.
    let caughtUp = false;
    let batch: SessionEvent[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      timer = undefined;
      const add = batch;
      batch = [];
      setGot((g) => (g.id === sessionId ? { id: sessionId, events: g.events.concat(add) } : { id: sessionId, events: add }));
    };
    (async () => {
      let after = 0n;
      while (!stop.signal.aborted) {
        try {
          for await (const e of local.streamSessionEvents({ sessionId, afterSeq: after, follow: true }, { signal: stop.signal })) {
            after = e.seq;
            batch.push(e);
            if (!caughtUp && e.seq >= known.current) {
              caughtUp = true;
              clearTimeout(timer);
              flush();
            } else timer ??= setTimeout(flush, 30);
          }
        } catch {
          // Broken or cancelled; picked up again below unless the page moved on.
        }
        await new Promise((r) => setTimeout(r, 3000));
      }
    })();
    return () => {
      stop.abort();
      clearTimeout(timer);
    };
  }, [sessionId]);
  return got.id === sessionId ? got.events : [];
}
