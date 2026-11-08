import assert from "node:assert/strict";
import { test } from "node:test";
import { catchRedirect, caught, signCommandConnector, upload } from "./routes.ts";
import { signInRemotely } from "./signin.ts";

// The console's own routes, against fetch as the test plays hermeshd (the machine's for
// upload, the owner's device's for signin and command-connector).

type Asked = { url: string; method: string; headers: Record<string, string>; body: unknown };

/** fetch as the test plays it: each request is kept and answered by `reply`. */
function fakeFetch(reply: (asked: Asked, n: number) => Response | Promise<Response>) {
  const asked: Asked[] = [];
  globalThis.fetch = async (url, init) => {
    init?.signal?.throwIfAborted();
    const a: Asked = { url: String(url), method: init?.method ?? "GET", headers: (init?.headers ?? {}) as Record<string, string>, body: init?.body };
    asked.push(a);
    return reply(a, asked.length);
  };
  return asked;
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const text = (body: string, status: number) => new Response(body + "\n", { status, headers: { "Content-Type": "text/plain" } });
/** "Still pending", after a while, as the device's catcher answers a poll. */
const pending = () => new Promise<Response>((resolve) => setTimeout(() => resolve(json({ pending: true })), 5));

test("upload: the bytes go up beside the page, the name percent-encoded; the answer is the path on the machine", async () => {
  const asked = fakeFetch(() => json({ path: "/home/me/.her/uploads/console/0a1b-周报 skill.zip" }));
  const file = new Blob(["PK"]);
  assert.equal(await upload(file, "周报 skill.zip"), "/home/me/.her/uploads/console/0a1b-周报 skill.zip");
  assert.equal(asked[0].url, "./upload");
  assert.equal(asked[0].method, "POST");
  assert.equal(asked[0].headers["Her-File-Name"], "%E5%91%A8%E6%8A%A5%20skill.zip");
  assert.equal(asked[0].body, file);
  fakeFetch(() => text("The file is larger than 50 MiB.", 413));
  await assert.rejects(upload(file, "big.zip"), /^Error: The file is larger than 50 MiB\.$/);
});

test("signin: a catcher on the device, then what it caught, asked again while pending and after a lost request", async () => {
  let asked = fakeFetch(() => json({ id: "s1", redirect_uri: "http://127.0.0.1:53682/callback" }));
  assert.deepEqual(await catchRedirect("/callback"), { id: "s1", redirect_uri: "http://127.0.0.1:53682/callback" });
  assert.deepEqual([asked[0].url, asked[0].method, JSON.parse(String(asked[0].body))], ["./signin", "POST", { path: "/callback" }]);
  fakeFetch(() => json({ error: "a sign-in needs the path its redirect goes to, such as /auth/callback" }, 400));
  await assert.rejects(catchRedirect("callback"), /a sign-in needs the path/);

  asked = fakeFetch((_, n) => {
    if (n === 2) throw new TypeError("network");
    return n < 4 ? pending() : json({ code: "c0de", state: "st", client_id: "app_1" });
  });
  assert.deepEqual(await caught("s/1"), { code: "c0de", state: "st", client_id: "app_1" });
  assert.equal(asked.length, 4);
  assert.equal(asked[0].url, "./signin/s%2F1");
  fakeFetch(() => json({ error: "no such sign-in" }, 404));
  await assert.rejects(caught("gone"), /^Error: no such sign-in$/);
  fakeFetch(pending);
  const stop = new AbortController();
  setTimeout(() => stop.abort(), 20);
  await assert.rejects(caught("s2", stop.signal));
});

test("command-connector: the connector as the device signs it; a refusal is its text", async () => {
  const asked = fakeFetch(() => json({}));
  await signCommandConnector({ name: "files", command: "npx", args: ["-y", "@acme/files"], env: { TOKEN: "${credential:mcp.files.TOKEN}" } });
  assert.deepEqual([asked[0].url, asked[0].method, JSON.parse(String(asked[0].body))], [
    "./command-connector",
    "POST",
    { name: "files", command: "npx", args: ["-y", "@acme/files"], env: { TOKEN: "${credential:mcp.files.TOKEN}" } },
  ]);
  fakeFetch(() => text("the owner did not approve", 403));
  await assert.rejects(signCommandConnector({ name: "files", command: "x", args: [], env: {} }), /^Error: the owner did not approve$/);
});

/** her's side of a sign-in, as the test plays it. */
function her() {
  const calls: string[] = [];
  return {
    calls,
    start: async (redirect_uri: string) => (calls.push(`start ${redirect_uri}`), { authorize_url: "https://auth.example.com/authorize?x=1", login_id: "L1" }),
    complete: async (login_id: string, g: { code: string; state: string; client_id?: string; iss?: string }) => (calls.push(`complete ${login_id} ${g.code} ${g.state} ${g.client_id ?? "-"} ${g.iss ?? "-"}`), "done"),
    cancel: async (login_id: string) => calls.push(`cancel ${login_id}`),
  };
}

test("a sign-in that ends on the device: her starts it for the catcher's address, the page opens it, what came back completes it", async () => {
  fakeFetch((a) => (a.method === "POST" ? json({ id: "s1", redirect_uri: "http://127.0.0.1:53682/auth/callback" }) : json({ code: "c0de", state: "st", client_id: "app_1", iss: "https://auth.example.com" })));
  const h = her();
  const opened: string[] = [];
  const done = await signInRemotely({ path: "/auth/callback", start: h.start, open: (url) => opened.push(url), complete: h.complete, cancel: h.cancel });
  assert.equal(done, "done");
  assert.deepEqual(opened, ["https://auth.example.com/authorize?x=1"]);
  assert.deepEqual(h.calls, ["start http://127.0.0.1:53682/auth/callback", "complete L1 c0de st app_1 https://auth.example.com"]);
});

test("a sign-in the provider refused, or that was stopped, is cancelled on her's side too", async () => {
  fakeFetch((a) => (a.method === "POST" ? json({ id: "s1", redirect_uri: "http://127.0.0.1:53682/callback" }) : json({ error: "access_denied", error_description: "The person said no" })));
  let h = her();
  await assert.rejects(signInRemotely({ path: "/callback", start: h.start, open: () => {}, complete: h.complete, cancel: h.cancel }), /^Error: The person said no$/);
  assert.deepEqual(h.calls.slice(1), ["cancel L1"]);

  fakeFetch((a) => (a.method === "POST" ? json({ id: "s1", redirect_uri: "http://127.0.0.1:53682/callback" }) : pending()));
  h = her();
  const stop = new AbortController();
  const signing = signInRemotely({ path: "/callback", start: h.start, open: () => setTimeout(() => stop.abort(), 20), complete: h.complete, cancel: h.cancel, signal: stop.signal });
  await assert.rejects(signing);
  assert.deepEqual(h.calls.slice(1), ["cancel L1"]);
});
