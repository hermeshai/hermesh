import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { cli, fixture, launch, onMac, snap, sessionList } from "./app";

// The main window's frame (docs/her-team.md §16): the sidebar is Her, the overview, and every session as the owner
// views them: by status, titles alone, until they choose otherwise.
test("the sidebar groups sessions by status: needs you, running, done; archived only on request", async () => {
  const { app, page } = await launch(fixture.socket, { profile: fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-")) });
  const list = (await sessionList(page));
  const group = (name: string) => list.getByRole("group", { name });
  await expect(list.locator(".grp-h")).toHaveText(["需要处理", "进行中", "已结束"]);
  await expect(group("需要处理")).toContainText("选颜色");
  await expect(group("需要处理")).toContainText("限流");
  await expect(group("需要处理")).toContainText("崩溃");
  await expect(group("进行中")).toContainText("跑很久");
  await expect(group("已结束")).toContainText("写 hello");
  // Titles alone: no second line.
  await expect(list.locator(".sl2")).toHaveCount(0);
  await expect(list).not.toContainText("归档的");
  await snap(page, "main-sidebar");
  await page.getByRole("button", { name: "显示已归档" }).click();
  await expect(group("已归档")).toContainText("归档的");
  await page.getByRole("button", { name: "隐藏已归档" }).click();
  await expect(list).not.toContainText("归档的");
  await app.close();
});

// The sidebar reads calmly: Her and the overview a row apart, no line under them, the sessions' heading with the
// view menu at its end; search and the toggle in the top band; 14px titles; a quiet foot, a dot only when something
// is wrong, and no count of machines.
test("the sidebar keeps one rhythm, one heading per group and a quiet foot", async () => {
  const { app, page } = await launch();
  const box = async (l: ReturnType<typeof page.locator>) => (await l.boundingBox())!;
  const centre = async (l: ReturnType<typeof page.locator>) => ((b) => b.y + b.height / 2)(await box(l));
  const hide = await centre(page.getByRole("button", { name: "隐藏边栏" }));
  const nav = page.locator(".side-nav");
  await expect(nav.getByRole("button")).toHaveText([/^新会话/, /^Her/, /^概览/]);
  // New Session's icon is a plain plus.
  expect(await nav.getByTestId("nav-new-thread").locator("svg.ic").innerHTML()).toBe('<path d="M8 3v10M3 8h10"></path>');
  const her = await box(nav.getByRole("button", { name: /^Her/ }));
  const overview = await box(nav.getByRole("button", { name: /^概览/ }));
  expect(overview.height).toBe(32);
  expect(overview.y - her.y).toBe(34);
  // Search shares the top band with the toggle, at its end.
  const search = page.locator(".side-top").getByRole("button", { name: "搜索" });
  expect(Math.abs((await centre(search)) - hide)).toBeLessThanOrEqual(1);
  const side = await box(page.locator(".side"));
  expect(side.x + side.width - ((await box(search)).x + (await box(search)).width)).toBeLessThanOrEqual(10);
  // The sessions' heading under them, with no line between, its view menu at its end.
  expect(await nav.evaluate((el) => getComputedStyle(el).borderBottomWidth)).toBe("0px");
  const heading = page.locator(".side-h");
  await expect(heading).toHaveText("任务");
  expect((await box(heading)).y).toBeGreaterThanOrEqual(overview.y + overview.height + 8);
  const view = heading.getByRole("button", { name: "视图选项" });
  expect(Math.abs((await centre(view)) - (await centre(heading)))).toBeLessThanOrEqual(1);

  const list = await sessionList(page);
  const title = list.getByRole("listitem").first().locator(".stitle");
  expect(await title.evaluate((el) => [getComputedStyle(el).fontSize, getComputedStyle(el).lineHeight])).toEqual(["14px", "20px"]);
  expect(await heading.evaluate((el) => getComputedStyle(el).fontSize)).toBe("12.5px");
  expect(await list.locator(".grp-h").first().evaluate((el) => getComputedStyle(el).fontSize)).toBe("13px");
  // No numbers: the group headings are their names alone.
  for (const h of await list.locator(".grp-h").allInnerTexts()) expect(h).not.toMatch(/\d/);

  const foot = page.getByRole("button", { name: "账号与设置" });
  await expect(foot).not.toContainText("在线");
  // Signed in or not, and nothing more (docs/cold-start-prd.md §6.1).
  await expect(foot).toContainText("已登录");
  await expect(foot.locator(".dotc")).toHaveCount(0);
  await page.emulateMedia({ colorScheme: "dark" });
  // Selected nav item reads brighter and heavier than an unselected row in the dark.
  const selStyle = await nav.locator(".nav-i.sel").evaluate((el) => [getComputedStyle(el).color, getComputedStyle(el).fontWeight]);
  const idleStyle = await title.evaluate((el) => [getComputedStyle(el).color, getComputedStyle(el).fontWeight]);
  expect(selStyle).toEqual(["rgb(255, 255, 255)", "500"]);
  expect(idleStyle).toEqual(["rgba(255, 255, 255, 0.78)", "400"]);
  await snap(page, "sidebar-rhythm");
  await app.close();
});

// A row is the coding agent's logo, the title and one mark after it: an orange dot while it waits for the owner, a
// red one when it failed, a ring while it runs, none when it is done. A title too long for the row is cut with an
// ellipsis and never pushes the mark out.
test("long titles are cut, each row is the agent's logo, its title and one mark, and the left edge is flush", async () => {
  const { app, page } = await launch();
  const long = [
    "重构登录模块并补全单元测试，同时把旧的会话迁移脚本改成幂等的，最后更新文档和变更说明",
    "Investigate flaky integration tests in the payments service after the dependency upgrade",
    "feature/a-branch-name-with-no-spaces-at-all-to-see-how-an-unbreakable-title-is-cut-off",
  ];
  for (const t of long) await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", t, "--prompt", "say 好", "--wait");
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "一个正在运行、标题也很长很长很长的会话，看看计时和圆点是不是还在右边", "--prompt", "shell sleep 20\nsay 好了");
  const list = (await sessionList(page));
  for (const t of long) await expect(list.getByRole("listitem").filter({ hasText: t.slice(0, 12) })).toBeVisible();
  const running = list.getByRole("listitem").filter({ hasText: "一个正在运行" });
  await expect(running).toBeVisible();

  // Hovering a row replaces its mark with ⋯. Measure the resting layout.
  await page.mouse.move(0, 0);
  const box = async (l: ReturnType<typeof page.locator>) => (await l.boundingBox())!;
  for (const row of await list.getByRole("listitem").all()) {
    const title = row.locator(".stitle");
    const meta = row.locator(".smeta");
    const r = await box(row);
    const m = await box(meta);
    // The title is cut when it needs more room than it has, and the mark stays inside the row, after the title.
    if (await title.evaluate((e) => e.scrollWidth > e.clientWidth)) expect(await title.evaluate((e) => getComputedStyle(e).textOverflow)).toBe("ellipsis");
    expect(m.x + m.width).toBeLessThanOrEqual(r.x + r.width);
    // The agent's logo starts the row, left of the title.
    expect((await box(row.locator(".slogo"))).x + 16).toBeLessThanOrEqual((await box(title)).x);
  }
  const mark = (title: string) => list.getByRole("listitem").filter({ hasText: title }).locator(".smeta [role=img]");
  await expect(mark("选颜色")).toHaveAccessibleName("等回答");
  await expect(mark("选颜色")).toHaveClass("dot-w");
  await expect(mark("限流")).toHaveClass("dot-r");
  await expect(mark("崩溃")).toHaveAccessibleName("已崩溃");
  await expect(running.locator(".smeta .busy")).toHaveAccessibleName("运行中");
  await expect(mark("写 hello")).toHaveCount(0);
  // The running title shimmers, as on the iPhone: two moving copies over it, which screen readers skip; the title still reads as written.
  await expect(running.locator(".stitle")).toHaveClass(/\brun\b/);
  await expect(running.locator(".stitle .shine[aria-hidden=true]")).toHaveCount(2);
  expect(await running.locator(".stitle .shine").first().evaluate((e) => getComputedStyle(e).animationName)).toBe("stitle-run");
  await expect(running.locator(".stitle")).toHaveText((await running.locator(".stitle").getAttribute("title"))!);
  await expect(list.getByRole("listitem").filter({ hasText: "写 hello" }).locator(".stitle .shine")).toHaveCount(0);
  // The overview does not turn while sessions run (each row shows its own work); its one mark is the waiting dot.
  const overview = page.getByRole("button", { name: /^概览/ });
  await expect(overview.locator(".dot-w")).toBeVisible();
  await expect(overview.locator(".busy")).toHaveCount(0);
  // Flush left: a row's logo starts where its group's heading does.
  const heading = await box(list.getByRole("group", { name: "进行中" }).locator(".grp-h"));
  expect(Math.abs((await box(running.locator(".slogo"))).x - (heading.x + 9))).toBeLessThanOrEqual(1);
  await snap(page, "sidebar-long-titles");

  // With the metadata, the second line starts under the title, the time at its end under the mark.
  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /显示/ }).hover();
  await page.getByRole("menu", { name: "显示" }).getByRole("menuitemcheckbox", { name: "元数据" }).click();
  await page.mouse.move(0, 0);
  expect(Math.abs((await box(running.locator(".sl2"))).x - (await box(running.locator(".stitle"))).x)).toBeLessThanOrEqual(1);
  await expect(running.locator(".stime")).toHaveText(/^\d+:\d\d$/);
  const edge = async (l: ReturnType<typeof page.locator>) => (await box(l)).x + (await box(l)).width;
  expect(Math.abs((await edge(running.locator(".stime"))) - (await edge(running.locator(".smeta"))))).toBeLessThanOrEqual(1);
  await expect(list.getByRole("listitem").filter({ hasText: "写 hello" }).locator(".stime")).toHaveText(/^(刚刚|\d+ 分钟前|\d+ 小时前)$/);
  await expect(list.getByRole("listitem").filter({ hasText: "写 hello" })).toContainText("bot → Gemini · demo");
  await snap(page, "sidebar-metadata");
  await app.close();
});

