import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launch, snap } from "./app";

// The shell around the UI: the page reaches hermeshd only through the main
// process, which never lets it near signing.
test("the page reads sessions from hermeshd through the main process", async () => {
  const { app, page } = await launch();
  await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  await app.close();
});

test("signing requests are not reachable from the page", async () => {
  const { app, page } = await launch();
  for (const method of ["WatchSigningRequests", "CompleteSigningRequest"]) {
    const status = await page.evaluate(
      (m) => fetch(`app://hermesh/api/hermesh.local.v1.LocalService/${m}`, { method: "POST", headers: { "content-type": "application/json" }, body: "{}" }).then((r) => r.status),
      method,
    );
    expect(status, method).toBe(403);
  }
  const other = await page.evaluate(() =>
    fetch("app://hermesh/api/some.other.Service/Method", { method: "POST", body: "{}" }).then((r) => r.status),
  );
  expect(other).toBe(403);
  await app.close();
});

test("the page has no Node and cannot leave the app", async () => {
  const { app, page } = await launch();
  expect(await page.evaluate(() => typeof (globalThis as { require?: unknown }).require)).toBe("undefined");
  expect(await page.evaluate(() => typeof (globalThis as { process?: unknown }).process)).toBe("undefined");
  await page.evaluate(() => (location.href = "https://example.com"));
  await page.waitForTimeout(500);
  expect(page.url()).toMatch(/^app:\/\/hermesh\//);
  await app.close();
});

test("a cancelled stream releases the daemon connection", async () => {
  const { app, page } = await launch();
  const open = () => app.evaluate(() => (globalThis as unknown as { hermeshTest: { openUpstream(): number } }).hermeshTest.openUpstream());
  // The window keeps one stream open: its watch on the sessions.
  await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  await expect.poll(open).toBe(1);
  const id = (await import("./app")).fixture.sessions["跑很久"];
  await page.evaluate(async (sessionId) => {
    const ctl = new AbortController();
    (globalThis as { abortStream?: () => void }).abortStream = () => ctl.abort();
    const body = JSON.stringify({ sessionId, follow: true });
    const env = new Uint8Array(5 + body.length);
    new DataView(env.buffer).setUint32(1, body.length);
    env.set(new TextEncoder().encode(body), 5);
    fetch("app://hermesh/api/hermesh.local.v1.LocalService/StreamSessionEvents", {
      method: "POST",
      headers: { "content-type": "application/connect+json", "connect-protocol-version": "1" },
      body: env,
      signal: ctl.signal,
    }).then((r) => r.body?.getReader().read()).catch(() => {});
  }, id);
  await expect.poll(open).toBe(2);
  await page.evaluate(() => (globalThis as { abortStream?: () => void }).abortStream?.());
  await expect.poll(open).toBe(1);
  await app.close();
});

test("an idle window makes no calls to hermeshd: it follows one Watch", async () => {
  const { app, page } = await launch();
  const calls = () => app.evaluate(() => (globalThis as unknown as { hermeshTest: { forwardedCalls(): Record<string, number> } }).hermeshTest.forwardedCalls());
  await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  await page.waitForTimeout(2000); // what opening the window reads (the overview's usage)
  const before = await calls();
  expect(before.Watch).toBe(1);
  await page.waitForTimeout(8000);
  expect(await calls()).toEqual(before);
  await app.close();
});

test("the window opens at the size it had last time, loading in the middle until hermeshd answers", async () => {
  const width = (app: Awaited<ReturnType<typeof launch>>["app"]) => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  const first = await launch(undefined, { profile });
  await expect(first.page.getByRole("complementary", { name: "边栏" })).toBeVisible();
  await expect.poll(() => width(first.app)).toBeGreaterThan(900);
  await first.app.close();

  const { app, page } = await launch(path.join(fs.mkdtempSync(path.join(os.tmpdir(), "hmsock-")), "none.sock"), { profile });
  await expect(page.getByText("正在连接后台服务…")).toBeVisible();
  expect(await width(app)).toBeGreaterThan(900);
  await snap(page, "start-03-main-size");
  await app.close();
});
