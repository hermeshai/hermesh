import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { httpTransport, RpcClient } from "@hermesh/her-client";
import type { Methods, ModelsListResult } from "@hermesh/her-protocol";
import { errorText } from "./format";
import { i18n } from "./i18n";

// The page's one connection to her, through hermeshd's console routes (the events and rpc
// beside the page): every page asks on it, so a read on one page subscribes the connection to
// what changes (config/changed after config/read, and so on). When it drops, the pages wait
// and a new one is opened; what they read is read again on it. Also the model list, which
// several pages show, and the toast and the confirmation every page may show.

export type Toast = { id: number; text: string; bad?: boolean; undo?: () => void };

/** A confirmation: what will happen, and the button that does it. */
export type Ask = { title: string; message: string; button: string; run: () => Promise<unknown> };

export type Her = {
  /** The connection, or null while one is being opened. */
  rpc: RpcClient | null;
  /** Why the last connection ended before it answered anything. */
  lost: string | null;
  /** models/list: the providers, their models and what new assistants start with. */
  models: ModelsListResult | null;
  refreshModels: () => Promise<void>;
  call: <M extends keyof Methods & string>(method: M, params: Methods[M][0]) => Promise<Methods[M][1]>;
  toast: Toast | null;
  /** Says something at the bottom of the window for a few seconds; `undo` adds its button. */
  show: (text: string, undo?: () => void, bad?: boolean) => void;
  dismiss: () => void;
  ask: Ask | null;
  confirm: (a: Ask | null) => void;
};

const Context = createContext<Her | null>(null);

export function useHer(): Her {
  const her = useContext(Context);
  if (!her) throw new Error("useHer outside HerProvider");
  return her;
}

/** How long a toast stays: long enough to reach its Undo. */
export const TOAST_MS = 5200;

export function HerProvider({ children }: { children: ReactNode }) {
  const [rpc, setRpc] = useState<RpcClient | null>(null);
  const [lost, setLost] = useState<string | null>(null);
  const [models, setModels] = useState<ModelsListResult | null>(null);
  const [toast, setToast] = useState<Toast | null>(null);
  const [ask, confirm] = useState<Ask | null>(null);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let close = () => {};
    let tries = 0;
    const open = () => {
      const transport = httpTransport("./");
      close = transport.close;
      const client = new RpcClient(transport);
      let answered = false;
      client.request("models/list", {}).then(
        (m) => {
          answered = true;
          tries = 0;
          setLost(null);
          setModels(m);
        },
        () => {},
      );
      client.onClose(() => {
        if (stopped) return;
        if (!answered) setLost(i18n.t("console:conn.unreachable"));
        setRpc(null);
        timer = setTimeout(open, Math.min(1000 * 2 ** tries++, 10_000));
      });
      setRpc(client);
    };
    open();
    return () => {
      stopped = true;
      clearTimeout(timer);
      close();
    };
  }, []);

  const refreshModels = useCallback(async () => {
    if (rpc) setModels(await rpc.request("models/list", {}));
  }, [rpc]);
  useEffect(() => rpc?.on("models/changed", () => void refreshModels().catch(() => {})), [rpc, refreshModels]);

  const call = useCallback(
    <M extends keyof Methods & string>(method: M, params: Methods[M][0]): Promise<Methods[M][1]> =>
      rpc ? rpc.request(method, params) : Promise.reject(new Error(i18n.t("console:error.notConnected"))),
    [rpc],
  );

  const ids = useRef(0);
  const show = useCallback((text: string, undo?: () => void, bad?: boolean) => setToast({ id: ++ids.current, text, undo, bad }), []);
  const dismiss = useCallback(() => setToast(null), []);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast((now) => (now?.id === toast.id ? null : now)), TOAST_MS);
    return () => clearTimeout(timer);
  }, [toast]);

  const her = useMemo<Her>(
    () => ({ rpc, lost, models, refreshModels, call, toast, show, dismiss, ask, confirm }),
    [rpc, lost, models, refreshModels, call, toast, show, dismiss, ask],
  );
  return <Context.Provider value={her}>{children}</Context.Provider>;
}

/** The settings her's files say now, merged (config/read), read again when they change; null until read. */
export function useConfig(): { config: Record<string, unknown> | null; failed: string | null; read: () => Promise<void> } {
  const { rpc } = useHer();
  const [config, setConfig] = useState<Record<string, unknown> | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const read = useCallback(async () => {
    if (rpc) setConfig(((await rpc.request("config/read", {})).config ?? {}) as Record<string, unknown>);
  }, [rpc]);
  useEffect(() => {
    if (!rpc) return;
    read().then(
      () => setFailed(null),
      (e: unknown) => setFailed(errorText(e)),
    );
    return rpc.on("config/changed", () => void read().catch(() => {}));
  }, [rpc, read]);
  return { config, failed, read };
}

const clocks = new Map<number, { subscribe: (f: () => void) => () => void; now: () => Date }>();

/** The time, redrawn every `interval` ms: for "3 min ago". */
export function useNow(interval = 60_000): Date {
  let c = clocks.get(interval);
  if (!c) {
    const subs = new Set<() => void>();
    let now = new Date();
    let timer: ReturnType<typeof setInterval> | undefined;
    c = {
      now: () => now,
      subscribe(f) {
        subs.add(f);
        timer ??= setInterval(() => {
          now = new Date();
          subs.forEach((s) => s());
        }, interval);
        return () => {
          subs.delete(f);
          if (!subs.size) timer = void clearInterval(timer);
        };
      },
    };
    clocks.set(interval, c);
  }
  return useSyncExternalStore(c.subscribe, c.now);
}