// macOS: the window is see-through where the sidebar is, so the system's
// sidebar material shows there; the content stays opaque. The top band is 44
// high with the traffic lights in its middle, and the sidebar toggle sits
// right after them, shown or hidden.
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
  expect(await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].getWindowButtonPosition())).toEqual({ x: 12, y: 15 });
  expect((await page.locator(".side-top").boundingBox())!.height).toBe(44);
  expect((await page.locator(".ctool").boundingBox())!.height).toBe(44);
  const toggle = (await page.getByRole("button", { name: "隐藏边栏" }).boundingBox())!;
  expect(toggle.x).toBeGreaterThanOrEqual(77);
  expect(toggle.x).toBeLessThanOrEqual(81);
  // On the lights' line: 14 high from y 15.
  expect(Math.abs(toggle.y + toggle.height / 2 - 22)).toBeLessThanOrEqual(1);
  await page.getByRole("button", { name: "隐藏边栏" }).click();
  // Hidden, the sidebar is gone; the content fills the window, and the toggle that shows it again keeps its place.
  await expect(page.getByRole("complementary", { name: "边栏" })).toHaveCount(0);
  await expect.poll(async () => Math.round((await page.locator(".content").boundingBox())!.x)).toBe(0);
  const show = (await page.getByRole("button", { name: "显示边栏" }).boundingBox())!;
  expect(Math.abs(show.x - toggle.x)).toBeLessThanOrEqual(1);
  expect(Math.abs(show.y + show.height / 2 - 22)).toBeLessThanOrEqual(1);
  await page.getByRole("button", { name: "显示边栏" }).click();
  await expect.poll(async () => Math.round((await page.getByRole("complementary", { name: "边栏" }).boundingBox())!.width)).toBe(272);
  await app.close();
});

