import { useCallback, useEffect, useRef, useState } from "react";
import { applyEvents, fromSnapshot, mergeBots, openCallTransport, RpcClient, RpcError, sortBots, withHistory, type SessionView } from "@hermesh/her-client";
import type { BotSummary, Event, EventClass, ModelsListResult, SessionNewResult } from "@hermesh/her-protocol";
import type { HerWaiting, Machine } from "@hermesh/proto/local";
import { local } from "../api";
import { errorText } from "../store";
import { herAdapter } from "./adapter";
import { reachable } from "./environments";
import { keptFrom, type Kept } from "./kept";

// Her's connections while the Her page shows an environment (docs/her-server.md
// §5.1): one for the environment, which follows its assistants (bots/subscribe)
// and models; one for the conversation on screen, opened with bots/open in its
// setup so the snapshot comes ahead of its events. Nothing is open while the
// page is not shown: the sidebar's count and the notifications come from Watch.

export const noModels: ModelsListResult = { models: [], providers: [] };

/** After a connection ended: again in 1 s, then up to every 30 s while it keeps failing. */
const retryIn = (failures: number) => Math.min(30_000, 1000 * 2 ** Math.max(0, failures - 1));

export type Environment = {
  /** Connected: calls go on it. */
  rpc: RpcClient | null;
  bots: BotSummary[];
  botsLoaded: boolean;
  models: ModelsListResult;
  /** Why the last connection failed, while it is down. */
  error: string | null;
};

/** What each environment showed last: it shows at once when the page comes back to it, and while it is offline. */
const keptEnvs = new Map<string, { bots: BotSummary[]; models: ModelsListResult }>();

function fromKept(id: string): Environment {
  const k = keptEnvs.get(id);
  return { rpc: null, bots: k?.bots ?? [], botsLoaded: !!k, models: k?.models ?? noModels, error: null };
}

/**
 * The environment's connection: its assistants and models as her pushes them.
 * It opens while the machine can be reached and her runs there (Watch says
 * when); one that ends opens again after a while. Offline, what was shown last
 * stays.
 */
export function useEnvironment(machine: Machine): Environment {
  const id = machine.machineId;
  const live = reachable(machine);
  const running = !!machine.her?.running;
  const [state, setState] = useState<Environment & { id: string }>(() => ({ id, ...fromKept(id) }));
  const [attempt, setAttempt] = useState(0);
  const failures = useRef(0);
  useEffect(() => {
    if (!live || !running) return;
    let stop = false;
    let rpc: RpcClient | null = null;
    const keep = (patch: Partial<Environment>) =>
      setState((s) => {
        const next = { ...(s.id === id ? s : { id, ...fromKept(id) }), ...patch };
        keptEnvs.set(id, { bots: next.bots, models: next.models });
        return next;
      });
    const transport = openCallTransport(herAdapter(local, id), {
      setup: [
        { method: "bots/subscribe", params: {} },
        { method: "models/list", params: {} },
      ],
      onOpened: ([bots, models]) => {
        if (!bots || "error" in bots) {
          keep({ error: bots && "error" in bots ? bots.error.message : null });
          return transport.close();
        }
        failures.current = 0;
        keep({
          rpc,
          bots: sortBots((bots.result as { bots: BotSummary[] }).bots),
          botsLoaded: true,
          models: models && "result" in models ? (models.result as ModelsListResult) : noModels,
          error: null,
        });
      },
    });
    rpc = new RpcClient(transport);
    const client = rpc;
    client.on("bots/changed", (c) => setState((s) => (s.id === id ? withKept(id, { ...s, bots: sortBots(mergeBots(s.bots, c.bots, c.deleted ?? [])) }) : s)));
    client.on("models/changed", () => {
      client.request("models/list", {}).then((models) => !stop && setState((s) => (s.id === id ? withKept(id, { ...s, models }) : s)), () => {});
    });
    client.onClose(() => {
      if (stop) return;
      setState((s) => (s.id === id ? { ...s, rpc: null } : s));
      failures.current += 1;
      setTimeout(() => !stop && setAttempt((a) => a + 1), retryIn(failures.current));
    });
    return () => {
      stop = true;
      transport.close();
      setState((s) => (s.id === id && s.rpc === client ? { ...s, rpc: null } : s));
    };
  }, [id, live, running, attempt]);
  return state.id === id ? state : { ...fromKept(id) };
}

function withKept<T extends Environment>(id: string, next: T): T {
  keptEnvs.set(id, { bots: next.bots, models: next.models });
  return next;
}

