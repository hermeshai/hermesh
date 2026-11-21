import { splitCommand } from "./model.ts";

// What the add sheet's address box holds (customize-design §10.9): a remote address, a command
// line with leading VAR=value, or a server's JSON as other apps write it; and the name to
// offer for it.

/** One header or variable; `saved` is its reference when its value is already in the credential store. */
export type Row = { key: string; value: string; locked: boolean; saved?: string | null };

export const secretish = /token|key|secret|password|passwd|auth|cookie|credential/i;
export const row = (key: string, value: string): Row => ({ key, value, locked: secretish.test(key) && !!value });

/** Loader/interpreter injection variables refused by herhost for command connectors. */
export const blockedEnvVars = new Set([
  "LD_PRELOAD",
  "LD_LIBRARY_PATH",
  "LD_AUDIT",
  "DYLD_INSERT_LIBRARIES",
  "DYLD_LIBRARY_PATH",
  "DYLD_FRAMEWORK_PATH",
  "DYLD_FALLBACK_LIBRARY_PATH",
  "NODE_OPTIONS",
  "PYTHONSTARTUP",
  "PYTHONPATH",
  "PYTHONHOME",
  "PERL5OPT",
  "RUBYOPT",
  "BASH_ENV",
  "ENV",
  "IFS",
  "ZDOTDIR",
]);

export const envVarName = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** Detects unquoted shell operators (pipes, chaining, redirects, subshells) in a pasted command line. */
export function hasShellSyntax(line: string): boolean {
  let quote: string | null = null;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote) {
      if (c === quote) quote = null;
      else if (c === "\\" && quote === '"' && i + 1 < line.length) i++;
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
      continue;
    }
    if (c === "\\" && i + 1 < line.length) {
      i++;
      continue;
    }
    if (c === "|" || c === ";" || c === ">" || c === "<" || c === "`") return true;
    if (c === "&" && (line[i + 1] === "&" || i === line.length - 1 || /\s/.test(line[i + 1] ?? ""))) return true;
    if (c === "$" && line[i + 1] === "(") return true;
  }
  return false;
}

export type ProgramIssue = "empty" | "space" | "shell" | "var" | "batch" | null;

/** Validates the executable name/path of a local stdio connector (no shell, no interpolation). */
export function validateProgram(program: string): ProgramIssue {
  const p = program.trim();
  if (!p) return "empty";
  if (p.includes("${")) return "var";
  if (hasShellSyntax(p)) return "shell";
  if (/[\x00-\x1f\x7f]/.test(p) || (!/^(\/|\.\.?\/|~\/|[A-Za-z]:[\\/])/.test(p) && /\s/.test(p))) return "space";
  if (/\.(bat|cmd)$/i.test(p)) return "batch";
  return null;
}

/** Normalizes a candidate name into a valid connector identifier (1-32 chars of [a-z0-9_-]). */
export function sanitizeServerName(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}

const isRecord = (x: unknown): x is Record<string, unknown> => !!x && typeof x === "object" && !Array.isArray(x);
const KNOWN_LOCAL_JSON_KEYS = new Set(["command", "args", "env", "type", "transport", "name", "disabled", "enabled"]);

export type ImportedServer =
  | { kind: "local"; name?: string; command: string; args: string[]; env: Row[]; skipped: string[] }
  | { kind: "remote"; name?: string; url: string; host: string; insecure: boolean; sse: boolean; headers: Row[] };

export type JsonImportResult =
  | { ok: true; servers: ImportedServer[] }
  | { ok: false; error: "empty" | "json" | "jsonServer" | "badArgs" | "badEnv" };

