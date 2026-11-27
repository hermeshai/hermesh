import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { cli, fixture, gitRepo, launch, snap, sessionList } from "./app";

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

test("only pushed or merged sessions show a sidebar git mark, and hovering shows a read-only git status card", async () => {
  test.setTimeout(120_000);
  const repo = gitRepo("gitdemo");
  const origin = path.join(fs.realpathSync.native(fs.mkdtempSync(path.join(os.tmpdir(), "hmorig-"))), "origin.git");
  const gitEnv = { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" };
  execFileSync("git", ["init", "-q", "--bare", "-b", "main", origin], { env: gitEnv });
  execFileSync("git", ["remote", "add", "origin", origin], { cwd: repo, env: gitEnv });
  execFileSync("git", ["push", "-q", "-u", "origin", "main"], { cwd: repo, env: gitEnv });

  const { app, page } = await launch();
  await page.evaluate(async (dir) => {
    const st = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/GetStatus", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" }).then((r) => r.json());
    await fetch("app://hermesh/api/hermesh.local.v1.LocalService/AddProject", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ machineId: st.localMachineId, projectId: "gitdemo", path: dir, workspace: "WORKSPACE_WORKTREE" }),
    });
  }, repo);

  // Pure Q&A / review session with no code changes: hover card omits the noisy .shover-git box.
  await cli("session", "start", "--project", "gitdemo", "--agent", "gemini", "--title", "纯审查无改动", "--prompt", "say 只读复核完成", "--wait");
  const list = await sessionList(page);
  const qaRow = list.getByRole("listitem").filter({ hasText: "纯审查无改动" });
  await expect(qaRow).toBeVisible();
  await qaRow.hover();
  const card = page.getByTestId("session-hover-card");
  await expect(card).toBeVisible();
  await expect(card).toContainText("纯审查无改动");
  await expect(card.locator(".shover-git")).toHaveCount(0);
  await page.locator(".side-h").hover();
  await expect(card).toHaveCount(0);

  const started = JSON.parse(
    await cli("session", "start", "--project", "gitdemo", "--agent", "gemini", "--title", "实现分支感知", "--prompt", "write feature.ts export const ok = true;\nsay 改好了", "--wait", "-o", "json"),
  ).session as { session_id: string };

  const row = list.getByRole("listitem").filter({ hasText: "实现分支感知" });
  await expect(row).toBeVisible();
  // Dirty worktree: no sidebar row icon, hover card shows uncommitted status and has zero action buttons.
  await expect(row.locator(".smeta [role=img]")).toHaveCount(0);
  await row.hover();
  await expect(card).toBeVisible();
  await expect(card).toContainText("实现分支感知");
  await expect(card).toContainText("1 个文件改动未提交");
  await expect(card).toContainText("+1");
  await expect(card.locator("button")).toHaveCount(0);
  await page.locator(".side-h").hover();
  await expect(card).toHaveCount(0);

  // Local commit (unpushed): sidebar row stays unmarked, hover card shows ahead commit summary.
  await cli("session", "send", started.session_id, "--message", "shell git -c user.name=t -c user.email=t@t add -A && git -c user.name=t -c user.email=t@t commit -m 'feat: add status perception'\nsay 已提交", "--wait");
  await row.hover();
  await expect(card).toContainText("1 个本地提交待推送");
  await expect(card).toContainText("feat: add status perception");
  await page.locator(".side-h").hover();
  await expect(row.locator(".smeta [role=img]")).toHaveCount(0);

  const info = await page.evaluate(async (sessionId) => {
    const r = await fetch("app://hermesh/api/hermesh.session.v1.SessionService/GetSession", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ sessionId }),
    });
    const ws = (await r.json()).projection.session.workspaceStrategy;
    return { worktreePath: ws.worktreePath as string, branch: ws.branch as string };
  }, started.session_id);

  // Push externally (2 commits total) and hover row: on-demand single-session refresh updates card & row mark.
  fs.writeFileSync(path.join(info.worktreePath, "feature2.ts"), "export const v2 = 2;\n");
  execFileSync("git", ["add", "feature2.ts"], { cwd: info.worktreePath, env: gitEnv });
  execFileSync("git", ["commit", "-q", "-m", "feat: second commit"], { cwd: info.worktreePath, env: gitEnv });
  execFileSync("git", ["push", "-q", "origin", info.branch], { cwd: info.worktreePath, env: gitEnv });
  await page.waitForTimeout(5100);
  await row.hover();
  await expect(card).toContainText("已提交并推送");
  await expect(card).toContainText("feat: second commit");
  await page.locator(".side-h").hover();
  await expect(row.getByRole("img", { name: "已推送" })).toBeVisible();

  // Squash-merge on remote origin/main via a separate clone (local repo never fetches),
  // then hover the row: on-demand remote sync flips both card and row mark to merged!
  const ghClone = path.join(fs.realpathSync.native(fs.mkdtempSync(path.join(os.tmpdir(), "hmgh-"))), "clone");
  execFileSync("git", ["clone", "-q", origin, ghClone], { env: gitEnv });
  execFileSync("git", ["fetch", "-q", "origin", info.branch], { cwd: ghClone, env: gitEnv });
  execFileSync("git", ["merge", "-q", "--squash", "FETCH_HEAD"], { cwd: ghClone, env: gitEnv });
  execFileSync("git", ["commit", "-q", "-m", "feat: squash merge (#1)"], { cwd: ghClone, env: gitEnv });
  execFileSync("git", ["push", "-q", "origin", "main"], { cwd: ghClone, env: gitEnv });

  await page.waitForTimeout(10_200);
  await row.hover();
  await expect(card).toContainText("已合并至 main");
  await page.locator(".side-h").hover();
  await expect(row.getByRole("img", { name: "已合并" })).toBeVisible();

  await app.close();
});

