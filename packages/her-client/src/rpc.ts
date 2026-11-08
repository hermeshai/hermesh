import type { Methods, Notifications, RpcErrorObject, ServerRequests } from "@hermesh/her-protocol";

// A JSON-RPC 2.0 connection to her (protocol-design §6.1–§6.2): NDJSON text
// in and out, requests matched to their responses by id, notifications
// handed to whoever listens for their method. Each message is parsed once,
// here; what the transport carries is the text exactly as her wrote it.

/** A byte pipe to her: the Electron bridge, a socket, or the mock server. */
export interface Transport {
  /** Sends complete NDJSON lines, each ending in "\n". */
  send(text: string): void;
  /** Received text: whole lines, one or several. Returns a function that stops listening. */
  onText(cb: (text: string) => void): () => void;
  /** The connection to her ended. */
  onClose(cb: () => void): () => void;
}

/** An error response, with the kind every her error carries (protocol-design §6.7). */
export class RpcError extends Error {
  readonly code: number;
  readonly kind: string;
  readonly data: unknown;

  constructor(e: RpcErrorObject) {
    super(e.message);
    this.code = e.code;
    this.kind = typeof e.data?.kind === "string" ? e.data.kind : "internal_error";
    this.data = e.data;
  }
}

/** The connection ended before the response came. */
export const disconnected: RpcErrorObject = { code: -32099, message: "disconnected from her", data: { kind: "disconnected" } };

type Pending = { resolve: (v: unknown) => void; reject: (e: unknown) => void };

type Incoming = { id?: number | string | null; method?: string; params?: unknown; result?: unknown; error?: RpcErrorObject };

export class RpcClient {
  private readonly transport: Transport;
  private nextId = 1;
  private readonly pending = new Map<number, Pending>();
  private readonly listeners = new Map<string, Set<(params: never) => void>>();
  private readonly handlers = new Map<string, (params: never) => Promise<unknown>>();
  /** An incomplete last line, until the rest of it arrives. */
  private partial = "";
  private closed = false;
  private readonly closeListeners = new Set<() => void>();

  constructor(transport: Transport) {
    this.transport = transport;
    transport.onText((text) => this.receive(text));
    transport.onClose(() => this.close());
  }

  request<M extends keyof Methods & string>(method: M, params: Methods[M][0]): Promise<Methods[M][1]> {
    if (this.closed) return Promise.reject(new RpcError(disconnected));
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject });
      this.transport.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
    });
  }

  notify(method: string, params: unknown) {
    if (!this.closed) this.transport.send(JSON.stringify({ jsonrpc: "2.0", method, params }) + "\n");
  }

  /** Listens for a notification's method; returns a function that stops. */
  on<N extends keyof Notifications & string>(method: N, cb: (params: Notifications[N]) => void): () => void {
    let set = this.listeners.get(method);
    if (!set) this.listeners.set(method, (set = new Set()));
    const f = cb as (params: never) => void;
    set.add(f);
    return () => void set.delete(f);
  }

  /** Answers the server's requests of a method; an error thrown is the error response. */
  handle<M extends keyof ServerRequests & string>(method: M, cb: (params: ServerRequests[M][0]) => Promise<ServerRequests[M][1]>) {
    this.handlers.set(method, cb as (params: never) => Promise<unknown>);
  }

  private answer(id: number | string, method: string, params: unknown) {
    const handler = this.handlers.get(method);
    const reply = (body: object) => {
      if (!this.closed) this.transport.send(JSON.stringify({ jsonrpc: "2.0", id, ...body }) + "\n");
    };
    if (!handler) return reply({ error: { code: -32601, message: `method not found: ${method}` } });
    handler(params as never).then(
      (result) => reply({ result }),
      (e: unknown) => reply({ error: { code: -32603, message: e instanceof Error ? e.message : String(e) } }),
    );
  }

  private receive(text: string) {
    const lines = (this.partial + text).split("\n");
    this.partial = lines.pop() ?? "";
    for (const line of lines) {
      if (line === "") continue;
      let msg: Incoming;
      try {
        msg = JSON.parse(line);
      } catch {
        continue; // Not ours to fix; the server answers malformed input, not the client.
      }
      this.dispatch(msg);
    }
  }

  private dispatch(msg: Incoming) {
    if (msg.method !== undefined && msg.id != null) return this.answer(msg.id, msg.method, msg.params);
    if (msg.method !== undefined) {
      this.listeners.get(msg.method)?.forEach((f) => f(msg.params as never));
      return;
    }
    if (typeof msg.id !== "number") return;
    const p = this.pending.get(msg.id);
    if (!p) return;
    this.pending.delete(msg.id);
    if (msg.error) p.reject(new RpcError(msg.error));
    else p.resolve(msg.result ?? {});
  }

  /** Calls back once the connection has ended (pending requests have failed by then). */
  onClose(cb: () => void): () => void {
    if (this.closed) {
      cb();
      return () => {};
    }
    this.closeListeners.add(cb);
    return () => void this.closeListeners.delete(cb);
  }

  get isClosed(): boolean {
    return this.closed;
  }

  private close() {
    if (this.closed) return;
    this.closed = true;
    for (const p of this.pending.values()) p.reject(new RpcError(disconnected));
    this.pending.clear();
    for (const cb of this.closeListeners) cb();
    this.closeListeners.clear();
  }
}
