import type { ChatGptStatus, McpLoginResult } from "@hermesh/her-protocol";

// Signing in for an environment's Her from the Mac app (docs/her-server.md §5.4): to ChatGPT, or
// to a connector a secure card connects (docs/her-secure-cards.md §4), as the console does
// (her/apps/console/src/signin.ts), but in the main process: this computer's hermeshd catches the
// browser's redirect on 127.0.0.1 (the console's device-side routes, internal/owner/her_console.go),
// her on the Her Server makes the authorization request and keeps the PKCE verifier, the browser
// opens it, and what the redirect carried goes back to her, who exchanges it and keeps the tokens
// there. Nothing of it stays here.

/** How a sign-in ended, for the page: her's words and the account, never a token. */
export type ChatGptSignIn = { state: "connected" | "failed" | "cancelled"; account?: string; message?: string };

/** How a connector's sign-in ended: her's words when it failed. */
export type ConnectorSignIn = { state: "connected" | "failed" | "cancelled"; message?: string };

/** What the browser brought back to the catcher. */
type Caught = { code: string; state: string; client_id?: string; iss?: string } | { error: string; error_description?: string };

/** What the redirect carried when the provider let the person in. */
type Granted = Extract<Caught, { code: string }>;

/** A sign-in her started: where the browser goes, and the id that finishes or cancels it. */
type Started = { authorize_url: string; login_id: string };

export type SignInDeps = {
  /** The environment's console here, ending in "/" (GetHerConsole). */
  base: string;
  fetch: typeof fetch;
  /** A request to her on the environment: its result, or her's error thrown. */
  call: <T>(method: string, params: unknown) => Promise<T>;
  /** Opens the authorization page in the browser. */
  open: (url: string) => Promise<void>;
  /** Stops the sign-in, and her's side with it. */
  signal: AbortSignal;
  /** How long to wait before asking the catcher again after a request failed on the way. */
  retry?: number;
};

/** One kind of sign-in: how her starts, finishes and cancels it, and how it ended. */
type Flow<T> = {
  /** The catcher's path: `/auth/callback` for ChatGPT, `/callback` for a connector. */
  path: string;
  /** Asks her to start, the browser coming back to `redirect_uri`; or how it ended without starting. */
  start: (redirect_uri: string) => Promise<Started | T>;
  /** Hands her what the redirect carried; her exchanges it and keeps the tokens. */
  complete: (login_id: string, granted: Granted) => Promise<T>;
  /** Ends the sign-in her keeps waiting. */
  cancel: (login_id: string) => Promise<unknown>;
  failed: (message: string) => T;
  cancelled: T;
};

/** Why a route said no: the `error` of its JSON, or its text. */
async function refusal(r: Response): Promise<Error> {
  const text = (await r.text()).trim();
  try {
    const said = JSON.parse(text) as { error?: unknown };
    if (typeof said.error === "string") return new Error(said.error);
  } catch {
    // Plain text.
  }
  return new Error(text || `HTTP ${r.status}`);
}

async function json<T>(r: Response): Promise<T> {
  if (!r.ok) throw await refusal(r);
  return (await r.json()) as T;
}

const pause = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => (clearTimeout(timer), reject(signal.reason)), { once: true });
  });

/** What the catcher caught, once it has: each GET waits a while and says when it is still pending; one that fails on the way is asked again. */
async function caught(d: SignInDeps, id: string): Promise<Caught> {
  for (;;) {
    let r: Response;
    try {
      r = await d.fetch(`${d.base}signin/${encodeURIComponent(id)}`, { signal: d.signal });
    } catch (e) {
      if (d.signal.aborted) throw e;
      await pause(d.retry ?? 1000, d.signal);
      continue;
    }
    const body = await json<Caught | { pending: true }>(r);
    if (!("pending" in body && body.pending)) return body as Caught;
  }
}

const begun = (s: object): s is Started => "login_id" in s;

/**
 * A sign-in that ends on this computer: resolves once her has the tokens, or with why not. The
 * provider's refusal, a failure on the way, or `signal` cancels her's side too.
 */
async function signIn<T extends object>(d: SignInDeps, f: Flow<T>): Promise<T> {
  let login: string | undefined;
  try {
    const catcher = await json<{ id: string; redirect_uri: string }>(
      await d.fetch(`${d.base}signin`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: f.path }), signal: d.signal }),
    );
    const started = await f.start(catcher.redirect_uri);
    if (!begun(started)) return started;
    login = started.login_id;
    d.signal.throwIfAborted();
    await d.open(started.authorize_url);
    const got = await caught(d, catcher.id);
    if ("error" in got) {
      await f.cancel(login).catch(() => {});
      return f.failed(got.error_description || got.error);
    }
    return await f.complete(login, got);
  } catch (e) {
    if (login) await f.cancel(login).catch(() => {});
    if (d.signal.aborted) return f.cancelled;
    throw e;
  }
}

const ended = (s: ChatGptStatus): ChatGptSignIn => ({
  state: s.state === "connected" ? "connected" : s.state === "cancelled" ? "cancelled" : "failed",
  account: s.account ?? undefined,
  message: s.message ?? undefined,
});

/** Signs in to ChatGPT for the environment. */
export function signInChatGpt(d: SignInDeps): Promise<ChatGptSignIn> {
  return signIn<ChatGptSignIn>(d, {
    path: "/auth/callback",
    start: async (redirect_uri) => {
      const s = await d.call<ChatGptStatus>("chatgpt/login", { redirect_uri });
      return s.authorize_url && s.login_id ? { authorize_url: s.authorize_url, login_id: s.login_id } : { state: "failed", message: s.message ?? undefined };
    },
    // ChatGPT names the client it registered in the redirect of a first sign-in.
    complete: async (login_id, g) => ended(await d.call<ChatGptStatus>("chatgpt/complete", { login_id, code: g.code, state: g.state, client_id: g.client_id })),
    cancel: (login_id) => d.call("chatgpt/cancel", { login_id }),
    failed: (message) => ({ state: "failed", message }),
    cancelled: { state: "cancelled" },
  });
}

/**
 * Signs in to the environment's connector `name`, in her's configuration already (a card's ops
 * added it): her says on the card how it went once it lists the connector's tools.
 */
export function signInConnector(d: SignInDeps, name: string): Promise<ConnectorSignIn> {
  return signIn<ConnectorSignIn>(d, {
    path: "/callback",
    start: (redirect_uri) => d.call<McpLoginResult>("mcp/login", { name, redirect_uri }),
    complete: async (login_id, g) => (await d.call("mcp/login_complete", { login_id, code: g.code, state: g.state, iss: g.iss }), { state: "connected" }),
    cancel: () => d.call("mcp/login_cancel", { name }),
    failed: (message) => ({ state: "failed", message }),
    cancelled: { state: "cancelled" },
  });
}

/** Where a connector's sign-in may send the browser: an https page, or an http one on loopback, as her lets a connector be addressed. */
export function connectorPage(url: string): boolean {
  try {
    const u = new URL(url);
    return u.protocol === "https:" || (u.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(u.hostname));
  } catch {
    return false;
  }
}
