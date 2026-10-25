import http from "node:http";
import { Readable } from "node:stream";

// The renderer reaches hermeshd only through here: requests to
// app://hermesh/api/<service>/<method> are forwarded to the daemon's Unix
// socket. Signing requests are the signer's alone, so those two methods are
// never forwarded.
const service = "hermesh.local.v1.LocalService";
const blocked = new Set(["WatchSigningRequests", "CompleteSigningRequest"]);
const forwardedHeaders = ["content-type", "connect-protocol-version", "connect-timeout-ms", "connect-content-encoding", "connect-accept-encoding"];

let open = 0;
/** Upstream requests in flight (for tests: a cancelled stream must not leak). */
export const openUpstream = () => open;

const calls = new Map<string, number>();
/** The calls forwarded so far, by method (for tests: idle windows make none, docs/state-sync.md §8). */
export const forwardedCalls = () => Object.fromEntries(calls);

export function allowed(apiPath: string): boolean {
  const m = /^\/([\w.]+)\/(\w+)$/.exec(apiPath);
  return m !== null && m[1] === service && !blocked.has(m[2]);
}

export async function forward(req: Request, apiPath: string, socket: string, cors: Record<string, string>): Promise<Response> {
  if (req.method === "OPTIONS") return new Response(null, { status: 204, headers: cors });
  if (req.method !== "POST" || !allowed(apiPath)) return new Response("not allowed", { status: 403, headers: cors });
  const method = apiPath.slice(apiPath.lastIndexOf("/") + 1);
  calls.set(method, (calls.get(method) ?? 0) + 1);
  const body = Buffer.from(await req.arrayBuffer());
  const headers: Record<string, string> = { "content-length": String(body.length) };
  for (const h of forwardedHeaders) {
    const v = req.headers.get(h);
    if (v) headers[h] = v;
  }
  return new Promise<Response>((resolve) => {
    open++;
    let done = false;
    const finish = () => {
      if (!done) {
        done = true;
        open--;
      }
    };
    const up = http.request({ socketPath: socket, path: apiPath, method: "POST", headers }, (res) => {
      res.on("close", finish);
      const out = new Headers(cors);
      for (const [k, v] of Object.entries(res.headers)) {
        if (typeof v === "string" && (k.startsWith("connect-") || k === "content-type" || k.startsWith("grpc-"))) out.set(k, v);
      }
      const stream = Readable.toWeb(res) as ReadableStream<Uint8Array>;
      resolve(new Response(stream, { status: res.statusCode ?? 502, headers: out }));
    });
    up.on("error", (err) => {
      finish();
      resolve(new Response(JSON.stringify({ code: "unavailable", message: `hermeshd: ${err.message}` }), {
        status: 503,
        headers: { ...cors, "content-type": "application/json" },
      }));
    });
    up.on("close", finish);
    req.signal?.addEventListener("abort", () => up.destroy());
    up.end(body);
  });
}
