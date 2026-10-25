import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { cli, fixture, launch, snap } from "./app";

// The main window's frame: the sidebar lists sessions as the owner chose to
// view them, by status unless they chose otherwise.
test("the sidebar groups sessions by status: needs you, running, done; archived only on request", async () => {
  const { app, page } = await launch();
  const list = page.getByRole("list", { name: "会话" });
  const group = (name: string) => list.getByRole("group", { name });
  await expect(group("需要处理")).toContainText("选颜色");
  await expect(group("需要处理")).toContainText("限流");
  await expect(group("需要处理")).toContainText("崩溃");
  await expect(group("进行中")).toContainText("跑很久");
  await expect(group("已结束")).toContainText("写 hello");
  await expect(list).not.toContainText("归档的");
  await snap(page, "main-sidebar");
  await page.getByRole("button", { name: "显示已归档" }).click();
  await expect(group("已归档")).toContainText("归档的");
  await page.getByRole("button", { name: "隐藏已归档" }).click();
  await expect(list).not.toContainText("归档的");
  await app.close();
});

// The status dot sits with the status text on the right, so titles start at
// the same edge as the group headings; a title too long for the row is cut
// with an ellipsis and never pushes the status out.
test("long titles are cut, the status dot sits by its text, and the left edge is flush", async () => {
  const { app, page } = await launch();
  const long = [
    "重构登录模块并补全单元测试，同时把旧的会话迁移脚本改成幂等的，最后更新文档和变更说明",
    "Investigate flaky integration tests in the payments service after the dependency upgrade",
    "feature/a-branch-name-with-no-spaces-at-all-to-see-how-an-unbreakable-title-is-cut-off",
  ];
  for (const t of long) await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", t, "--prompt", "say 好", "--wait");
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "一个正在运行、标题也很长很长很长的会话，看看计时和圆点是不是还在右边", "--prompt", "shell sleep 20\nsay 好了");
  const list = page.getByRole("list", { name: "会话" });
  for (const t of long) await expect(list.getByRole("listitem").filter({ hasText: t.slice(0, 12) })).toBeVisible();
  const running = list.getByRole("listitem").filter({ hasText: "一个正在运行" });
  await expect(running).toBeVisible();

  const box = async (l: ReturnType<typeof page.locator>) => (await l.boundingBox())!;
  for (const row of await list.getByRole("listitem").all()) {
    const title = row.locator(".stitle");
    const meta = row.locator(".smeta");
    const r = await box(row);
    const m = await box(meta);
    // The title is cut when it needs more room than it has, and the status stays inside the row.
    if (await title.evaluate((e) => e.scrollWidth > e.clientWidth)) expect(await title.evaluate((e) => getComputedStyle(e).textOverflow)).toBe("ellipsis");
    expect(m.x + m.width).toBeLessThanOrEqual(r.x + r.width);
    // The dot is in the status, after the title; nothing sits left of the title.
    expect((await box(meta.locator(".sdot"))).x).toBeGreaterThan((await box(title)).x + 40);
  }
  // Flush left: a title starts where its group's heading does.
  const heading = await box(list.getByRole("group", { name: "进行中" }).locator(".grp-h"));
  expect(Math.abs((await box(running.locator(".stitle"))).x - (heading.x + 9))).toBeLessThanOrEqual(1);
  expect(Math.abs((await box(running.locator(".sl2"))).x - (await box(running.locator(".stitle"))).x)).toBeLessThanOrEqual(1);
  // The time ends the second line, under the status: the clock for a running turn, else when it last changed.
  await expect(running.locator(".stime")).toHaveText(/^\d+:\d\d$/);
  const edge = async (l: ReturnType<typeof page.locator>) => (await box(l)).x + (await box(l)).width;
  expect(Math.abs((await edge(running.locator(".stime"))) - (await edge(running.locator(".smeta"))))).toBeLessThanOrEqual(1);
  await expect(list.getByRole("listitem").filter({ hasText: "写 hello" }).locator(".stime")).toHaveText(/^(刚刚|\d+ 分钟前|\d+ 小时前)$/);
  await snap(page, "sidebar-long-titles");
  await app.close();
});

// macOS: the window is see-through where the sidebar is, so the system's
// sidebar material shows there; the content stays opaque. The sidebar toggle
// sits right after the traffic lights.
test("the sidebar shows the window's material; the toggle sits after the traffic lights", async () => {
  test.skip(process.platform !== "darwin", "macOS only");
  const { app, page } = await launch();
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();
  expect(await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].getBackgroundColor())).toMatch(/^#0{6}(00)?$|^#00000000$/i);
  const bg = (sel: string) => page.locator(sel).first().evaluate((e) => getComputedStyle(e).backgroundColor);
  expect(await bg("body")).toBe("rgba(0, 0, 0, 0)");
  const side = await bg(".side");
  const alpha = Number(side.match(/rgba?\([^)]*,\s*([\d.]+)\)$/)?.[1] ?? 1);
  expect(alpha).toBeLessThan(0.5);
  expect(await bg(".content")).not.toMatch(/rgba\(.*,\s*0(\.\d+)?\)$/);
  const toggle = (await page.getByRole("button", { name: "隐藏边栏" }).boundingBox())!;
  expect(toggle.x).toBeGreaterThanOrEqual(90);
  expect(toggle.x).toBeLessThanOrEqual(94);
  await app.close();
});

