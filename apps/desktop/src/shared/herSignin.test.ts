import assert from "node:assert/strict";
import { test } from "node:test";
import { connectorPage, signInChatGpt, signInConnector, type SignInDeps } from "./herSignin.ts";

const base = "http://127.0.0.1:53111/her/m_1/4f2a/";
const redirect = "http://127.0.0.1:53999/auth/callback";

/**
 * This computer's hermeshd and her on the environment, as a test plays them: the catcher answers
 * each poll with the next of `polls` (a thrown error is a request that failed on the way), her
 * answers each method with `answers`; what was asked and opened is kept.
 */
function world(polls: (object | Error)[], answers: Record<string, unknown> = {}) {
  const asked: { method: string; url: string; body?: string }[] = [];
  const calls: { method: string; params: unknown }[] = [];
  const opened: string[] = [];
  const stop = new AbortController();
  const fetch = (async (url: string, init?: RequestInit) => {
    asked.push({ method: init?.method ?? "GET", url, body: init?.body as string | undefined });
    if (init?.method === "POST") return Response.json({ id: "c1", redirect_uri: redirect });
    const next = polls.shift();
    if (next === undefined) {
      // Nothing more: the poll waits, as hermeshd's does, until the sign-in is stopped.
      return new Promise<Response>((_, reject) => init?.signal?.addEventListener("abort", () => reject(init.signal!.reason)));
    }
    if (next instanceof Error) throw next;
    return Response.json(next);
  }) as typeof globalThis.fetch;
  const call = async <T>(method: string, params: unknown): Promise<T> => {
    calls.push({ method, params });
    const a = answers[method] ?? {};
    if (a instanceof Error) throw a;
    return a as T;
  };
  const deps: SignInDeps = { base, fetch, call, open: async (url) => void opened.push(url), signal: stop.signal, retry: 1 };
  return { deps, asked, calls, opened, stop };
}

const started = { state: "pending", login_id: "L1", authorize_url: "https://auth.openai.com/api/accounts/authorize?state=s1" };

test("a sign-in: the catcher here, her's authorization page in the browser, what came back to her", async () => {
  const w = world([{ pending: true }, new TypeError("fetch failed"), { code: "c0de", state: "s1", client_id: "issued" }], {
    "chatgpt/login": started,
    "chatgpt/complete": { state: "connected", account: "me@example.com", message: null },
  });
  assert.deepEqual(await signInChatGpt(w.deps), { state: "connected", account: "me@example.com", message: undefined });
  assert.deepEqual(w.asked[0], { method: "POST", url: `${base}signin`, body: JSON.stringify({ path: "/auth/callback" }) });
  assert.deepEqual(
    w.asked.slice(1).map((a) => `${a.method} ${a.url}`),
    [`GET ${base}signin/c1`, `GET ${base}signin/c1`, `GET ${base}signin/c1`],
  );
  assert.deepEqual(w.opened, [started.authorize_url]);
  assert.deepEqual(w.calls, [
    { method: "chatgpt/login", params: { redirect_uri: redirect } },
    { method: "chatgpt/complete", params: { login_id: "L1", code: "c0de", state: "s1", client_id: "issued" } },
  ]);
});

test("ChatGPT refusing, or her failing to finish, is said in her words; a refusal cancels her's side", async () => {
  const refused = world([{ error: "access_denied", error_description: "The user denied access" }], { "chatgpt/login": started });
  assert.deepEqual(await signInChatGpt(refused.deps), { state: "failed", message: "The user denied access" });
  assert.deepEqual(refused.calls.map((c) => c.method), ["chatgpt/login", "chatgpt/cancel"]);
  assert.deepEqual(refused.calls[1].params, { login_id: "L1" });

  const bare = world([{ error: "access_denied" }], { "chatgpt/login": started });
  assert.deepEqual(await signInChatGpt(bare.deps), { state: "failed", message: "access_denied" });

  const unfinished = world([{ code: "c0de", state: "s1" }], {
    "chatgpt/login": started,
    "chatgpt/complete": { state: "failed", message: "No models are available for this ChatGPT account" },
  });
  assert.deepEqual(await signInChatGpt(unfinished.deps), { state: "failed", account: undefined, message: "No models are available for this ChatGPT account" });

  // her could not start one: nothing opens.
  const busy = world([], { "chatgpt/login": { state: "pending", message: "Another ChatGPT sign-in is waiting; cancel it first" } });
  assert.deepEqual(await signInChatGpt(busy.deps), { state: "failed", message: "Another ChatGPT sign-in is waiting; cancel it first" });
  assert.deepEqual(busy.opened, []);
});

