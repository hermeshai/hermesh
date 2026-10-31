import { useEffect, useRef, useState } from "react";
import { EventView, type SessionEvent } from "@hermesh/proto/session";
import { local } from "../api";

/** A session's stored events, and the reply the agent is still writing ("" when none). */
export type Conversation = { events: SessionEvent[]; draft: string };

/**
 * The conversations opened last, the newest last: going back to one shows
 * it at once (also while its machine cannot be reached), and its stream
 * picks up after what it holds.
 */
const kept = new Map<string, SessionEvent[]>();
const keepAtMost = 10;

function keep(id: string, events: SessionEvent[]) {
  kept.delete(id);
  kept.set(id, events);
  if (kept.size > keepAtMost) kept.delete(kept.keys().next().value!);
}

/**
 * A session's events (BASIC: a long tool output as its preview, read whole
 * when its row opens), followed while the page shows it: the stream picks
 * up after the last event it got when it breaks (as the Mac app did). last
 * is the session's last event as the list knows it: the events up to it
 * show as soon as they are all in. The draft grows with the agent's reply
 * until a stored event ends it (docs/reply-streaming.md).
 */
export function useSessionEvents(sessionId: string, last: bigint): Conversation {
  const [got, setGot] = useState<Conversation & { id: string }>(() => ({ id: sessionId, events: kept.get(sessionId) ?? [], draft: "" }));
  const known = useRef(last);
  known.current = last;
  useEffect(() => {
    const stop = new AbortController();
    // Events arrive one by one; they are added in small batches, but what
    // was there when the page opened at once.
    const held = kept.get(sessionId) ?? [];
    let caughtUp = held.length > 0;
    if (caughtUp) setGot((g) => (g.id === sessionId ? g : { id: sessionId, events: held, draft: "" }));
    let batch: SessionEvent[] = [];
    let draft = "";
    let timer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      timer = undefined;
      const add = batch;
      batch = [];
      setGot((g) => {
        const events = g.id === sessionId ? (add.length ? g.events.concat(add) : g.events) : held.concat(add);
        if (add.length) keep(sessionId, events);
        return { id: sessionId, events, draft };
      });
    };
    (async () => {
      let after = held.at(-1)?.seq ?? 0n;
      while (!stop.signal.aborted) {
        try {
          for await (const e of local.streamSessionEvents({ sessionId, afterSeq: after, follow: true, partial: true, view: EventView.BASIC }, { signal: stop.signal })) {
            if (e.kind.case === "agentDraft") {
              draft = e.kind.value.whole ? e.kind.value.text : draft + e.kind.value.text;
              timer ??= setTimeout(flush, 30);
              continue;
            }
            after = e.seq;
            batch.push(e);
            draft = "";
            if (!caughtUp && e.seq >= known.current) {
              caughtUp = true;
              clearTimeout(timer);
              flush();
              // Before then, not in 30 ms batches, which a slow stream (Windows)
              // shows as pieces; a second on, what came shows anyway.
            } else timer ??= setTimeout(flush, caughtUp ? 30 : 1000);
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
  return got.id === sessionId ? got : { events: kept.get(sessionId) ?? [], draft: "" };
}