test("concurrent active sessions stay in creation order without jumping, support drag reorder and reset, and pin to the top", async () => {
  test.setTimeout(120_000);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(fixture.socket, { profile });
  let list = await sessionList(page);

  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "并发任务 A", "--prompt", "shell sleep 3\nshell sleep 60\nsay A好");
  const active = () => list.getByRole("group", { name: "进行中" });
  await expect(active()).toContainText("并发任务 A");

  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "并发任务 B", "--prompt", "shell sleep 60\nsay B好");
  await expect(active()).toContainText("并发任务 B");

  const activeTitles = () => active().locator(".stitle").allInnerTexts();
  await expect.poll(activeTitles).toEqual(["并发任务 B", "并发任务 A", "跑很久"]);

  // Wait until "并发任务 A" finishes its first 3s tool step so its updatedAt overtakes "并发任务 B".
  await expect.poll(async () => {
    const r = await page.evaluate(async () => {
      const res = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListSessions", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ limit: 50 }),
      });
      return ((await res.json()).sessions as { title: string; updatedAt: string }[]).map((s) => [s.title, Date.parse(s.updatedAt)]);
    });
    const byTitle = Object.fromEntries(r);
    return (byTitle["并发任务 A"] ?? 0) > (byTitle["并发任务 B"] ?? Infinity);
  }, { timeout: 15_000 }).toBe(true);

  // Even after "并发任务 A" updated more recently, 进行中 stays rock-solid in creation order.
  expect(await activeTitles()).toEqual(["并发任务 B", "并发任务 A", "跑很久"]);

  // Drag "跑很久" above "并发任务 B" inside 进行中; the custom order persists across relaunch and resets from View Options.
  const dragRow = async (fromLocator: ReturnType<typeof page.locator>, toX: number, toY: number) => {
    const box = (await fromLocator.boundingBox())!;
    await page.mouse.move(box.x + 60, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + 60, box.y - 8, { steps: 3 });
    await page.mouse.move(toX, toY, { steps: 8 });
    await page.mouse.up();
  };

  const topBox = (await active().getByRole("listitem").first().boundingBox())!;
  await dragRow(active().getByRole("listitem").filter({ hasText: "跑很久" }), topBox.x + 60, topBox.y + 2);
  await expect.poll(activeTitles).toEqual(["跑很久", "并发任务 B", "并发任务 A"]);

  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  list = await sessionList(page);
  await expect.poll(activeTitles).toEqual(["跑很久", "并发任务 B", "并发任务 A"]);

  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /排序/ }).hover();
  await page.getByRole("menu", { name: "排序" }).getByRole("menuitem", { name: "恢复默认顺序" }).click();
  await expect.poll(activeTitles).toEqual(["并发任务 B", "并发任务 A", "跑很久"]);

  // Pinning via ⋯ moves a task into the top 置顶 group; dragging another task into 置顶 pins and orders it too.
  const rowA = active().getByRole("listitem").filter({ hasText: "并发任务 A" });
  await rowA.hover();
  await rowA.getByRole("button", { name: "更多操作" }).click();
  await page.getByRole("menu", { name: "更多操作" }).getByRole("menuitem", { name: "置顶" }).click();

  const pinned = list.getByRole("group", { name: "置顶" });
  await expect(pinned).toContainText("并发任务 A");
  await expect(active()).not.toContainText("并发任务 A");
  expect((await list.locator(".grp-h").allInnerTexts())[0]).toBe("置顶");

  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "刚跑完的", "--prompt", "say 好", "--wait");
  await expect(list.getByRole("group", { name: "已结束" })).toContainText("刚跑完的");

  const pinTopBox = (await pinned.getByRole("listitem").first().boundingBox())!;
  await dragRow(list.getByRole("listitem").filter({ hasText: "刚跑完的" }), pinTopBox.x + 60, pinTopBox.y + 2);
  await expect.poll(() => pinned.locator(".stitle").allInnerTexts()).toEqual(["刚跑完的", "并发任务 A"]);

  // Unpinning via ⋯ returns "刚跑完的" to 已结束; unpin "并发任务 A" too so later tests start with no pinned rows.
  const pinnedDone = pinned.getByRole("listitem").filter({ hasText: "刚跑完的" });
  await pinnedDone.hover();
  await pinnedDone.getByRole("button", { name: "更多操作" }).click();
  await page.getByRole("menu", { name: "更多操作" }).getByRole("menuitem", { name: "取消置顶" }).click();
  await expect(list.getByRole("group", { name: "已结束" })).toContainText("刚跑完的");

  const pinnedActive = pinned.getByRole("listitem").filter({ hasText: "并发任务 A" });
  await pinnedActive.hover();
  await pinnedActive.getByRole("button", { name: "更多操作" }).click();
  await page.getByRole("menu", { name: "更多操作" }).getByRole("menuitem", { name: "取消置顶" }).click();
  await expect(pinned).toHaveCount(0);

  await app.close();
});

