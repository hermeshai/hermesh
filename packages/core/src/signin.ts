// Signing in and joining (docs/desktop-app.md §5.3, docs/trusted-devices.md
// 4.1–4.2): the pure parts the app's main process and its first-run pages
// share.

import { t } from "./i18n";

/** The hosted center the app signs in to unless the person changes it. */
export const officialCenter = "https://hermesh.io";

/** A hermesh:// link the app was opened with. */
export type AppLink = { kind: "link"; code: string } | { kind: "join"; owner: string };

/**
 * hermesh://link?code=… (the browser sign-in came back with a link code)
 * or hermesh://join?owner=… (the account already has an owner: join it).
 */
export function parseAppLink(s: string): AppLink | null {
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return null;
  }
  if (u.protocol !== "hermesh:") return null;
  if (u.host === "link") {
    const code = u.searchParams.get("code");
    return code ? { kind: "link", code } : null;
  }
  if (u.host === "join") {
    const owner = u.searchParams.get("owner");
    return owner ? { kind: "join", owner } : null;
  }
  return null;
}

/** RFC 7636 S256: base64url(SHA-256(verifier)), as the center checks it. */
export async function pkceChallenge(verifier: string): Promise<string> {
  const h = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier)));
  return base64url(h);
}

function base64url(b: Uint8Array): string {
  return btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** A center's address without a trailing slash; throws if it is not an http(s) URL. */
export function centerBase(center: string): string {
  const s = center.trim().replace(/\/+$/, "");
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    throw new Error(t("signin.badCenter", { center }));
  }
  if ((u.protocol !== "https:" && u.protocol !== "http:") || !u.host) throw new Error(t("signin.badCenter", { center }));
  return s;
}

/** A root key's platform, by Node's name for the OS. */
export function rootPlatform(nodePlatform: string): string {
  return nodePlatform === "darwin" ? "macos" : nodePlatform === "win32" ? "windows" : "linux";
}

/** The page the browser sign-in starts on. */
export function appLoginURL(center: string, challenge: string): string {
  return `${centerBase(center)}/app-login?challenge=${encodeURIComponent(challenge)}`;
}

/** Where a center publishes its connect URL. */
export function discoveryURL(center: string): string {
  return `${centerBase(center)}/.well-known/hermesh.json`;
}

/**
 * Where a center's devices connect, from its answer to discoveryURL (as in
 * internal/connaddr): a center that publishes none (404, or no
 * connect_url) is its own. The answer is trusted no more than routing, but
 * it must be a bare origin, and https unless the center itself is http.
 */
export function connectURLFrom(center: string, status: number, body: string): string {
  const base = centerBase(center);
  if (status === 404) return base;
  if (status !== 200) throw new Error(t("signin.httpStatus", { url: discoveryURL(center), status }));
  let doc: { connect_url?: unknown };
  try {
    doc = JSON.parse(body);
  } catch {
    throw new Error(t("signin.notJson", { url: discoveryURL(center) }));
  }
  const s = typeof doc?.connect_url === "string" ? doc.connect_url : "";
  if (s === "") return base;
  const bad = new Error(t("signin.badConnectURL", { url: s }));
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    throw bad;
  }
  const scheme = u.protocol.slice(0, -1);
  const okScheme = scheme === "https" || (scheme === "http" && base.startsWith("http://"));
  if (!okScheme || !u.host || s.includes("?") || s.includes("#") || u.pathname.replace(/\//g, "") !== "") throw bad;
  return `${scheme}://${u.host}`;
}

/**
 * A connection address, "relay.example.com/o_…": where devices connect and
 * the owner, as internal/connaddr parses it.
 */
export function parseConnectAddress(s: string): { relay: string; owner: string } | null {
  let t = s.trim();
  if (t === "") return null;
  if (!t.includes("://")) t = "https://" + t;
  let u: URL;
  try {
    u = new URL(t);
  } catch {
    return null;
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") return null;
  const path = u.pathname.replace(/^\/+|\/+$/g, "");
  const i = path.lastIndexOf("/");
  const owner = path.slice(i + 1);
  if (!owner.startsWith("o_") || owner.length !== 26) return null;
  const prefix = i > 0 ? "/" + path.slice(0, i) : "";
  return { relay: `${u.protocol}//${u.host}${prefix}`, owner };
}

/** A project id for a folder the person picked: its name, made safe and unique. */
export function projectIdFromFolder(name: string, taken: ReadonlySet<string>): string {
  let s = [...name.toLowerCase()].map((c) => (/^[a-z0-9._-]$/.test(c) ? c : "-")).join("");
  s = s.replace(/^[-._]+/, "");
  if (s === "") s = "project";
  s = s.slice(0, 60);
  let id = s;
  for (let n = 2; taken.has(id); n++) id = `${s}-${n}`;
  return id;
}