// The avatar at the sidebar's foot opens the account menu upward, as wide as
// the foot and opaque over the list; it is used with the mouse and with the
// keyboard, as a native menu.
test("the avatar opens the account menu above it, opaque, by mouse and keyboard", async () => {
  const { app, page } = await launch();
  const avatar = page.getByRole("button", { name: "账号与设置" });
  const menu = page.getByRole("menu", { name: "账号与设置" });
  const item = (name: string) => menu.getByRole("menuitem", { name: new RegExp(`^${name}`) });
  await avatar.click();
  await expect(avatar).toHaveAttribute("aria-expanded", "true");
  await expect(menu.getByRole("menuitem")).toHaveText(
    onMac ? ["连接 agent…⇧⌘N", "添加环境…", "输入配对码…⇧⌘P", "设置…⌘,", "账号…"] : ["连接 agent…Ctrl+Shift+N", "添加环境…", "输入配对码…Ctrl+Shift+P", "设置…Ctrl+,", "账号…"],
  );
  await menu.evaluate((el) => Promise.all(el.getAnimations().map((x) => x.finished))); // it rises into place
  const a = (await avatar.boundingBox())!;
  const m = (await menu.boundingBox())!;
  expect(m.y + m.height).toBeLessThanOrEqual(a.y);
  expect(Math.abs(m.x - a.x)).toBeLessThan(1);
  expect(Math.abs(m.width - a.width)).toBeLessThan(1);
  const background = await menu.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(background).toMatch(/^rgb\(/); // no alpha: the list doesn't show through
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
const groups = async (page: Page) => (await sessionList(page)).locator(".grp-h").allInnerTexts();

test("the view menu groups, sorts and shows the list from submenus, opaque over it, and is kept for next time", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(fixture.socket, { profile });
  const list = (await sessionList(page));
  const button = page.getByRole("button", { name: "视图选项" });
  const menu = page.getByRole("menu", { name: "视图选项" });

  // Each row of the menu says what it is set to.
  await button.click();
  await expect(menu.getByRole("menuitem")).toHaveText(["分组按状态", "排序最新在前", "显示仅标题"]);
  // It opens leftward from its button, over the sidebar.
  const [m, b, side] = await Promise.all([menu.boundingBox(), button.boundingBox(), page.locator(".side").boundingBox()]);
  expect(Math.abs(m!.x + m!.width - (b!.x + b!.width))).toBeLessThanOrEqual(1);
  expect(m!.x).toBeGreaterThanOrEqual(side!.x);
  const background = await menu.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(background).toMatch(/^rgb\(/); // no alpha: the list doesn't show through
  await menu.getByRole("menuitem", { name: /分组/ }).hover();
  const group = page.getByRole("menu", { name: "分组" });
  await expect(group.getByRole("menuitemradio")).toHaveText(["按派活的人", "按状态", "按日期", "按项目", "按 Agent", "按环境"]);
  // By status, until the owner picks a view (docs/her-team.md §16).
  await expect(group.getByRole("menuitemradio", { name: "按状态" })).toHaveAttribute("aria-checked", "true");
  await expect(list.getByRole("group", { name: "已结束" })).toContainText("写 hello");
  await snap(page, "view-menu");
  await group.getByRole("menuitemradio", { name: "按派活的人" }).click();
  await expect(list.getByRole("group", { name: "bot 派的" })).toContainText("写 hello");
  await button.click();
  await menu.getByRole("menuitem", { name: /分组/ }).hover();
  await group.getByRole("menuitemradio", { name: "按 Agent" }).click();
  await expect(menu).toHaveCount(0);
  await expect(list.getByRole("group", { name: "Gemini CLI" })).toContainText("写 hello");
  // By project: a group for each, named as the project.
  await button.click();
  await menu.getByRole("menuitem", { name: /分组/ }).hover();
  await group.getByRole("menuitemradio", { name: "按项目" }).click();
  await expect(list.getByRole("group", { name: "demo" })).toContainText("写 hello");

  // From the keyboard: → opens a submenu, ↓ moves, ↩ chooses.
  await page.mouse.move(0, 0);
  await button.focus();
  await page.keyboard.press("Enter");
  await expect(menu.getByRole("menuitem", { name: /分组/ })).toBeFocused();
  await page.keyboard.press("ArrowRight");
  await expect(group.getByRole("menuitemradio", { name: "按派活的人" })).toBeFocused();
  await page.keyboard.press("ArrowDown");
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

  // With the metadata, each row says who sent the work to which agent, and the project.
  await expect(list).not.toContainText("bot → Gemini · demo");
  await button.click();
  await menu.getByRole("menuitem", { name: /显示/ }).hover();
  await page.getByRole("menu", { name: "显示" }).getByRole("menuitemcheckbox", { name: "元数据" }).click();
  await expect(list).toContainText("bot → Gemini · demo");
  await snap(page, "view-metadata");

  // The view stays as it was left.
  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  await expect((await sessionList(page))).toContainText("写 hello");
  expect((await groups(page)).at(-1)).toBe("今天");
  await expect((await sessionList(page))).toContainText("bot → Gemini · demo");
  await page.getByRole("button", { name: "视图选项" }).click();
  await expect(page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem")).toHaveText(["分组按日期", "排序最早在前", "显示元数据"]);
  await app.close();
});

test("a group's heading collapses and opens it, in every grouping, and stays so next time", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(fixture.socket, { profile });
  const list = (await sessionList(page));
  const heading = (name: string) => list.getByRole("group", { name }).getByRole("button", { name: new RegExp(`^${name}`) });
  const rows = (name: string) => list.getByRole("group", { name }).getByRole("listitem");
  const groupBy = async (name: string) => {
    await page.getByRole("button", { name: "视图选项" }).click();
    await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /分组/ }).hover();
    await page.getByRole("menu", { name: "分组" }).getByRole("menuitemradio", { name }).click();
  };
  await groupBy("按状态");

  await expect(heading("已结束")).toHaveAttribute("aria-expanded", "true");
  await expect(rows("已结束")).not.toHaveCount(0);
  await heading("已结束").click();
  await expect(heading("已结束")).toHaveAttribute("aria-expanded", "false");
  await expect(rows("已结束")).toHaveCount(0);
  await expect(rows("进行中")).not.toHaveCount(0); // only that group
  await snap(page, "sidebar-collapsed");

  // By date, its groups collapse on their own; the status groups keep theirs.
  await groupBy("按日期");
  await expect(rows("今天")).not.toHaveCount(0);
  await heading("今天").click();
  await expect(rows("今天")).toHaveCount(0);

  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  const again = (await sessionList(page));
  await expect(again.getByRole("group", { name: "今天" }).getByRole("button", { name: /^今天/ })).toHaveAttribute("aria-expanded", "false");
  await expect(again.getByRole("group", { name: "今天" }).getByRole("listitem")).toHaveCount(0);
  await again.getByRole("group", { name: "今天" }).getByRole("button", { name: /^今天/ }).click();
  await expect(again.getByRole("group", { name: "今天" }).getByRole("listitem")).not.toHaveCount(0);
  await app.close();
});

test("a row's ⋯ menu renames in place, archives, and deletes once confirmed, interrupting a running turn", async () => {
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "改个名字", "--prompt", "say 好", "--wait");
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "还在跑的", "--prompt", "shell sleep 1800");
  const { app, page } = await launch();
  const list = (await sessionList(page));
  const row = (title: string) => list.getByRole("listitem").filter({ hasText: title });
  const more = async (title: string) => {
    await row(title).hover();
    await row(title).getByRole("button", { name: "更多操作" }).click();
    return page.getByRole("menu", { name: "更多操作" });
  };

  // ⋯ in place of the archive button: rename, regenerate title, archive, then delete after a line.
  let menu = await more("改个名字");
  await expect(menu.getByRole("menuitem")).toHaveText(["重命名", "重新生成标题", "归档", "删除…"]);
  await snap(page, "row-menu");
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);

  // Rename in place: Esc keeps the title, ↩ saves the new one.
  menu = await more("改个名字");
  await menu.getByRole("menuitem", { name: "重命名" }).click();
  const field = list.getByRole("textbox", { name: "任务标题" });
  await expect(field).toBeFocused();
  await expect(field).toHaveValue("改个名字");
  await field.fill("不要这个");
  await field.press("Escape");
  await expect(field).toHaveCount(0);
  await expect(row("改个名字")).toBeVisible();
  menu = await more("改个名字");
  await menu.getByRole("menuitem", { name: "重命名" }).click();
  await field.fill("新的名字");
  await snap(page, "row-rename");
  await field.press("Enter");
  await expect(row("新的名字")).toBeVisible();
  await expect(row("改个名字")).toHaveCount(0);

  // Delete asks first; canceling keeps it.
  menu = await more("新的名字");
  await menu.getByRole("menuitem", { name: "删除…" }).click();
  const dialog = page.getByRole("dialog", { name: "删除「新的名字」？" });
  await expect(dialog).toContainText("任务记录、对话和工作区会被永久删除，无法恢复。agent 做的改动仍保留在分支");
  await snap(page, "row-delete-confirm");
  await dialog.getByRole("button", { name: "取消" }).click();
  await expect(row("新的名字")).toBeVisible();
  menu = await more("新的名字");
  await menu.getByRole("menuitem", { name: "删除…" }).click();
  await dialog.getByRole("button", { name: "删除" }).click();
  await expect(page.getByText("已删除「新的名字」")).toBeVisible();
  await expect(row("新的名字")).toHaveCount(0);

  // A running session can't be archived from here, but deleting it interrupts its turn first.
  await expect(row("还在跑的")).toBeVisible();
  menu = await more("还在跑的");
  await expect(menu.getByRole("menuitem", { name: "归档" })).toBeDisabled();
  await menu.getByRole("menuitem", { name: "删除…" }).click();
  const running = page.getByRole("dialog", { name: "删除「还在跑的」？" });
  await expect(running).toContainText("这一轮还在进行，会先被打断。");
  await running.getByRole("button", { name: "删除" }).click();
  await expect(row("还在跑的")).toHaveCount(0, { timeout: 30_000 });
  expect(await cli("session", "list")).not.toContain("还在跑的");
  await app.close();
});

