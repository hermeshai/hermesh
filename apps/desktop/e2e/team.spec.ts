import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launch, snap, sessionList, navTo } from "./app";

// Her and her team on the Mac (docs/her-team.md §14), on the fixture's world: this Mac with sessions in every
// state, all sent by the agent "bot", and Her on nowhere.

test("the window opens on Her's first run while she runs nowhere; Not Now makes the overview the start page, which brings her first run back", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(undefined, { profile, start: true });
  const firstRun = () => page.getByRole("heading", { name: "选择 Her 在哪里运行" });
  const work = () => page.getByRole("heading", { name: "概览", level: 1 });
  await expect(firstRun()).toBeVisible({ timeout: 30_000 });
  await expect(page.locator(".side-nav").getByRole("button", { name: /^Her/ })).toContainText("未启用");
  await page.getByRole("button", { name: "以后再说" }).click();
  await expect(work()).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Her 还没启用" })).toBeVisible();
  await app.close();

  // Next time, the overview: Not Now is kept on this Mac.
  ({ app, page } = await launch(undefined, { profile, start: true }));
  // The start page is picked once the owner's world is in: its sessions are counted, and it is still the overview.
  await expect(page.getByRole("region", { name: "需要你处理" })).toContainText("选颜色", { timeout: 30_000 });
  await expect(work()).toBeVisible();
  await page.getByRole("button", { name: "选择她在哪里运行…" }).click();
  await expect(firstRun()).toBeVisible();
  await app.close();
});

test("the sidebar is Her, the overview and every session; collapsed, its rail shows the agents at work, each a menu of its sessions", async () => {
  const { app, page } = await launch();
  await page.setViewportSize({ width: 1240, height: 800 });
  const sidebar = page.getByRole("complementary", { name: "边栏" });
  await expect(page.locator(".side-nav .nav-i")).toHaveText([/^Her/, /^概览/]);
  // The overview's row: a dot while something waits on the owner, and a ring while anything runs; no numbers.
  const overview = page.locator(".side-nav").getByRole("button", { name: /^概览/ });
  await expect(overview.getByRole("img", { name: /项需要处理/ })).toBeVisible();
  await expect(overview.getByRole("img", { name: /个在忙/ })).toBeVisible();
  await expect(overview).not.toContainText(/\d/);
  // No map of machines and agents: their settings are in Settings.
  await expect(page.getByRole("group", { name: "团队" })).toHaveCount(0);
  await expect(sidebar).not.toContainText("连接的 Agent");

  // The sessions are in the sidebar itself; one chosen shows beside it, marked in it.
  const list = page.getByRole("list", { name: "任务" });
  await expect(sidebar.getByRole("list", { name: "任务" })).toContainText("写 hello");
  const hello = list.getByRole("listitem").filter({ hasText: "写 hello" });
  await hello.click();
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  await expect(hello).toHaveAttribute("aria-current", "true");
  await expect(page.locator(".slist")).toHaveCount(0);
  await snap(page, "sidebar-session");

  // Collapsed, the sidebar is its rail, and stays so from page to page: Her, the overview, then one logo for each
  // coding agent with sessions at hand, saying on hover how they stand.
  await page.getByRole("button", { name: "收成窄栏" }).click();
  const rail = page.getByRole("complementary", { name: "窄栏" });
  await expect(rail.getByRole("button", { name: /^Her/ })).toBeVisible();
  await expect(rail.getByRole("button", { name: /^概览/ })).toBeVisible();
  const gemini = rail.getByRole("button", { name: /^Gemini CLI · \d+ 个在等你 · \d+ 个出错 · \d+ 个在忙$/ });
  await gemini.hover();
  await expect(page.getByRole("tooltip")).toHaveText(/^Gemini CLI · \d+ 个在等你 · \d+ 个出错 · \d+ 个在忙$/);
  // Its sessions at hand are a menu beside it, what waits first; a click opens one, the rail staying.
  await gemini.click();
  const menu = page.getByRole("menu", { name: "Gemini CLI" });
  await expect(menu.getByRole("menuitem").first()).toContainText("选颜色");
  await expect(menu).toContainText("跑很久");
  await expect(menu).not.toContainText("写 hello");
  await snap(page, "rail-agent-menu");
  await menu.getByRole("menuitem", { name: /跑很久/ }).click();
  await expect(menu).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "跑很久" })).toBeVisible();
  await expect(gemini).toHaveAttribute("aria-current", "true");
  // From the keyboard: the first is chosen at once; Esc closes back to the logo.
  await gemini.focus();
  await page.keyboard.press("Enter");
  await expect(menu.getByRole("menuitem").first()).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(gemini).toBeFocused();
  await navTo(page, /^概览/);
  await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
  await expect(rail).toBeVisible();
  await rail.getByRole("button", { name: "展开边栏" }).click();
  await expect(sidebar).toBeVisible();
  await app.close();
});

test("The overview: what waits is listed and one click answers a simple question; recently finished; the link map stays", async () => {
  const { app, page } = await launch();
  await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
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
