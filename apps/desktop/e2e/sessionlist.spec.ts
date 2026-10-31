import { expect, test } from "@playwright/test";
import { cli, launch, snap } from "./app";

// The session list as a chat app's: the newest page at once, older ones as
// the list scrolls to its end, and what changes shows up as it happens.

/** Starts n sessions that finish at once (8 at a time); returns their titles, oldest first. */
async function startSessions(n: number): Promise<string[]> {
  const titles = Array.from({ length: n }, (_, i) => `批量 ${String(i + 1).padStart(2, "0")}`);
  for (let i = 0; i < n; i += 8) {
    await Promise.all(
      titles.slice(i, i + 8).map((t) => cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", t, "--prompt", "say 好", "--wait")),
    );
  }
  return titles;
}

test("the sidebar shows the newest page, and scrolling to its end loads older sessions", async () => {
  test.setTimeout(240_000);
  // 55 more: with the fixture's six, more than a page (50). The newest
  // page and every session that needs the owner or runs come first.
  const titles = await startSessions(55);
  const { app, page } = await launch();
  const list = page.getByRole("list", { name: "会话" });
  const rows = list.getByRole("listitem");
  await expect(list).toContainText(titles[54]);
  await expect(list).toContainText("选颜色"); // a question, however old
  await expect(list).toContainText("跑很久"); // running
  // 50 newest, plus the question, the two failures and the running one
  // (older); the archived one is not shown.
  await expect(rows).toHaveCount(54);
  await expect(list).not.toContainText("写 hello");
  // The six left out are the oldest by when they last changed: "写 hello" and
  // some of the first batches. Which ones depends on how those batches
  // finished (hermeshd stores whole seconds, and a slow runner finishes a
  // batch late): the sidebar shows exactly hermeshd's first page.
  const first = await page.evaluate(async () => {
    const r = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListSessions", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ limit: 50 }) });
    return ((await r.json()).sessions as { title: string }[]).map((s) => s.title);
  });
  const shown = await list.getByText(/^批量 \d\d$/).allTextContents();
  expect(shown.sort()).toEqual(first.filter((t) => /^批量 \d\d$/.test(t)).sort());
  await expect(list.getByText("正在读取更早的会话…")).toBeAttached();
  await snap(page, "sessions-first-page");

  await list.hover();
  await page.mouse.wheel(0, 20_000);
  await expect(list).toContainText("写 hello");
  await expect(list).toContainText(titles[0]);
  // No gaps: every session is there once the older page has come.
  await expect.poll(async () => {
    const shown = await rows.allTextContents();
    return titles.filter((t) => !shown.some((r) => r.startsWith(t)));
  }, { message: "sessions missing after the older page" }).toEqual([]);
  await expect(rows).toHaveCount(60);
  await expect(list.getByText("没有更早的会话了")).toBeVisible();
  await expect(list.getByText("正在读取更早的会话…")).toHaveCount(0);
  await snap(page, "sessions-all");
  await app.close();
});

test("a session started by an agent shows up at once, and so do its changes", async () => {
  const { app, page } = await launch();
  const list = page.getByRole("list", { name: "会话" });
  await expect(list).toContainText("选颜色");
  // The list is pushed to, not polled (that took up to 3 s).
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "新来的", "--prompt", "shell sleep 3\nsay 好了");
  const row = list.getByRole("listitem").filter({ hasText: "新来的" });
  await expect(row).toBeVisible({ timeout: 1500 });
  await expect(list.getByRole("group", { name: "进行中" })).toContainText("新来的");
  // It finishes: from 进行中 to 已结束.
  await expect(list.getByRole("group", { name: "已结束" })).toContainText("新来的", { timeout: 30_000 });
  await app.close();
});