// Narrowing the window, what is beside the conversation gives way, the right
// side first: the info card, then the sidebar, until the conversation is
// alone. What gave way can still be asked for: it shows over the conversation
// for now, and goes with Esc, a click beside it or another choice. Widened
// again, both are back as the owner had them.
test("a narrow window hides the info card, then the sidebar; asked for, they show over", async () => {
  const { app, page } = await launch();
  const resize = async (width: number) => {
    // Native window sizes can be clamped to the runner's display; exercise the layout at each viewport width.
    await page.setViewportSize({ width, height: 800 });
    await expect.poll(() => page.evaluate(() => innerWidth)).toBe(width);
  };
  const main = page.locator(".mainwrap");
  const sidebar = page.getByRole("complementary", { name: "边栏" });
  const card = page.getByRole("complementary", { name: "任务信息" });
  const info = page.getByRole("button", { name: "信息面板", exact: true });
  const showSidebar = page.getByRole("button", { name: "显示边栏" });
  const conversation = page.locator(".dmain");
  const docked = async () => ({ sidebar: !/\bnoside\b/.test((await main.getAttribute("class"))!), card: await page.locator(".dwrap.dock").count() === 1 });

  // 1440: the sidebar, the session and its card; two columns and the card, no list column.
  await resize(1440);
  await (await sessionList(page)).getByText("写 hello", { exact: true }).click();
  await expect(card).toBeVisible();
  expect(await docked()).toEqual({ sidebar: true, card: true });
  await expect(page.locator(".slist")).toHaveCount(0);

  // 1000: no room for the card beside the conversation; the sidebar stays.
  await resize(1000);
  await expect(card).toHaveCount(0);
  await expect(info).toHaveAttribute("aria-pressed", "false");
  expect(await docked()).toEqual({ sidebar: true, card: false });
  await expect.poll(async () => Math.round((await conversation.boundingBox())!.width)).toBe(1000 - 272);
  await snap(page, "layout-1000");
  // Asked for, the card shows over the conversation, which keeps its width; Esc puts it away.
  await info.click();
  await expect(card).toBeVisible();
  await expect(info).toHaveAttribute("aria-pressed", "true");
  expect(await docked()).toEqual({ sidebar: true, card: false });
  await snap(page, "layout-1000-card-over");
  await page.keyboard.press("Escape");
  await expect(card).toHaveCount(0);

  // 800: the sidebar is hidden; the conversation has the window.
  await resize(800);
  await expect(sidebar).toHaveCount(0);
  expect(await docked()).toEqual({ sidebar: false, card: false });
  await expect.poll(async () => Math.round((await conversation.boundingBox())!.width)).toBe(800);
  await snap(page, "layout-800");
  // Asked for, the sidebar shows over the session, which stays where it is; a click beside it puts it away.
  await showSidebar.click();
  await expect(main).toHaveClass(/\bpeek\b/);
  await expect.poll(async () => Math.round((await sidebar.boundingBox())!.width)).toBe(272);
  expect(Math.round((await conversation.boundingBox())!.width)).toBe(800);
  await snap(page, "layout-800-sidebar-over");
  await page.locator(".peek-scrim").click({ position: { x: 700, y: 400 } });
  await expect(main).not.toHaveClass(/\bpeek\b/);
  // Choosing from it puts it away too: a page, or a session.
  await showSidebar.click();
  await sidebar.getByRole("button", { name: /^概览/ }).click();
  await expect(main).not.toHaveClass(/\bpeek\b/);
  await expect(page.locator(".peek-scrim")).toHaveCount(0);
  await expect(sidebar).toHaveCount(0);
  await (await sessionList(page)).getByText("写 hello", { exact: true }).click();
  await expect(main).not.toHaveClass(/\bpeek\b/);
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  // The card, asked for here, shows over the conversation too.
  await info.click();
  await expect(card).toBeVisible();
  await snap(page, "layout-800-card-over");
  await page.keyboard.press("Escape");

  // Wide again, both are back beside it as they were.
  await resize(1440);
  await expect(card).toBeVisible();
  expect(await docked()).toEqual({ sidebar: true, card: true });

  // Hidden by the owner, it stays hidden from page to page; the card keeps its place.
  await sidebar.getByRole("button", { name: "隐藏边栏" }).click();
  await resize(1240);
  await expect(card).toBeVisible();
  expect(await docked()).toEqual({ sidebar: false, card: true });
  await app.evaluate(({ Menu }) => Menu.getApplicationMenu()?.getMenuItemById("overview")?.click());
  await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
  await expect(sidebar).toHaveCount(0);
  await showSidebar.click();
  expect(await docked()).toEqual({ sidebar: true, card: false });

  // The window goes no narrower than the conversation alone.
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setSize(300, 800));
  await expect.poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].getSize()[0])).toBe(480);
  await resize(480);
  await snap(page, "layout-480");
  // The overview fits it too: nothing wider than the window.
  await showSidebar.click();
  await sidebar.getByRole("button", { name: /^概览/ }).click();
  await expect(main).not.toHaveClass(/\bpeek\b/);
  // The map scales to its measured width in ResizeObserver, after the page mounts.
  await expect.poll(() => page.evaluate(() => [...document.querySelectorAll(".content *")]
    .filter((e) => e.getBoundingClientRect().right > innerWidth + 1)
    .map((e) => ({ tag: e.tagName, class: e.getAttribute("class"), right: e.getBoundingClientRect().right })))).toEqual([]);
  await snap(page, "layout-480-overview");
  await app.close();
});
