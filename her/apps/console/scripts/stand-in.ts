import { spawn, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { connect, type Socket } from "node:net";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { createInterface } from "node:readline";
import { PROTOCOL_VERSION } from "@hermesh/her-protocol";

// The console's routes as hermeshd serves them, in front of a real `her serve --listen unix://…`
// (as packages/her-client/src/http.e2e.test.ts stands in for hermeshd in front of the mock her):
// the machine's /her/console/ (the built console, events, rpc with hermeshd's allowlist and its
// limits on config/write, upload, export) and, on the same address, what the owner's device adds
// (signin, which catches a sign-in's redirect on 127.0.0.1, and command-connector, applied
// here at once where the device would ask its signer first). The model service, the search
// services and an MCP server are her-mock's. Mirrors internal/herhost/allow.go and console.go
// and internal/owner/her_console.go; for the dev server and the end-to-end test.

export type StandIn = {
  /** The console's address: http://127.0.0.1:<port>/her/console/ */
  url: string;
  /** her's home, private to this run. */
  home: string;
  /** her-mock's MCP server, for a connector added by address. */
  mcpUrl: string;
  /** Every connection the console holds drops, as when the tunnel goes. */
  drop(): void;
  close(): Promise<void>;
};

/** hermeshd's allowlist for the console (allow.go's `allowed`, its console part). */
const allowed = new Set([
  "models/list",
  "config/read",
  "config/write",
  "connections/list",
  "connections/discover",
  "connections/connect",
  "connections/disconnect",
  "connections/remove",
  "connections/retry",
  "connections/models",
  "credentials/set",
  "credentials/delete",
  "auth/set",
  "auth/delete",
  "chatgpt/status",
  "chatgpt/login",
  "chatgpt/complete",
  "chatgpt/logout",
  "chatgpt/cancel",
  "mcp/list",
  "mcp/check",
  "mcp/login",
  "mcp/login_complete",
  "mcp/login_cancel",
  "mcp/logout",
  "mcp/probe",
  "mcp/probe_cancel",
  "mcp/tools",
  "skills/list",
  "skills/read",
  "skills/files",
  "skills/install",
  "skills/remove",
  "skills/set_enabled",
  "web_search/status",
  "web_search/set_key",
  "web_search/test",
  "browser/allow",
  "browser/status",
  "browser/use_extension",
  "models/refresh",
  "models/show",
]);

/** What config/write may change (allow.go's `writable`): null, anything under it; "*", any key. */
type Node = { [key: string]: Node | null };
const any = null;
const writable: Node = {
  web_search: any,
  browser: { enabled: any, default: any, sites: any, extension: any },
  mcp_servers: {
    "*": {
      name: any,
      url: any,
      transport: any,
      headers: any,
      bearer_token_env_var: any,
      auth: any,
      oauth: any,
      enabled: any,
      exposure: any,
      description: any,
      direct_tools: any,
      enabled_tools: any,
      disabled_tools: any,
      startup_timeout_secs: any,
      tool_timeout_secs: any,
      parallel_tools: any,
      required: any,
    },
  },
};
const commandKeys = new Set(["command", "args", "env", "env_vars", "cwd", "connector"]);

/** Whether an edit setting `keys` to `value` changes only what `writable` allows (allow.go's `writes`). */
function writes(keys: string[], value: unknown, upsert: boolean): boolean {
  let n: Node = writable;
  for (const k of keys) {
    const next = Object.hasOwn(n, k) ? n[k] : Object.hasOwn(n, "*") ? n["*"] : undefined;
    if (next === undefined) return false;
    if (next === null) return true;
    n = next;
  }
  const connectors = keys[0] === "mcp_servers";
  if (value === null || value === undefined) return connectors;
  if (typeof value !== "object" || Array.isArray(value) || (!upsert && !connectors)) return false;
  return Object.entries(value).every(([k, v]) => writes([...keys, k], v, true));
}

/** A key path split as her splits it: `a.b."c.d"` is a, b, c.d. */
function keyPath(text: string): string[] {
  const keys: string[] = [];
  for (let i = 0; ; ) {
    let key = "";
    if (text[i] === '"') {
      for (i++; ; i++) {
        if (i >= text.length) throw new Error(`unfinished quote in key path ${JSON.stringify(text)}`);
        if (text[i] === '"') {
          i++;
          break;
        }
        if (text[i] === "\\") i++;
        key += text[i];
      }
    } else {
      for (; i < text.length && text[i] !== "."; i++) key += text[i];
      if (!/^[A-Za-z0-9_-]+$/.test(key)) throw new Error(`bad key in key path ${JSON.stringify(text)}`);
    }
    keys.push(key);
    if (i === text.length) return keys;
    if (text[i] !== ".") throw new Error(`key path ${JSON.stringify(text)}: unexpected ${text[i]}`);
    i++;
  }
}

/** Why hermeshd refuses a request from an owner's device, else null (allow.go's `check`). */
function refusal(method: string, params: Record<string, unknown> | undefined): string | null {
  if (!allowed.has(method)) return `her's "${method}" is not open to the owner's devices`;
  if (method === "config/write")
    for (const e of (params?.edits ?? []) as { key_path?: string; value?: unknown; merge?: string }[]) {
      let keys: string[];
      try {
        keys = keyPath(e.key_path ?? "");
      } catch (err) {
        return (err as Error).message;
      }
      if (!writes(keys, e.value, e.merge === "upsert")) return `the owner's devices may not change ${e.key_path}: settings that let Her do more on this machine need the owner's root key`;
    }
  if ((method === "mcp/probe" || method === "mcp/login") && Object.keys((params?.server ?? {}) as object).some((k) => commandKeys.has(k)))
    return "only SetHerCommandConnector, signed by the owner's root key, adds or changes a connector started as a command";
  return null;
}

type Answer = { id?: number; result?: unknown; error?: unknown };

/** One connection to her's socket: requests under ids of its own, notifications to `onNote`. */
class HerConnection {
  private next = 0;
  private readonly waiting = new Map<number, (a: Answer) => void>();
  private readonly socket: Socket;
  onNote: (line: string) => void = () => {};
  readonly closed: Promise<void>;

  private constructor(socket: Socket) {
    this.socket = socket;
    const lines = createInterface({ input: socket });
    lines.on("line", (line) => {
      const msg = JSON.parse(line) as Answer & { method?: string };
      if (msg.method === undefined && typeof msg.id === "number") {
        this.waiting.get(msg.id)?.(msg);
        this.waiting.delete(msg.id);
      } else if (msg.method !== undefined && msg.id === undefined) this.onNote(line);
      else if (msg.method !== undefined) socket.write(JSON.stringify({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "the console answers no requests" } }) + "\n");
    });
    this.closed = new Promise((resolve) => socket.on("close", () => resolve()));
    this.closed.then(() => {
      for (const done of this.waiting.values()) done({ error: { code: -32099, message: "her went away", data: { kind: "disconnected" } } });
    });
  }

  static async open(socketPath: string): Promise<HerConnection> {
    const socket = connect(socketPath);
    await new Promise<void>((resolve, reject) => socket.once("connect", resolve).once("error", reject));
    const c = new HerConnection(socket);
    const init = await c.call("initialize", { protocol_version: PROTOCOL_VERSION, client_info: { name: "hermeshd (stand-in)" } });
    if (init.error) throw new Error(`initialize: ${JSON.stringify(init.error)}`);
    return c;
  }

  call(method: string, params: unknown): Promise<Answer> {
    const id = ++this.next;
    return new Promise((resolve) => {
      this.waiting.set(id, resolve);
      this.socket.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
    });
  }

  close() {
    this.socket.destroy();
  }
}