/** What a bot's page never shows: the words as they are written. */
const notStreamed = { exclude: ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"] as EventClass[] };

/** The conversations opened last, the newest last: going back to one shows it at once, and while its machine is offline. */
const keptViews = new Map<string, { session: string; view: SessionView }>();

function keepView(key: string, session: string, view: SessionView) {
  keptViews.delete(key);
  keptViews.set(key, { session, view });
  if (keptViews.size > 10) keptViews.delete(keptViews.keys().next().value!);
}

export type Conversation = {
  view: SessionView | null;
  session: string | null;
  /** The conversation's connection, while it is open. */
  rpc: RpcClient | null;
  /** her can't start the conversation: no model works in the environment. */
  noModel: boolean;
  loadEarlier: () => Promise<void>;
};

/**
 * An assistant's conversation while its page shows it: opened with `bots/open` in the connection's setup, then
 * every event after it, applied once a frame. Earlier history comes a page
 * at a time when asked for. A cleared conversation is a new session: the page
 * opens that one.
 */
export function useConversation(machine: Machine, bot: BotSummary | undefined, show: (text: string) => void): Conversation {
  const id = machine.machineId;
  const botId = bot?.id ?? "";
  const key = `${id}\0${botId}`;
  const live = reachable(machine);
  const running = !!machine.her?.running;
  const modelReady = !!machine.her?.modelReady;
  const [got, setGot] = useState<{ key: string; session: string | null; view: SessionView | null; rpc: RpcClient | null; noModel: boolean }>(() => ({
    key,
    session: keptViews.get(key)?.session ?? null,
    view: keptViews.get(key)?.view ?? null,
    rpc: null,
    noModel: false,
  }));
  const earlier = useRef<() => Promise<void>>(async () => {});
  // A conversation cleared, or detached by her, is opened again.
  const [generation, setGeneration] = useState(0);
  const attached = got.key === key ? got.session : null;
  const sessionNow = bot?.session_id ?? null;
  useEffect(() => {
    if (sessionNow && attached && sessionNow !== attached) setGeneration((g) => g + 1);
  }, [sessionNow]);
  const failures = useRef(0);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!botId || !live || !running) return;
    let stop = false;
    let session: string | null = null;
    let view: SessionView | null = null;
    let pending: Event[] = [];
    let frame = 0;
    let rpc: RpcClient | null = null;
    const publish = (patch: Partial<typeof got> = {}) => {
      if (view && session) keepView(key, session, view);
      setGot((g) => ({ ...(g.key === key ? g : { key, session: null, view: null, rpc: null, noModel: false }), session, view, rpc, ...patch }));
    };
    const flush = () => {
      frame = 0;
      if (!view || !session || pending.length === 0) return;
      view = applyEvents(view, pending);
      pending = [];
      publish();
    };
    const transport = openCallTransport(herAdapter(local, id), {
      setup: [{ method: "bots/open", params: { bot_id: botId, events: notStreamed } }],
      onOpened: ([r]) => {
        if (!r || "error" in r) {
          const kind = r && "error" in r ? new RpcError(r.error as never).kind : "";
          // Deleted meanwhile: the list hears of it too. Without a model, the page says so.
          if (kind === "no_model") publish({ noModel: true });
          else if (r && "error" in r && kind !== "bot_not_found") show(r.error.message);
          return;
        }
        failures.current = 0;
        const opened = r.result as SessionNewResult;
        session = opened.session_id;
        // Events up to the snapshot are in it already: applyEvents skips them by seq.
        view = applyEvents(fromSnapshot(opened.snapshot), pending);
        pending = [];
        publish({ noModel: false });
      },
    });
    rpc = new RpcClient(transport);
    const client = rpc;
    client.on("session/event", (p) => {
      if (p.session_id !== session) return;
      pending.push(p.event);
      if (!frame) frame = requestAnimationFrame(flush);
    });
    client.on("session/detached", (p) => {
      if (p.session_id === session && !stop) setGeneration((g) => g + 1);
    });
    let paging: Promise<void> | null = null;
    earlier.current = () => {
      const cursor = view?.before;
      if (!cursor || !session) return Promise.resolve();
      paging ??= client
        .request("session/history", { session_id: session, before: cursor })
        .then((page) => {
          if (stop || !view || view.before !== cursor) return;
          view = withHistory(view, page);
          publish();
        })
        .catch((e) => {
          if (!stop) show(errorText(e));
        })
        .finally(() => (paging = null));
      return paging;
    };
    client.onClose(() => {
      if (stop) return;
      rpc = null;
      setGot((g) => (g.key === key ? { ...g, rpc: null } : g));
      failures.current += 1;
      setTimeout(() => !stop && setAttempt((a) => a + 1), retryIn(failures.current));
    });
    return () => {
      stop = true;
      earlier.current = async () => {};
      cancelAnimationFrame(frame);
      transport.close();
      setGot((g) => (g.key === key && g.rpc === client ? { ...g, rpc: null } : g));
    };
  }, [id, botId, live, running, modelReady, generation, attempt]);

  const loadEarlier = useCallback(() => earlier.current(), []);
  if (got.key === key) return { view: got.view, session: got.session, rpc: got.rpc, noModel: got.noModel, loadEarlier };
  const kept = keptViews.get(key);
  return { view: kept?.view ?? null, session: kept?.session ?? null, rpc: null, noModel: false, loadEarlier };
}

/**
 * What hermeshd keeps of the environment's Her (kept.ts), asked for when the
 * page shows the environment: what the page shows while the environment has
 * nothing of its own yet, such as after the app restarted.
 */
export function useKept(machineId: string): Kept | null {
  const [got, setGot] = useState<{ id: string; kept: Kept | null }>({ id: machineId, kept: null });
  useEffect(() => {
    let stop = false;
    local.getHerConversation({ machineId }).then(
      (r) => !stop && setGot({ id: machineId, kept: keptFrom(r) }),
      () => {},
    );
    return () => {
      stop = true;
    };
  }, [machineId]);
  return got.id === machineId ? got.kept : null;
}

/**
 * The messages this device keeps for an environment's assistants until it is
 * back (ListHerWaiting): asked for when the page shows the environment, when
 * the machine comes and goes, and when `refresh` says something changed.
 */
export function useWaiting(machine: Machine): { list: HerWaiting[]; refresh: () => void } {
  const id = machine.machineId;
  const live = reachable(machine);
  const [got, setGot] = useState<{ id: string; list: HerWaiting[] }>({ id, list: [] });
  const [asked, setAsked] = useState(0);
  useEffect(() => {
    let stop = false;
    local.listHerWaiting({ machineId: id }).then(
      (r) => !stop && setGot({ id, list: r.waiting }),
      () => {},
    );
    return () => {
      stop = true;
    };
  }, [id, live, asked]);
  const refresh = useCallback(() => setAsked((a) => a + 1), []);
  return { list: got.id === id ? got.list : [], refresh };
}
