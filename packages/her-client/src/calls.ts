import { PROTOCOL_VERSION, type InitializeResult } from "@hermesh/her-protocol";
import type { Transport } from "./rpc.ts";

// her's connections as hermeshd forwards them (proto/hermesh/her/v1/her.proto): a
// connection is a stream that opens it, says its id and then carries her's notifications,
// and each request is a call of its own on that connection, answered once. This turns them
// into the NDJSON pipe RpcClient reads, so a page talks to her through hermeshd as it would
// to her directly.

/**
 * What a connection's stream carries: its id first, with what each setup request returned,
 * then her's notifications.
 */
export type ConnectionEvent = { opened: { connectionId: string; responses?: CallResult[] } } | { notification: { method: string; params: unknown } };

/** A JSON-RPC error of her's. */
export type CallError = { code: number; message: string; data?: unknown };

/** How a call came back: her's result, or her's error. */
export type CallResult = { result: unknown } | { error: CallError };

/** A request sent as a connection opens (bots/subscribe, bots/open), before any notification of it. */
export type SetupRequest = { method: string; params: unknown };

/** hermeshd's side of a connection. */
export interface CallAdapter {
  /** Opens a connection, sending `setup` on it first. The stream ends when the connection does; `signal` ends it. */
  open(signal: AbortSignal, setup: SetupRequest[]): AsyncIterable<ConnectionEvent>;
  /** One request on the connection `connectionId` names. A throw: it or its answer was lost. */
  call(connectionId: string, method: string, params: unknown): Promise<CallResult>;
}

/**
 * What `initialize` answers. hermeshd initialized the connection itself and does not pass
 * her's answer on, so this is her's own (her-server's native.rs) without its version. A
 * method hermeshd does not forward still fails when it is called.
 */
const initialized: InitializeResult = {
  protocol_version: PROTOCOL_VERSION,
  server_info: { name: "her" },
  capabilities: {
    max_frame_bytes: 128 << 20,
    session_delete: true,
    resource_read: true,
    event_filter: true,
    sessions_subscribe: true,
    session_metadata: true,
    transcript_task_end: true,
    auth: true,
    agents: true,
    model_connections: true,
    model_discovery: true,
    customize: true,
    embedded_browser: true,
    input_queue: true,
    message_resend: true,
    workbooks: true,
    bots: true,
    routines: true,
  },
};

/**
 * A transport over one connection: hand it to an RpcClient at once. `initialize` is answered
 * here, every other request goes as a call on the connection once the stream says its id,
 * and its answer comes back under the request's own id. Notifications the page sends have no
 * way to her and are dropped. When the stream ends (or `close` ends it), onClose fires, and
 * the RpcClient's owner opens a new transport: it is a new connection, so whatever the old
 * one subscribed to or opened is asked for again.
 *
 * A call's answer and the stream's notifications travel apart, so an event her sent after
 * answering (a session's events after the snapshot `bots/open` returned) can arrive before
 * the answer does. What must come first goes in `setup`: its answers reach `onOpened` with
 * the connection's id, before any notification is passed on.
 */
export function openCallTransport(
  adapter: CallAdapter,
  o: { setup?: SetupRequest[]; onOpened?: (responses: CallResult[]) => void } = {},
): Transport & { close(): void } {
  const texts = new Set<(text: string) => void>();
  const closes = new Set<() => void>();
  const abort = new AbortController();
  let closed = false;
  let opened!: (id: string | null) => void;
  /** The connection's id, once the stream says it; null when it ended first. */
  const connection = new Promise<string | null>((resolve) => (opened = resolve));

  const deliver = (msg: object) => {
    if (closed) return;
    const line = JSON.stringify({ jsonrpc: "2.0", ...msg }) + "\n";
    for (const f of texts) f(line);
  };

  const end = () => {
    if (closed) return;
    closed = true;
    opened(null);
    abort.abort();
    for (const f of closes) f();
    closes.clear();
  };

  void (async () => {
    try {
      for await (const e of adapter.open(abort.signal, o.setup ?? [])) {
        if (closed) break;
        if ("opened" in e) {
          o.onOpened?.(e.opened.responses ?? []);
          opened(e.opened.connectionId);
        } else deliver({ method: e.notification.method, params: e.notification.params });
      }
    } catch {
      // A stream that fails has ended all the same.
    }
    end();
  })();

  const call = async (id: number | string, method: string, params: unknown) => {
    const connectionId = await connection;
    if (connectionId === null || closed) return;
    let r: CallResult;
    try {
      r = await adapter.call(connectionId, method, params);
    } catch (e) {
      r = { error: { code: -32603, message: e instanceof Error ? e.message : String(e), data: { kind: "internal_error" } } };
    }
    deliver("error" in r ? { id, error: r.error } : { id, result: r.result });
  };

  return {
    send(text) {
      for (const line of text.split("\n")) {
        if (line.trim() === "") continue;
        const msg = JSON.parse(line) as { id?: number | string | null; method?: unknown; params?: unknown };
        // Only requests go on: a notification has no way to her, and as her sends no
        // requests this way, there are no answers to pass on either.
        if (typeof msg.method !== "string" || msg.id == null) continue;
        const id = msg.id;
        if (msg.method === "initialize") queueMicrotask(() => deliver({ id, result: initialized }));
        else void call(id, msg.method, msg.params);
      }
    },
    onText(cb) {
      texts.add(cb);
      return () => void texts.delete(cb);
    },
    onClose(cb) {
      if (closed) {
        queueMicrotask(cb);
        return () => {};
      }
      closes.add(cb);
      return () => void closes.delete(cb);
    },
    close: end,
  };
}