test("stopped while the browser is out: cancelled, here and on her's side", async () => {
  const w = world([{ pending: true }], { "chatgpt/login": started });
  const done = signInChatGpt(w.deps);
  while (w.asked.length < 3) await new Promise((r) => setTimeout(r, 1));
  w.stop.abort();
  assert.deepEqual(await done, { state: "cancelled" });
  assert.deepEqual(w.calls.map((c) => c.method), ["chatgpt/login", "chatgpt/cancel"]);
});

test("a failure on the way cancels her's side and is thrown, as is the catcher's refusal", async () => {
  const w = world([], { "chatgpt/login": started });
  w.deps.open = async () => {
    throw new Error("not an https page");
  };
  await assert.rejects(signInChatGpt(w.deps), /not an https page/);
  assert.deepEqual(w.calls.map((c) => c.method), ["chatgpt/login", "chatgpt/cancel"]);

  const refusing = world([]);
  refusing.deps.fetch = (async () => Response.json({ error: "a sign-in needs the path its redirect goes to" }, { status: 400 })) as typeof fetch;
  await assert.rejects(signInChatGpt(refusing.deps), /a sign-in needs the path/);
  assert.deepEqual(refusing.calls, []);
});

const mcpStarted = { authorize_url: "https://mcp.notion.com/authorize?state=s2", login_id: "M1", client: "registered" };

test("a connector's sign-in: her's mcp/login with the catcher's address, the page in the browser, what came back to her", async () => {
  const w = world([{ pending: true }, { code: "c0de", state: "s2", iss: "https://mcp.notion.com" }], { "mcp/login": mcpStarted });
  assert.deepEqual(await signInConnector(w.deps, "notion"), { state: "connected" });
  assert.deepEqual(w.asked[0], { method: "POST", url: `${base}signin`, body: JSON.stringify({ path: "/callback" }) });
  assert.deepEqual(w.opened, [mcpStarted.authorize_url]);
  assert.deepEqual(w.calls, [
    { method: "mcp/login", params: { name: "notion", redirect_uri: redirect } },
    { method: "mcp/login_complete", params: { login_id: "M1", code: "c0de", state: "s2", iss: "https://mcp.notion.com" } },
  ]);
});

test("a connector's sign-in refused, stopped, or failing on the way cancels it on her's side by the connector's name", async () => {
  const refused = world([{ error: "access_denied", error_description: "The user denied access" }], { "mcp/login": mcpStarted });
  assert.deepEqual(await signInConnector(refused.deps, "notion"), { state: "failed", message: "The user denied access" });
  assert.deepEqual(refused.calls.slice(1), [{ method: "mcp/login_cancel", params: { name: "notion" } }]);

  const stopped = world([{ pending: true }], { "mcp/login": mcpStarted });
  const done = signInConnector(stopped.deps, "notion");
  while (stopped.asked.length < 3) await new Promise((r) => setTimeout(r, 1));
  stopped.stop.abort();
  assert.deepEqual(await done, { state: "cancelled" });
  assert.deepEqual(stopped.calls.map((c) => c.method), ["mcp/login", "mcp/login_cancel"]);

  const unfinished = world([{ code: "c0de", state: "s2" }], { "mcp/login": mcpStarted, "mcp/login_complete": new Error("the state is not this sign-in's") });
  await assert.rejects(signInConnector(unfinished.deps, "notion"), /not this sign-in's/);
  assert.deepEqual(unfinished.calls.map((c) => c.method), ["mcp/login", "mcp/login_complete", "mcp/login_cancel"]);

  // her refusing to start one: nothing opens, nothing to cancel.
  const unknown = world([], { "mcp/login": new Error("no connector named notion") });
  await assert.rejects(signInConnector(unknown.deps, "notion"), /no connector named notion/);
  assert.deepEqual(unknown.opened, []);
  assert.deepEqual(unknown.calls.map((c) => c.method), ["mcp/login"]);
});

test("a connector's sign-in page opens when it is https, or http on this machine's loopback", () => {
  assert.ok(connectorPage("https://mcp.notion.com/authorize?x=1"));
  assert.ok(connectorPage("http://127.0.0.1:8123/authorize"));
  assert.ok(connectorPage("http://localhost:8123/authorize"));
  assert.ok(connectorPage("http://[::1]:8123/authorize"));
  assert.ok(!connectorPage("http://mcp.notion.com/authorize"));
  assert.ok(!connectorPage("file:///etc/passwd"));
  assert.ok(!connectorPage("not a url"));
});
