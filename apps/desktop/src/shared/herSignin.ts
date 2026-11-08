import type { ChatGptStatus } from "@hermesh/her-protocol";

// Signing in to ChatGPT for an environment's Her from the Mac app (docs/her-server.md §5.4), as
// the console does (her/apps/console/src/signin.ts), but in the main process: this computer's
// hermeshd catches the browser's redirect on 127.0.0.1 (the console's device-side routes,
// internal/owner/her_console.go), her on the Her Server makes the authorization request and
// keeps the PKCE verifier, the browser opens it, and what the redirect carried goes back to her,
// who exchanges it and keeps the tokens there. Nothing of it stays here.

/** How a sign-in ended, for the page: her's words and the account, never a token. */
export type ChatGptSignIn = { state: "connected" | "failed" | "cancelled"; account?: string; message?: string };

/** What the browser brought back to the catcher. */
type Caught = { code: string; state: string; client_id?: string } | { error: string; error_description?: string };

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

const ended = (s: ChatGptStatus): ChatGptSignIn => ({
  state: s.state === "connected" ? "connected" : s.state === "cancelled" ? "cancelled" : "failed",
  account: s.account ?? undefined,
  message: s.message ?? undefined,
});

/**
 * Signs in to ChatGPT for the environment: resolves once her has the tokens, or with why not.
 * The provider's refusal, a failure on the way, or `signal` cancels her's side too.
 */
export async function signInChatGpt(d: SignInDeps): Promise<ChatGptSignIn> {
  let login: string | undefined;
  try {
    const catcher = await json<{ id: string; redirect_uri: string }>(
      await d.fetch(`${d.base}signin`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: "/auth/callback" }), signal: d.signal }),
    );
    const started = await d.call<ChatGptStatus>("chatgpt/login", { redirect_uri: catcher.redirect_uri });
    if (!started.authorize_url || !started.login_id) return { state: "failed", message: started.message ?? undefined };
    login = started.login_id;
    d.signal.throwIfAborted();
    await d.open(started.authorize_url);
    const got = await caught(d, catcher.id);
    if ("error" in got) {
      await d.call("chatgpt/cancel", { login_id: login }).catch(() => {});
      return { state: "failed", message: got.error_description || got.error };
    }
    // ChatGPT names the client it registered in the redirect of a first sign-in.
    return ended(await d.call<ChatGptStatus>("chatgpt/complete", { login_id: login, code: got.code, state: got.state, client_id: got.client_id }));
  } catch (e) {
    if (login) await d.call("chatgpt/cancel", { login_id: login }).catch(() => {});
    if (d.signal.aborted) return { state: "cancelled" };
    throw e;
  }
}
