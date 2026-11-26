import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { cli, fixture, launch, snap, sessionList } from "./app";

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
  const list = (await sessionList(page));
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
  await expect(list.getByText("正在读取更早的任务…")).toBeAttached();
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
  await expect(list.getByText("没有更早的任务了")).toBeVisible();
  await expect(list.getByText("正在读取更早的任务…")).toHaveCount(0);
  await snap(page, "sessions-all");
  await app.close();
});

test("a session started by an agent shows up at once, and so do its changes", async () => {
  const { app, page } = await launch();
  const list = (await sessionList(page));
  await expect(list).toContainText("选颜色");
  // The list is pushed to, not polled (that took up to 3 s).
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "新来的", "--prompt", "shell sleep 3\nsay 好了");
  const row = list.getByRole("listitem").filter({ hasText: "新来的" });
  await expect(row).toBeVisible({ timeout: 1500 });
  // In progress with a ring while it runs (docs/her-team.md §16), then done, with no mark, once it finishes.
  await expect(list.getByRole("group", { name: "进行中" })).toContainText("新来的");
  await expect(row.getByRole("img", { name: "运行中" })).toBeVisible();
  await expect(list.getByRole("group", { name: "已结束" })).toContainText("新来的", { timeout: 30_000 });
  await expect(row.locator(".smeta [role=img]")).toHaveCount(0);
  await app.close();
});

test("a failed or crashed session resumed leaves the error state as soon as it runs", async () => {
  const { app, page } = await launch();
  const list = await sessionList(page);
  const attention = list.getByRole("group", { name: "需要处理" });
  const active = list.getByRole("group", { name: "进行中" });
  const done = list.getByRole("group", { name: "已结束" });

  for (const [title, errMark] of [["限流", "出错"], ["崩溃", "已崩溃"]] as const) {
    const row = list.getByRole("listitem").filter({ hasText: title });
    await expect(attention).toContainText(title);
    await expect(row.getByRole("img", { name: errMark })).toBeVisible();

    await cli("session", "send", fixture.sessions[title], "--message", "shell sleep 3\nsay 接着跑完");
    await expect(active).toContainText(title, { timeout: 5_000 });
    await expect(attention).not.toContainText(title);
    await expect(row.getByRole("img", { name: "运行中" })).toBeVisible();
    await expect(row.getByRole("img", { name: errMark })).toHaveCount(0);

    await expect(done).toContainText(title, { timeout: 30_000 });
    await expect(row.locator(".smeta [role=img]")).toHaveCount(0);
  }
  await app.close();
});

test("grouped by project, long project groups fold to five while keeping active, selected and later projects in view", async () => {
  test.setTimeout(180_000);
  const zetaDir = fs.mkdtempSync(path.join(os.tmpdir(), "hmzeta-"));
  const { app, page } = await launch();
  const existingBatch = await page.evaluate(async () => {
    const r = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListSessions", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ limit: 50 }) });
    return ((await r.json()).sessions as { title: string }[]).some((s) => s.title === "批量 10");
  });
  if (!existingBatch) await startSessions(18);
  await page.evaluate(async (dir) => {
    const st = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/GetStatus", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" }).then((r) => r.json());
    await fetch("app://hermesh/api/hermesh.local.v1.LocalService/AddProject", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ machineId: st.localMachineId, projectId: "zeta", path: dir, workspace: "WORKSPACE_IN_PLACE" }),
    });
  }, zetaDir);
  await cli("session", "start", "--project", "zeta", "--agent", "gemini", "--title", "第二个项目的任务", "--prompt", "say 好", "--wait");

  const list = await sessionList(page);
  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /分组/ }).hover();
  await page.getByRole("menu", { name: "分组" }).getByRole("menuitemradio", { name: "按项目" }).click();

  const demo = list.getByRole("group", { name: "demo" });
  const zeta = list.getByRole("group", { name: "zeta" });
  await expect(zeta).toContainText("第二个项目的任务");
  // Folding demo keeps later projects visible in the first viewport without scrolling past 50+ rows.
  await expect(zeta).toBeInViewport();

  // Waiting ("选颜色") and running ("跑很久") in demo stay visible even when older than the 5 newest idle tasks.
  await expect(demo).toContainText("选颜色");
  await expect(demo).toContainText("跑很久");
  const initialVisible = await demo.getByRole("listitem").count();
  expect(initialVisible).toBeGreaterThanOrEqual(7);
  expect(initialVisible).toBeLessThanOrEqual(9);

  const more = demo.getByRole("button", { name: /^显示更多（\d+）$/ });
  await expect(more).toBeVisible();
  const hiddenBefore = Number((await more.innerText()).match(/\d+/)![0]);
  expect(hiddenBefore).toBeGreaterThan(12);

  // Clicking "显示更多" reveals 10 more rows in demo.
  await more.click();
  await expect(demo.getByRole("listitem")).toHaveCount(initialVisible + 10);
  await expect(demo.getByRole("button", { name: `显示更多（${hiddenBefore - 10}）` })).toBeVisible();

  // Collapsing and reopening demo resets its fold back to 5 (+ pinned active/attention rows) without draining extra pages.
  const demoHeading = demo.getByRole("button", { name: /^demo$/ });
  await demoHeading.click();
  await expect(demo.getByRole("listitem")).toHaveCount(0);
  await demoHeading.click();
  await expect(demo.getByRole("listitem")).toHaveCount(initialVisible);
  await expect(demo.getByRole("button", { name: `显示更多（${hiddenBefore}）` })).toBeVisible();

  // Selecting an older folded task via Search pins it into view inside demo without unfolding the whole list.
  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  const palette = page.getByRole("dialog", { name: "搜索任务" });
  await palette.getByRole("textbox", { name: "搜索任务" }).fill("批量 10");
  await palette.getByRole("listbox", { name: "搜索结果" }).getByRole("option", { name: /批量 10/ }).click();
  await expect(demo).toContainText("批量 10");
  await expect(demo.getByRole("listitem")).toHaveCount(initialVisible + 1);

  // If older pages remain on the server, the foot offers on-demand loading rather than looping automatically.
  const loadOlderBtn = list.getByRole("button", { name: "读取更早的任务" });
  if (await loadOlderBtn.count()) {
    await loadOlderBtn.click();
    await expect(list.getByText("没有更早的任务了")).toBeVisible();
  }

  await app.close();
});
