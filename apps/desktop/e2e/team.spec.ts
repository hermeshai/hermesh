import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launch, snap, thisComputer, sessionList, navTo } from "./app";

// Her and her team on the Mac (docs/her-team.md §14), on the fixture's world: this Mac with sessions in every
// state, all sent by the agent "bot", and Her on nowhere.

test("the window opens on Her's first run while she runs nowhere; Not Now makes Work the start page, which brings her first run back", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(undefined, { profile, start: true });
  const firstRun = () => page.getByRole("heading", { name: "选择 Her 在哪里运行" });
  const work = () => page.getByRole("heading", { name: "工作", level: 1 });
  await expect(firstRun()).toBeVisible({ timeout: 30_000 });
  await expect(page.locator(".side-nav").getByRole("button", { name: /^Her/ })).toContainText("未启用");
  await page.getByRole("button", { name: "以后再说" }).click();
  await expect(work()).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Her 还没启用" })).toBeVisible();
  await app.close();

  // Next time, Work: Not Now is kept on this Mac.
  ({ app, page } = await launch(undefined, { profile, start: true }));
  // The start page is picked once the owner's world is in: its sessions are counted, and it is still Work.
  await expect(page.getByRole("region", { name: "需要你处理" })).toContainText("选颜色", { timeout: 30_000 });
  await expect(work()).toBeVisible();
  await page.getByRole("button", { name: "选择她在哪里运行…" }).click();
  await expect(firstRun()).toBeVisible();
  await app.close();
});

test("the sidebar is navigation only: its pages, All Sessions and the team, whose rows open the list's column beside the session", async () => {
  const { app, page } = await launch();
  await expect(page.locator(".side-nav .nav-i")).toHaveText([/^Her/, /^工作/, /^搜索/, /^全部会话/]);
  // No session in the sidebar: the list is a column of its own, opened from it.
  await expect(page.getByRole("complementary", { name: "边栏" })).not.toContainText("写 hello");
  // Work's row: a dot while something waits on the owner, and a ring with what runs.
  const workRow = page.locator(".side-nav").getByRole("button", { name: /^工作/ });
  await expect(workRow.getByRole("img", { name: /项需要处理/ })).toBeVisible();
  await expect(workRow.getByRole("img", { name: "1 个在忙" })).toBeVisible();

  // The map: this Mac, its coding agents, the agents connected to the owner.
  const map = page.getByRole("group", { name: "团队" });
  const mac = map.getByRole("button", { name: new RegExp(`^${thisComputer}`) });
  const gemini = map.getByRole("button", { name: /^Gemini CLI/ });
  await expect(gemini.getByRole("img", { name: "在等你" })).toBeVisible();
  await expect(gemini.getByRole("img", { name: "1 个在忙" })).toBeVisible();
  await expect(map).toContainText("连接的 Agent");

  // All Sessions: the column, by who sent the work, each row saying who sent it to whom; none chosen yet.
  await page.locator(".side-nav").getByRole("button", { name: /^全部会话/ }).click();
  const column = page.getByRole("region", { name: "全部会话" });
  const list = page.getByRole("list", { name: "会话" });
  await expect(list.getByRole("group", { name: "bot 派的" })).toContainText("写 hello");
  await expect(list.getByRole("listitem").filter({ hasText: "写 hello" })).toContainText("bot → Gemini · demo");
  await expect(page.getByText("选一个会话")).toBeVisible();
  const rows = await list.getByRole("listitem").count();
  // A session chosen in it shows beside it, the list staying.
  await list.getByRole("listitem").filter({ hasText: "写 hello" }).click();
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  await expect(column).toBeVisible();
  // The sidebar stays as it was (docs/her-team.md §14.9), the place chosen marked in it.
  await expect(page.getByRole("complementary", { name: "边栏" })).toBeVisible();
  await expect(page.locator(".side-nav").getByRole("button", { name: /^全部会话/ })).toHaveAttribute("aria-current", "true");
  await snap(page, "team-three-columns");

  // A row of the map is the list of its sessions, under its name; the chosen session stays when it is one of them.
  await gemini.click();
  await expect(gemini).toHaveAttribute("aria-current", "true");
  await expect(page.getByRole("region", { name: `${thisComputer} 上的 Gemini` })).toBeVisible();
  await expect(list.getByRole("listitem")).toHaveCount(rows);
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  await mac.click();
  await expect(page.getByRole("region", { name: thisComputer })).toBeVisible();
  await map.getByRole("button", { name: /^bot/ }).click();
  await expect(page.getByRole("region", { name: "bot 派的" })).toBeVisible();
  await expect(gemini).not.toHaveAttribute("aria-current", "true");

  // Collapsed, the sidebar is its rail, and stays so from page to page: the same places, as icons, each saying on
  // hover what it is and how it stands.
  await page.getByRole("button", { name: "收成窄栏" }).click();
  const rail = page.getByRole("complementary", { name: "窄栏" });
  await expect(rail.getByRole("button", { name: /^bot/ })).toHaveAttribute("aria-current", "true");
  const railGemini = rail.getByRole("button", { name: new RegExp(`^${thisComputer} 上的 Gemini CLI · 1 个在等你 · 1 个在忙$`) });
  await railGemini.click();
  await expect(page.getByRole("region", { name: `${thisComputer} 上的 Gemini` })).toBeVisible();
  await railGemini.hover();
  await expect(page.getByRole("tooltip")).toHaveText(`${thisComputer} 上的 Gemini CLI · 1 个在等你 · 1 个在忙`);
  await snap(page, "team-rail");
  await navTo(page, /^工作/);
  await expect(page.getByRole("heading", { name: "工作", level: 1 })).toBeVisible();
  await expect(list).toHaveCount(0);
  await expect(rail).toBeVisible();
  await rail.getByRole("button", { name: "展开边栏" }).click();
  await expect(page.getByRole("complementary", { name: "边栏" })).toBeVisible();

  // The map folds.
  await map.getByRole("button", { name: /^团队/ }).click();
  await expect(mac).toHaveCount(0);
  await map.getByRole("button", { name: /^团队/ }).click();
  await expect(mac).toBeVisible();
  await app.close();
});

test("Work: what waits is listed and one click answers a simple question; recently finished; the link map stays", async () => {
  const { app, page } = await launch();
  await expect(page.getByRole("heading", { name: "工作", level: 1 })).toBeVisible();
  await expect(page.getByRole("region", { name: "链路" })).toBeVisible();
  const needs = page.getByRole("region", { name: "需要你处理" });
  const question = needs.getByRole("listitem").filter({ hasText: "选颜色" });
  await expect(question).toContainText("bot → Gemini · demo · 选哪个颜色");
  await expect(needs.getByRole("listitem").filter({ hasText: "限流" })).toBeVisible();
  await expect(page.getByRole("region", { name: "最近完成" })).toContainText("写 hello");
  await snap(page, "team-work");

  await question.getByRole("button", { name: "蓝", exact: true }).click();
  await expect(page.locator(".toast")).toHaveText("已回答：蓝");
  await expect(question).toHaveCount(0, { timeout: 30_000 });
  // The session goes on with the answer.
  await (await sessionList(page)).getByRole("listitem").filter({ hasText: "选颜色" }).click();
  await expect(page.getByRole("heading", { name: "选颜色" })).toBeVisible();
  await expect(page.getByRole("log", { name: "对话" })).toContainText("蓝", { timeout: 30_000 });
  await app.close();
});