// The avatar at the sidebar's foot opens the account menu upward, as wide as
// the foot; it is used with the mouse and with the keyboard, as a native menu.
test("the avatar opens the account menu above it, by mouse and keyboard", async () => {
  const { app, page } = await launch();
  const avatar = page.getByRole("button", { name: "账号与设置" });
  const menu = page.getByRole("menu", { name: "账号与设置" });
  const item = (name: string) => menu.getByRole("menuitem", { name: new RegExp(`^${name}`) });
  await avatar.click();
  await expect(avatar).toHaveAttribute("aria-expanded", "true");
  await expect(menu.getByRole("menuitem")).toHaveText(["连接 agent…⇧⌘N", "添加机器…", "输入配对码…⇧⌘P", "设置…⌘,", "账号…"]);
  await menu.evaluate((el) => Promise.all(el.getAnimations().map((x) => x.finished))); // it rises into place
  const a = (await avatar.boundingBox())!;
  const m = (await menu.boundingBox())!;
  expect(m.y + m.height).toBeLessThanOrEqual(a.y);
  expect(Math.abs(m.x - a.x)).toBeLessThan(1);
  expect(Math.abs(m.width - a.width)).toBeLessThan(1);
  await snap(page, "account-menu");

  // 连接 agent… opens its sheet, and the menu closes.
  await item("连接 agent").click();
  await expect(menu).toHaveCount(0);
  const sheet = page.getByRole("dialog", { name: "连接你的 Agent" });
  await expect(sheet).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();

  // A click elsewhere closes it; so does Esc, back to the avatar.
  await avatar.click();
  await page.getByRole("heading", { name: "概览", level: 1 }).click();
  await expect(menu).toHaveCount(0);
  await avatar.click();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(avatar).toBeFocused();

  // From the keyboard: the first item is chosen at once, the arrows go round, ↩ picks.
  await page.keyboard.press("Enter");
  await expect(item("连接 agent")).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(item("账号")).toBeFocused();
  // Waiting from before the key: the window may open before the menu is checked.
  const opened = app.waitForEvent("window", (w) => w.url().includes("#settings"));
  await page.keyboard.press("Enter");
  const settings = await opened;
  await expect(settings.getByRole("main", { name: "账号与设备" })).toBeVisible();
  await app.close();
});

/** The titles of the list's groups, top to bottom. */
const groups = (page: Page) => page.getByRole("list", { name: "会话" }).locator(".grp-h").allInnerTexts();

test("the view menu groups, sorts and shows the list from submenus, opaque over it, and is kept for next time", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(fixture.socket, { profile });
  const list = page.getByRole("list", { name: "会话" });
  const button = page.getByRole("button", { name: "视图选项" });
  const menu = page.getByRole("menu", { name: "视图选项" });

  // Each row of the menu says what it is set to.
  await button.click();
  await expect(menu.getByRole("menuitem")).toHaveText(["分组按状态", "排序最新在前", "显示元数据"]);
  const background = await menu.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(background).toMatch(/^rgb\(/); // no alpha: the list doesn't show through
  await menu.getByRole("menuitem", { name: /分组/ }).hover();
  const group = page.getByRole("menu", { name: "分组" });
  await expect(group.getByRole("menuitemradio")).toHaveText(["按状态", "按日期", "按 Agent", "按机器"]);
  await expect(group.getByRole("menuitemradio", { name: "按状态" })).toHaveAttribute("aria-checked", "true");
  await snap(page, "view-menu");
  await group.getByRole("menuitemradio", { name: "按 Agent" }).click();
  await expect(menu).toHaveCount(0);
  await expect(list.getByRole("group", { name: "Gemini CLI" })).toContainText("写 hello");

  // From the keyboard: → opens a submenu, ↓ moves, ↩ chooses.
  await page.mouse.move(0, 0);
  await button.focus();
  await page.keyboard.press("Enter");
  await expect(menu.getByRole("menuitem", { name: /分组/ })).toBeFocused();
  await page.keyboard.press("ArrowRight");
  await expect(group.getByRole("menuitemradio", { name: "按状态" })).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(menu).toHaveCount(0);
  await expect(button).toBeFocused();
  expect((await groups(page))[0]).toBe("今天");

  // Oldest first turns the days around, and each day's sessions.
  const today = list.getByRole("group", { name: "今天" }).locator(".stitle");
  const newest = await today.first().innerText();
  await button.click();
  await menu.getByRole("menuitem", { name: /排序/ }).hover();
  const sort = page.getByRole("menu", { name: "排序" });
  await expect(sort.getByRole("menuitemradio")).toHaveText(["最后活跃时间", "最新在前", "最早在前"]);
  await sort.getByRole("menuitemradio", { name: "最早在前" }).click();
  await expect(today.last()).toHaveText(newest);

  // Without the metadata, rows are their titles and status alone.
  await expect(list).toContainText("bot · Gemini · demo");
  await button.click();
  await menu.getByRole("menuitem", { name: /显示/ }).hover();
  await page.getByRole("menu", { name: "显示" }).getByRole("menuitemcheckbox", { name: "元数据" }).click();
  await expect(list).not.toContainText("bot · Gemini · demo");
  await expect(list).toContainText("写 hello");
  await snap(page, "view-title-only");

  // The view stays as it was left.
  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  expect((await groups(page)).at(-1)).toBe("今天");
  await expect(page.getByRole("list", { name: "会话" })).not.toContainText("bot · Gemini · demo");
  await page.getByRole("button", { name: "视图选项" }).click();
  await expect(page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem")).toHaveText(["分组按日期", "排序最早在前", "显示仅标题"]);
  await app.close();
});