test("drag-and-drop places sessions into any slot across done, attention, project, and pinned sections with clean sidebar-scoped visuals", async () => {
  test.setTimeout(150_000);
  // Seed 5 finished tasks so the top 5 of 已结束 are deterministic regardless of earlier tests: [完成 5, 完成 4, 完成 3, 完成 2, 完成 1].
  for (const title of ["完成 1", "完成 2", "完成 3", "完成 4", "完成 5"]) {
    await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--prompt", "say 好", "--wait");
  }

  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdrag-"));
  let { app, page } = await launch(fixture.socket, { profile });
  let list = await sessionList(page);

  const group = (name: string) => list.getByRole("group", { name });
  const titlesOf = (name: string) => group(name).locator(".stitle").allInnerTexts();
  const topDone = async (n = 5) => (await titlesOf("已结束")).slice(0, n);
  const rowIn = (gName: string, title: string) => group(gName).getByRole("listitem").filter({ hasText: title });

  const dragTo = async (from: ReturnType<typeof page.locator>, toY: number) => {
    const box = (await from.boundingBox())!;
    const x = box.x + 80;
    const startY = box.y + box.height / 2;
    await page.mouse.move(x, startY);
    await page.mouse.down();
    await page.mouse.move(x, startY + (toY < startY ? -8 : 8), { steps: 3 });
    await page.mouse.move(x, toY, { steps: 8 });
    await page.mouse.up();
  };

  await expect.poll(() => topDone()).toEqual(["完成 5", "完成 4", "完成 3", "完成 2", "完成 1"]);
  for (let i = (await titlesOf("需要处理")).length; i < 3; i++) {
    const title = `待处理 ${i + 1}`;
    await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--prompt", "fail rate limited", "--wait").catch(() => {});
    await expect(group("需要处理")).toContainText(title);
  }
  const initialAttention = await titlesOf("需要处理");
  expect(initialAttention).toHaveLength(3);

  // 1. Visual & geometry checks during drag on a selected row:
  //    - Starting a drag (spawning .pin-dropzone) causes 0px layout shift on existing rows.
  //    - .srow.sel.drag-ghost is opaque (alpha === 1) with no action buttons inside.
  //    - .drop-line stays strictly inside the 272px sidebar and sits between the two target rows.
  const targetSel = rowIn("已结束", "完成 3");
  await targetSel.click();
  await expect(targetSel).toHaveClass(/\bsel\b/);

  const sideBox = (await page.locator(".side").boundingBox())!;
  const d5Before = (await rowIn("已结束", "完成 5").boundingBox())!;
  const d4Before = (await rowIn("已结束", "完成 4").boundingBox())!;
  const d3Box = (await targetSel.boundingBox())!;

  await page.mouse.move(d3Box.x + 80, d3Box.y + d3Box.height / 2);
  await page.mouse.down();
  await page.mouse.move(d3Box.x + 80, d3Box.y + d3Box.height / 2 - 10, { steps: 3 });

  // Zero layout jump when .pin-dropzone appears!
  await expect(page.locator(".pin-dropzone")).toBeVisible();
  const d5During = (await rowIn("已结束", "完成 5").boundingBox())!;
  expect(Math.abs(d5During.y - d5Before.y)).toBeLessThanOrEqual(0.5);

  // Ghost is opaque and stripped of .rowacts.
  const ghostInfo = await page.locator(".srow.drag-ghost").evaluate((el) => {
    const bg = getComputedStyle(el).backgroundColor;
    const m = bg.match(/rgba?\(([^)]+)\)/);
    const parts = m ? m[1].split(",").map((s) => Number(s.trim())) : [];
    const alpha = parts.length === 4 ? parts[3] : 1;
    return { alpha, acts: el.querySelectorAll(".rowacts").length };
  });
  expect(ghostInfo).toEqual({ alpha: 1, acts: 0 });

  // Move between 完成 5 (index 0) and 完成 4 (index 1) -> drop-line appears inside sidebar between them.
  await page.mouse.move(d3Box.x + 80, d4Before.y + d4Before.height * 0.3, { steps: 6 });
  const lineBox = (await page.locator(".drop-line").boundingBox())!;
  expect(lineBox.x).toBeGreaterThanOrEqual(sideBox.x);
  expect(lineBox.x + lineBox.width).toBeLessThanOrEqual(sideBox.x + sideBox.width);
  expect(lineBox.y).toBeGreaterThanOrEqual(d5Before.y + d5Before.height - 3);
  expect(lineBox.y).toBeLessThanOrEqual(d4Before.y + 3);
  await snap(page, "drag-reorder-done");
  await expect(page.getByTestId("session-hover-card")).toHaveCount(0);

  // Pressing Escape cancels the active drag cleanly without changing order.
  await page.keyboard.press("Escape");
  await expect(page.locator(".srow.drag-ghost")).toHaveCount(0);
  await expect(page.locator(".drop-line")).toHaveCount(0);
  await page.mouse.up();
  expect(await topDone()).toEqual(["完成 5", "完成 4", "完成 3", "完成 2", "完成 1"]);

  // 2. Arbitrary slot reordering inside 已结束 (status:done):
  // (a) 5th -> Top (slot 0, without accidentally triggering pin-dropzone)
  const doneFirst = (await rowIn("已结束", "完成 5").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 1"), doneFirst.y + 6);
  await expect.poll(() => topDone()).toEqual(["完成 1", "完成 5", "完成 4", "完成 3", "完成 2"]);
  await expect(group("置顶")).toHaveCount(0);

  // (b) Top -> Middle (move "完成 1" between "完成 4" and "完成 3", i.e. onto "完成 4"'s body)
  const c4Box = (await rowIn("已结束", "完成 4").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 1"), c4Box.y + c4Box.height * 0.6);
  await expect.poll(() => topDone()).toEqual(["完成 5", "完成 4", "完成 1", "完成 3", "完成 2"]);

  // (c) Middle -> 5th (move "完成 4" right after "完成 2")
  const c2Box = (await rowIn("已结束", "完成 2").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 4"), c2Box.y + c2Box.height * 0.6);
  await expect.poll(() => topDone()).toEqual(["完成 5", "完成 1", "完成 3", "完成 2", "完成 4"]);

  // (d) Adjacent 1-step swap down ("完成 1" index 1 <-> "完成 3" index 2) and back up
  const c3Now = (await rowIn("已结束", "完成 3").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 1"), c3Now.y + c3Now.height * 0.6);
  await expect.poll(() => topDone()).toEqual(["完成 5", "完成 3", "完成 1", "完成 2", "完成 4"]);

  const c3SwapUp = (await rowIn("已结束", "完成 3").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 1"), c3SwapUp.y + c3SwapUp.height * 0.35);
  await expect.poll(() => topDone()).toEqual(["完成 5", "完成 1", "完成 3", "完成 2", "完成 4"]);

  // 3. Reordering inside 需要处理 (status:attention): move last item to top.
  const [att0, att1, att2] = initialAttention;
  const att0Box = (await rowIn("需要处理", att0).boundingBox())!;
  await dragTo(rowIn("需要处理", att2), att0Box.y + 6);
  await expect.poll(() => titlesOf("需要处理")).toEqual([att2, att0, att1]);

  // 4. Pin via .pin-dropzone, pin into middle/bottom of 置顶, reorder within 置顶, and drag OUT of 置顶 into a specific middle slot of 已结束!
  const listBox = (await page.locator(".side-list").boundingBox())!;
  // Pin "完成 2" via top pin-dropzone.
  const pinSrcBox = (await rowIn("已结束", "完成 2").boundingBox())!;
  await page.mouse.move(pinSrcBox.x + 80, pinSrcBox.y + pinSrcBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(pinSrcBox.x + 80, listBox.y + 12, { steps: 10 });
  await expect(page.locator(".pin-dropzone.over")).toBeVisible();
  await snap(page, "drag-pin-dropzone");
  await page.mouse.up();
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 2"]);

  // Drag "完成 4" to bottom of 置顶 -> ["完成 2", "完成 4"].
  const p2Box = (await rowIn("置顶", "完成 2").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 4"), p2Box.y + p2Box.height * 0.8);
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 2", "完成 4"]);

  // Drag "完成 3" into the MIDDLE of 置顶 (between "完成 2" and "完成 4") -> ["完成 2", "完成 3", "完成 4"].
  const p4Box = (await rowIn("置顶", "完成 4").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 3"), p4Box.y + p4Box.height * 0.2);
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 2", "完成 3", "完成 4"]);

  // Reorder inside 置顶: move "完成 4" above "完成 2" -> ["完成 4", "完成 2", "完成 3"].
  const pTop = (await rowIn("置顶", "完成 2").boundingBox())!;
  await dragTo(rowIn("置顶", "完成 4"), pTop.y + 6);
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 4", "完成 2", "完成 3"]);

  // Drag "完成 4" OUT of 置顶 directly between "完成 5" and "完成 1" in 已结束, then move "完成 1" above "完成 5":
  // 置顶 keeps ["完成 2", "完成 3"] (reverse of natural ["完成 3", "完成 2"]), and top 3 of 已结束 become ["完成 1", "完成 5", "完成 4"]!
  const c1InDone = (await rowIn("已结束", "完成 1").boundingBox())!;
  await dragTo(rowIn("置顶", "完成 4"), c1InDone.y + c1InDone.height * 0.25);
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 2", "完成 3"]);
  await expect.poll(() => topDone(3)).toEqual(["完成 5", "完成 4", "完成 1"]);

  const doneTopNow = (await rowIn("已结束", "完成 5").boundingBox())!;
  await dragTo(rowIn("已结束", "完成 1"), doneTopNow.y + 6);
  await expect.poll(() => topDone(3)).toEqual(["完成 1", "完成 5", "完成 4"]);

  // 5. Switch to 按项目 (project grouping) and reorder mixed-status rows inside "demo".
  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /分组/ }).hover();
  await page.getByRole("menu", { name: "分组" }).getByRole("menuitemradio", { name: "按项目" }).click();

  const demoBefore = await titlesOf("demo");
  expect(demoBefore.length).toBeGreaterThanOrEqual(4);
  const demoFirstBox = (await group("demo").getByRole("listitem").first().boundingBox())!;
  const movedTitle = demoBefore[2];
  await dragTo(rowIn("demo", movedTitle), demoFirstBox.y + 6);
  await expect.poll(() => titlesOf("demo")).toEqual([movedTitle, demoBefore[0], demoBefore[1], ...demoBefore.slice(3)]);

  // 6. Switch back to 按状态, relaunch app, verify all custom orders persisted, then reset order.
  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /分组/ }).hover();
  await page.getByRole("menu", { name: "分组" }).getByRole("menuitemradio", { name: "按状态" }).click();

  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  list = await sessionList(page);
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 2", "完成 3"]);
  await expect.poll(() => topDone(3)).toEqual(["完成 1", "完成 5", "完成 4"]);
  await expect.poll(() => titlesOf("需要处理")).toEqual([att2, att0, att1]);

  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /排序/ }).hover();
  await page.getByRole("menu", { name: "排序" }).getByRole("menuitem", { name: "恢复默认顺序" }).click();
  await expect.poll(() => titlesOf("置顶")).toEqual(["完成 3", "完成 2"]);
  await expect.poll(() => topDone(3)).toEqual(["完成 5", "完成 4", "完成 1"]);
  await expect.poll(() => titlesOf("需要处理")).toEqual(initialAttention);

  await app.close();
});
