import type { CallAdapter, CallResult } from "@hermesh/her-client";
import type { CallResponse, OpenEvent, Response } from "@hermesh/proto/her";

// her's connections on a machine, through hermeshd's LocalService (HerOpen,
// HerCall): her's own JSON-RPC as bytes inside, which openCallTransport turns
// into the pipe an RpcClient reads (docs/her-server.md §6).

/** The part of LocalService a connection uses: api.ts's client, or a test's. */
export interface HerLocal {
  herOpen(req: { machineId: string; open: { setup: { method: string; params: Uint8Array }[] } }, o: { signal: AbortSignal }): AsyncIterable<OpenEvent>;
  herCall(req: { machineId: string; call: { connectionId: string; request: { method: string; params: Uint8Array } } }): Promise<CallResponse>;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** her's params as the bytes of their JSON. */
export const jsonBytes = (v: unknown): Uint8Array => encoder.encode(JSON.stringify(v ?? {}));

/** JSON from bytes; undefined for none. */
const fromBytes = (b: Uint8Array): unknown => (b.length > 0 ? JSON.parse(decoder.decode(b)) : undefined);

/** A request's answer as her gave it: its result, or her's error. */
export function callResult(r: Response | undefined): CallResult {
  if (!r) return { error: { code: -32603, message: "no answer", data: { kind: "internal_error" } } };
  if (r.error) {
    const data = fromBytes(r.error.data);
    return { error: { code: r.error.code, message: r.error.message, ...(data === undefined ? {} : { data }) } };
  }
  return { result: fromBytes(r.result) ?? {} };
}

/** Connections to her on one machine. hermeshd's own refusals (off the allowlist, her not running) throw. */
export function herAdapter(local: HerLocal, machineId: string): CallAdapter {
  return {
    async *open(signal, setup) {
      const stream = local.herOpen({ machineId, open: { setup: setup.map((r) => ({ method: r.method, params: jsonBytes(r.params) })) } }, { signal });
      for await (const e of stream) {
        if (e.kind.case === "opened") yield { opened: { connectionId: e.kind.value.connectionId, responses: e.kind.value.responses.map(callResult) } };
        else if (e.kind.case === "notification") yield { notification: { method: e.kind.value.method, params: fromBytes(e.kind.value.params) ?? {} } };
      }
    },
    async call(connectionId, method, params) {
      const r = await local.herCall({ machineId, call: { connectionId, request: { method, params: jsonBytes(params) } } });
      return callResult(r.response);
    },
  };
}