/** Parses an MCP server JSON snippet (single server or mcpServers/mcp_servers/servers map) for form pre-fill. */
export function parseMcpJson(text: string): JsonImportResult {
  const t = text.trim();
  if (!t) return { ok: false, error: "empty" };
  let v: unknown;
  try {
    v = JSON.parse(t);
  } catch {
    return { ok: false, error: "json" };
  }
  if (!isRecord(v)) return { ok: false, error: "json" };
  const isServer = (x: unknown): x is Record<string, unknown> => isRecord(x) && ("command" in x || "url" in x);
  const rawTable = isServer(v) ? { "": v } : (v["mcpServers"] ?? v["mcp_servers"] ?? v["servers"] ?? v);
  if (!isRecord(rawTable)) return { ok: false, error: "jsonServer" };
  const entries = Object.entries(rawTable).filter(([, s]) => isServer(s)) as [string, Record<string, unknown>][];
  if (!entries.length) return { ok: false, error: "jsonServer" };

  const servers: ImportedServer[] = [];
  for (const [entryName, s] of entries) {
    if (typeof s.url === "string" && typeof s.command !== "string") {
      const inner = detect(s.url);
      if (!inner || inner.kind !== "remote") return { ok: false, error: "jsonServer" };
      if (s.headers !== undefined && !isRecord(s.headers)) return { ok: false, error: "badEnv" };
      const headers = isRecord(s.headers) ? Object.entries(s.headers).map(([k, val]) => row(k, String(val ?? ""))) : [];
      servers.push({
        kind: "remote",
        name: entryName || undefined,
        url: inner.url,
        host: inner.host,
        insecure: inner.insecure,
        sse: inner.sse,
        headers,
      });
      continue;
    }
    const command = typeof s.command === "string" ? s.command.trim() : "";
    if (!command) return { ok: false, error: "jsonServer" };
    if (s.args !== undefined && (!Array.isArray(s.args) || !s.args.every((a) => typeof a === "string"))) {
      return { ok: false, error: "badArgs" };
    }
    if (s.env !== undefined && !isRecord(s.env)) {
      return { ok: false, error: "badEnv" };
    }
    const args = Array.isArray(s.args) ? [...s.args] : [];
    const env = isRecord(s.env) ? Object.entries(s.env).map(([k, val]) => row(k, String(val ?? ""))) : [];
    const skipped = Object.keys(s).filter((k) => !KNOWN_LOCAL_JSON_KEYS.has(k));
    servers.push({ kind: "local", name: entryName || undefined, command, args, env, skipped });
  }
  return { ok: true, servers };
}

/** Validates local stdio args and env rows before submitting to the signer. */
export function validateLocalRows(
  name: string,
  args: string[],
  env: Row[],
): { argsError?: "cred" | "nul"; envError?: { kind: "name" | "blocked" | "nul" | "scope" | "path" | "duplicate"; key: string } } {
  for (const a of args) {
    if (a.includes("\0")) return { argsError: "nul" };
    if (/\$\{credential:[^}]+\}/.test(a)) return { argsError: "cred" };
  }
  const prefix = `mcp.${name}`;
  const keys = new Set<string>();
  for (const r of env) {
    const k = r.key.trim();
    if (!k) continue;
    if (!envVarName.test(k)) return { envError: { kind: "name", key: k } };
    if (keys.has(k)) return { envError: { kind: "duplicate", key: k } };
    keys.add(k);
    if (blockedEnvVars.has(k.toUpperCase())) return { envError: { kind: "blocked", key: k } };
    const val = r.value || r.saved || "";
    if (val.includes("\0")) return { envError: { kind: "nul", key: k } };
    if (k.toUpperCase() === "PATH" && ((r.locked && r.value) || val.includes("${credential:"))) return { envError: { kind: "path", key: k } };
    for (const m of val.matchAll(/\$\{credential:([^}]+)\}/g)) {
      const cred = m[1];
      if (cred !== prefix && !cred.startsWith(`${prefix}.`) && !cred.startsWith(`mcp-stdio.${name}.`)) {
        return { envError: { kind: "scope", key: k } };
      }
    }
  }
  return {};
}

export type Detected =
  | { kind: "remote"; url: string; host: string; insecure: boolean; sse: boolean; json?: boolean; name?: string; headers: Row[] }
  | { kind: "local"; argv: string[]; env: Row[]; json?: boolean; name?: string }
  | { kind: "bad"; why: "json" | "jsonServer" | "url" | "scheme" | "onlyVars" | "shell" };

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
    if (typeof s.url === "string") {
      const inner = detect(s.url);
      if (!inner || inner.kind !== "remote") return inner ?? { kind: "bad", why: "url" };
      return { ...inner, json: true, name: name || undefined, headers: rows(s.headers) };
    }
    const cmd = typeof s.command === "string" ? s.command.trim() : "";
    if (!cmd) return { kind: "bad", why: "jsonServer" };
    const args = Array.isArray(s.args) ? s.args.map(String) : [];
    return { kind: "local", argv: [cmd, ...args], json: true, name: name || undefined, env: rows(s.env) };
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
  if (hasShellSyntax(t)) return { kind: "bad", why: "shell" };
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
