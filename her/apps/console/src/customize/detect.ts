import { joinCommand, splitCommand } from "./model.ts";

// What the add sheet's address box holds (customize-design §10.9): a remote address, a command
// line with leading VAR=value, or a server's JSON as other apps write it; and the name to
// offer for it.

/** One header or variable; `saved` is its reference when its value is already in the credential store. */
export type Row = { key: string; value: string; locked: boolean; saved?: string | null };

export const secretish = /token|key|secret|password|passwd|auth|cookie|credential/i;
export const row = (key: string, value: string): Row => ({ key, value, locked: secretish.test(key) && !!value });

export type Detected =
  | { kind: "remote"; url: string; host: string; insecure: boolean; sse: boolean; json?: boolean; name?: string; headers: Row[] }
  | { kind: "local"; argv: string[]; env: Row[]; json?: boolean; name?: string }
  | { kind: "bad"; why: "json" | "jsonServer" | "url" | "scheme" | "onlyVars" };

export function detect(text: string): Detected | null {
  const t = text.trim();
  if (!t) return null;
  if (t.startsWith("{")) {
    let v: unknown;
    try {
      v = JSON.parse(t);
    } catch {
      return { kind: "bad", why: "json" };
    }
    const o = v as Record<string, unknown>;
    const isServer = (x: unknown): boolean => !!x && typeof x === "object" && ("command" in (x as object) || "url" in (x as object));
    const table = (isServer(o) ? { "": o } : (o["mcpServers"] ?? o["mcp_servers"] ?? o["servers"] ?? o)) as Record<string, unknown>;
    const found = Object.entries(table ?? {}).find(([, s]) => isServer(s)) as [string, Record<string, unknown>] | undefined;
    if (!found) return { kind: "bad", why: "jsonServer" };
    const [name, s] = found;
    const rows = (m: unknown) => Object.entries((m as Record<string, unknown>) ?? {}).map(([k, val]) => row(k, String(val ?? "")));
    const inner = typeof s.url === "string" ? detect(s.url) : detect(joinCommand(String(s.command), Array.isArray(s.args) ? s.args.map(String) : []));
    if (!inner || inner.kind === "bad") return inner;
    return inner.kind === "remote" ? { ...inner, json: true, name: name || undefined, headers: rows(s.headers) } : { ...inner, json: true, name: name || undefined, env: rows(s.env) };
  }
  if (/^https?:\/\//i.test(t)) {
    let u: URL;
    try {
      u = new URL(t);
    } catch {
      return { kind: "bad", why: "url" };
    }
    const local = ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname);
    return { kind: "remote", url: t, host: u.hostname, insecure: u.protocol === "http:" && !local, sse: /\/sse\/?$/.test(u.pathname), headers: [] };
  }
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(t)) return { kind: "bad", why: "scheme" };
  const argv = splitCommand(t);
  const env: Row[] = [];
  while (argv.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(argv[0])) {
    const [k, ...v] = argv.shift()!.split("=");
    env.push(row(k, v.join("=")));
  }
  if (!argv.length) return { kind: "bad", why: "onlyVars" };
  return { kind: "local", argv, env };
}

/** The name to offer when none is typed: the host's telling part, or the package's. */
export function suggestName(d: Detected | null): string {
  if (!d || d.kind === "bad") return "";
  if (d.name) return d.name;
  let n: string;
  if (d.kind === "remote") {
    // An address by number (127.0.0.1, [::1]) names nothing: no name is offered.
    if (/^[\d.]+$|^\[/.test(d.host)) return "";
    const parts = d.host.split(".").filter((p) => !["mcp", "api", "www", "server"].includes(p));
    n = parts.length > 1 ? parts[parts.length - 2] : (parts[0] ?? d.host);
  } else {
    const pkg = d.argv.slice(1).find((a) => !a.startsWith("-") && !["dlx", "run", "x"].includes(a)) ?? d.argv[0];
    const bare = pkg.replace(/(?<=.)@[^@/]+$/, "");
    const [scope, last] = bare.startsWith("@") ? bare.slice(1).split("/") : [null, bare.split("/").pop() ?? bare];
    n = (last ?? "").replace(/\.(py|js|ts)$/, "").replace(/^(mcp-server-|server-|mcp-)/, "").replace(/(-mcp-server|-mcp|-server)$/, "");
    if (!n || n === "mcp") n = scope ?? "local";
  }
  return n
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}