async function body(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return Buffer.concat(chunks);
}

const types: Record<string, string> = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".ttf": "font/ttf", ".json": "application/json" };

function send(res: ServerResponse, status: number, value: unknown) {
  res.writeHead(status, { "Content-Type": "application/json" }).end(JSON.stringify(value));
}

function refuse(res: ServerResponse, status: number, text: string) {
  res.writeHead(status, { "Content-Type": "text/plain; charset=utf-8" }).end(text + "\n");
}

/** A sign-in's catcher on 127.0.0.1, as the owner's device keeps one (her_console.go). */
type Catcher = { server: Server; result: Promise<Record<string, string>>; told: boolean };

async function catcher(redirectPath: string): Promise<{ catcher: Catcher; port: number }> {
  let done!: (r: Record<string, string>) => void;
  const result = new Promise<Record<string, string>>((resolve) => (done = resolve));
  const server = createServer((req, res) => {
    const u = new URL(req.url ?? "/", "http://127.0.0.1");
    if (req.method !== "GET" || u.pathname !== redirectPath) return void res.writeHead(404).end();
    const q = u.searchParams;
    const keep = (...keys: string[]) => Object.fromEntries(keys.filter((k) => q.has(k)).map((k) => [k, q.get(k)!]));
    done(q.get("error") ? keep("error", "error_description") : q.get("code") ? keep("code", "state", "client_id", "iss") : { error: "the sign-in came back without a code" });
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" }).end("<!doctype html><p>You can close this window and go back to Hermesh.</p>");
    server.close();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { catcher: { server, result, told: false }, port: (server.address() as AddressInfo).port };
}

/** Lines of a child's output until `last` matches one, with all of them. */
function readUntil(child: ChildProcess, last: RegExp): Promise<string[]> {
  return new Promise((resolve, reject) => {
    const lines: string[] = [];
    const rl = createInterface({ input: child.stdout! });
    rl.on("line", (line) => {
      lines.push(line);
      if (last.test(line)) {
        rl.close();
        resolve(lines);
      }
    });
    child.once("exit", (code) => reject(new Error(`${child.spawnfile} exited (${code}) before it was ready`)));
  });
}

async function waitForSocket(socketPath: string, her: ChildProcess) {
  for (let i = 0; i < 300; i++) {
    if (her.exitCode !== null) throw new Error(`her exited (${her.exitCode}) before it listened`);
    try {
      (await HerConnection.open(socketPath)).close();
      return;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  throw new Error(`her did not listen on ${socketPath}`);
}

/** `herEnv`: more of her's environment, such as HER_TEST_CHATGPT_BASE for her's ChatGPT fixture. */
export async function startStandIn(opts: { port?: number; bin?: string; dist?: string; herEnv?: Record<string, string> } = {}): Promise<StandIn> {
  const bin = opts.bin ?? process.env.HER_BIN_DIR ?? path.resolve(import.meta.dirname, "../../../target/debug");
  for (const program of ["her", "her-mock"])
    if (!existsSync(path.join(bin, program))) throw new Error(`no ${program} in ${bin}: build her (cargo build -p her-cli -p her-mock in her/) or point HER_BIN_DIR at its target/debug`);
  const dist = opts.dist ?? path.resolve(import.meta.dirname, "../dist");
  if (!existsSync(path.join(dist, "index.html"))) throw new Error(`no console in ${dist}: run vite build first`);

  // her-mock: a model service, the search services, and an MCP server (with --simulation).
  const mock = spawn(path.join(bin, "her-mock"), ["--simulation"], { stdio: ["ignore", "pipe", "inherit"] });
  const said = (await readUntil(mock, /^required = true/)).join("\n");
  const [modelsPart] = said.split("# Add the following to config.toml");
  // The mock's models without `auth = "none"`: its provider takes a key, as a real one does.
  const models = modelsPart.split("\n").slice(1).join("\n").replace(/^auth = "none"\n/m, "");
  const base = /^base_url = "(http:\/\/[^/"]+)/m.exec(models)![1];
  const mcpUrl = /^url = "([^"]+)"/m.exec(said)![1];

  const home = mkdtempSync(path.join(tmpdir(), "her-console-"));
  mkdirSync(path.join(home, "user"));
  writeFileSync(path.join(home, "models.toml"), models);
  writeFileSync(
    path.join(home, "config.toml"),
    `# Search services at her-mock, as only tests do.\n[web_search.endpoints]\nexa = "${base}/exa/mcp"\nparallel = "${base}/parallel/mcp"\ntavily = "${base}/tavily"\nbrave = "${base}/brave"\nbocha = "${base}/bocha"\n`,
  );
  const socketPath = path.join(home, "her.sock");
  const her = spawn(path.join(bin, "her"), ["serve", "--listen", `unix://${socketPath}`], {
    env: {
      ...process.env,
      HER_HOME: home,
      // A home of its own: no skills of another app, and the trash in the run's folder.
      HOME: path.join(home, "user"),
      HER_CREDENTIAL_STORE: "file",
      HER_NO_BROWSER: "1",
      HER_LANG: "en",
      HER_MODEL_CATALOG_URL: `${base}/catalog/api.json`,
      ...opts.herEnv,
    },
    stdio: ["ignore", "inherit", "inherit"],
  });
  try {
    await waitForSocket(socketPath, her);
  } catch (e) {
    her.kill();
    mock.kill();
    throw e;
  }

  const held = new Map<string, HerConnection>();
  const catchers = new Map<string, Catcher>();

  const server = createServer(async (req, res) => {
    const u = new URL(req.url ?? "/", "http://127.0.0.1");
    try {
      if (u.pathname === "/" || u.pathname === "/her/console") return void res.writeHead(302, { Location: "/her/console/" }).end();
      const route = u.pathname.startsWith("/her/console/") ? u.pathname.slice("/her/console/".length) : null;
      if (route === null) return refuse(res, 404, "not found");

      if (route === "events" && req.method === "GET") {
        const c = await HerConnection.open(socketPath);
        const id = randomBytes(8).toString("hex");
        held.set(id, c);
        res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
        res.write(`event: opened\ndata: ${JSON.stringify({ connection_id: id })}\n\n`);
        c.onNote = (line) => res.write(`data: ${line}\n\n`);
        req.on("close", () => c.close());
        await c.closed;
        held.delete(id);
        res.end();
        return;
      }

      if (route === "rpc" && req.method === "POST") {
        const request = JSON.parse((await body(req)).toString("utf8")) as { id: unknown; method: string; params?: Record<string, unknown> };
        const why = refusal(request.method, request.params);
        if (why) return refuse(res, 403, why);
        let c = held.get(String(req.headers["her-connection"]));
        const own = !c;
        c ??= await HerConnection.open(socketPath);
        const answer = await c.call(request.method, request.params ?? {});
        if (own) c.close();
        return send(res, 200, { jsonrpc: "2.0", id: request.id, ...(answer.error ? { error: answer.error } : { result: answer.result }) });
      }

      if (route === "upload" && req.method === "POST") {
        const name = String(req.headers["her-file-name"] ?? "");
        if (!name || name === "." || name === ".." || /[/\\\0]/.test(name)) return refuse(res, 400, "Her-File-Name is not a file's name.");
        const dir = path.join(home, "uploads", "console");
        mkdirSync(dir, { recursive: true });
        const file = path.join(dir, `${randomBytes(8).toString("hex")}-${name}`);
        writeFileSync(file, await body(req));
        return send(res, 200, { path: file });
      }

      if (route === "export" && req.method === "GET") {
        // hermeshd asks her itself (data/export is off the allowlist), sends only a file her wrote in its exports/, then removes it.
        const own = await HerConnection.open(socketPath);
        const answer = await own.call("data/export", {});
        own.close();
        if (answer.error) return refuse(res, 400, `her's data/export: ${(answer.error as { message: string }).message}`);
        const file = realpathSync((answer.result as { path: string }).path);
        if (path.dirname(file) !== realpathSync(path.join(home, "exports"))) return refuse(res, 403, `${file} is not an archive in her's exports`);
        const bytes = readFileSync(file);
        rmSync(file);
        res.writeHead(200, { "Content-Type": "application/zip", "Content-Disposition": `attachment; filename=${path.basename(file)}`, "Cache-Control": "no-store" }).end(bytes);
        return;
      }

      if (route === "signin" && req.method === "POST") {
        const { path: redirectPath } = JSON.parse((await body(req)).toString("utf8")) as { path?: string };
        if (!redirectPath?.startsWith("/") || /[?#]/.test(redirectPath)) return send(res, 400, { error: "a sign-in needs the path its redirect goes to, such as /auth/callback" });
        const { catcher: c, port } = await catcher(redirectPath);
        const id = randomBytes(16).toString("hex");
        catchers.set(id, c);
        return send(res, 200, { id, redirect_uri: `http://127.0.0.1:${port}${redirectPath}` });
      }

      if (route.startsWith("signin/") && req.method === "GET") {
        const c = catchers.get(route.slice("signin/".length));
        if (!c) return send(res, 404, { error: "no such sign-in" });
        const result = await Promise.race([c.result, new Promise<null>((resolve) => setTimeout(() => resolve(null), 25_000))]);
        if (!result) return send(res, 200, { pending: true });
        catchers.delete(route.slice("signin/".length));
        return send(res, 200, result);
      }

      if (route === "command-connector" && req.method === "POST") {
        // The owner's device would show this to its signer, and apply it once signed.
        const c = JSON.parse((await body(req)).toString("utf8")) as { name: string; command?: string; args?: string[]; env?: Record<string, string>; remove?: boolean };
        const value = c.remove ? null : { command: c.command, ...(c.args?.length ? { args: c.args } : {}), ...(c.env && Object.keys(c.env).length ? { env: c.env } : {}) };
        const own = await HerConnection.open(socketPath);
        const answer = await own.call("config/write", { layer: "user", edits: [{ key_path: `mcp_servers.${JSON.stringify(c.name)}`, value, merge: "replace" }] });
        own.close();
        if (answer.error) return refuse(res, 400, (answer.error as { message: string }).message);
        return send(res, 200, {});
      }

      if (req.method !== "GET") return refuse(res, 405, "GET only");
      const file = path.join(dist, route === "" ? "index.html" : route);
      if (!file.startsWith(dist) || !existsSync(file)) return refuse(res, 404, "not found");
      res.writeHead(200, { "Content-Type": types[path.extname(file)] ?? "application/octet-stream" }).end(readFileSync(file));
    } catch (e) {
      if (!res.headersSent) refuse(res, 500, (e as Error).message);
      else res.end();
    }
  });
  await new Promise<void>((resolve) => server.listen(opts.port ?? 0, "127.0.0.1", resolve));
  const port = (server.address() as AddressInfo).port;

  return {
    url: `http://127.0.0.1:${port}/her/console/`,
    home,
    mcpUrl,
    drop() {
      for (const c of held.values()) c.close();
    },
    async close() {
      for (const c of held.values()) c.close();
      for (const c of catchers.values()) c.server.close();
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
      her.kill();
      mock.kill();
      await new Promise((resolve) => (her.exitCode !== null ? resolve(null) : her.once("exit", resolve)));
      rmSync(home, { recursive: true, force: true });
    },
  };
}
