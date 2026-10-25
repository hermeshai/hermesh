import { expect, test } from "@playwright/test";
import { launch, snap } from "./app";

// The overview: the link map of grants, machines and projects, what runs
// now (questions and failures first) and a year of usage, from the fixture's
// sessions. Sessions stay out of "需要你处理": the sidebar lists them.
test("正在干活 is a card per live session: status, time worked, who started it and where, questions and failures first", async () => {
  const { app, page } = await launch();
  const work = page.getByRole("region", { name: "正在干活" });
  const cards = work.getByRole("listitem");
  await expect(cards).toHaveCount(4);
  await expect(cards.first()).toContainText("选颜色");
  await expect(cards.first()).toContainText("等你回答");
  await expect(cards.filter({ hasText: "限流" })).toContainText("出错");
  await expect(cards.filter({ hasText: "崩溃" })).toContainText("已崩溃");
  const running = cards.filter({ hasText: "跑很久" });
  await expect(running).toContainText("运行中");
  await expect(running.locator(".wc-time")).toHaveText(/^\d+:\d\d$/);
  await expect(running.locator(".wc-foot")).toHaveText(/bot→这台 Mac\/demo$/); // after the grant's avatar
  // What is not asked for is not there: no coding-agent counts, no tool phase.
  await expect(work).not.toContainText("Gemini CLI");
  await expect(work).not.toContainText("sleep 1800");
  // Nothing but sessions waits on the owner here: no "需要你处理", no count on 概览.
  await expect(page.getByRole("region", { name: "需要你处理" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /概览/ })).toContainText("⌘1");
  await snap(page, "overview");
  await cards.first().click();
  await expect(page.getByRole("heading", { name: "选颜色" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "概览" })).toHaveCount(0);
  await app.close();
});

test("pointers are macOS's: the hand on text links only, the arrow on the map's cards; the hub's icon follows light and dark", async () => {
  const { app, page } = await launch();
  const map = page.getByRole("region", { name: "链路" });
  const cursor = (l: ReturnType<typeof page.locator>) => l.evaluate((el) => getComputedStyle(el).cursor);
  for (const name of ["bot", "这台 Mac", "demo", "+ 连接更多", "+ 添加机器"]) expect(await cursor(map.getByRole("button", { name, exact: true }))).toBe("default");
  expect(await cursor(page.getByRole("list", { name: "会话" }).getByRole("button", { name: /^需要处理/ }))).toBe("default");
  expect(await cursor(page.getByRole("button", { name: "管理授权…" }))).toBe("pointer");
  expect(await cursor(page.getByRole("button", { name: "显示已归档" }))).toBe("pointer");
  const icon = map.locator(".lmap-icon");
  expect(await icon.evaluate((el) => (el as HTMLElement).offsetWidth)).toBe(72); // before scaling to the window
  const look = () => icon.evaluate((el) => getComputedStyle(el).backgroundImage + getComputedStyle(el.querySelector(".hm-beam")!).mixBlendMode);
  await page.emulateMedia({ colorScheme: "light" });
  const light = await look();
  expect(light).toContain("rgb(255, 255, 255)");
  expect(light).toContain("multiply");
  await snap(page, "overview-hub-light");
  await page.emulateMedia({ colorScheme: "dark" });
  const dark = await look();
  expect(dark).toContain("rgb(38, 40, 51)");
  expect(dark).toContain("plus-lighter");
  await snap(page, "overview-hub-dark");
  await app.close();
});

test("a node clicked on the link map narrows 正在干活, names the condition and marks it on each card", async () => {
  const { app, page } = await launch();
  const map = page.getByRole("region", { name: "链路" });
  // The headline: today's sessions, how they ended, and today's tokens (the same as 用量's 今天).
  const today = map.getByRole("status", { name: "今天" });
  await expect(today).toHaveText(/^今天 6 个会话 · 完成 2 · 出错 2 · \d+ tokens$/);
  const tokens = (await today.textContent())!.match(/(\d+) tokens/)![1];
  await expect(page.getByRole("region", { name: "用量" }).locator(".u-kpi").first()).toContainText(tokens);
  const bot = map.getByRole("button", { name: "bot", exact: true });
  await expect(bot).toContainText("个人 agent"); // its grant is not about to expire
  // Hovering a grant shows where it may work: here, every machine and project.
  await bot.hover();
  await expect(map).toContainText("它能用的机器和项目");
  await expect(map.locator(".lm-edge.scope")).toHaveCount(2); // hub → this Mac, this Mac → demo
  await snap(page, "overview-scope");
  await page.mouse.move(0, 0);
  await expect(map.locator(".lm-edge.scope")).toHaveCount(0);
  for (const legend of ["运行中", "等你回答", "出错"]) await expect(map).toContainText(legend);
  const work = page.getByRole("region", { name: "正在干活" });
  const condition = work.getByRole("status");
  const cards = work.getByRole("listitem");

  for (const [name, text, mark] of [
    ["bot", "bot 发起的会话", "bot"],
    ["这台 Mac", "这台 Mac 上的会话", "这台 Mac"],
    ["demo", "demo 项目的会话", "demo"],
  ]) {
    const node = map.getByRole("button", { name, exact: true });
    await node.click();
    await expect(node).toHaveAttribute("aria-pressed", "true");
    await expect(condition).toContainText(text);
    await expect(condition).toContainText("4 个");
    await expect(cards).toHaveCount(4);
    await expect(cards.first().locator(".hit")).toHaveText(new RegExp(`${mark}$`));
  }
  await page.mouse.move(0, 0);
  await snap(page, "overview-pick");
  // Clicking it again, the hub or 显示全部 goes back to all.
  await map.getByRole("button", { name: "demo", exact: true }).click();
  await expect(condition).toHaveCount(0);
  await bot.click();
  await map.locator(".lmap-hub").click();
  await expect(condition).toHaveCount(0);
  await bot.click();
  await condition.getByRole("button", { name: "显示全部" }).click();
  await expect(condition).toHaveCount(0);
  await expect(bot).toHaveAttribute("aria-pressed", "false");

  // A machine's settings are in its context menu now that a click drills down.
  await app.evaluate(({ ipcMain }) => {
    ipcMain.removeHandler("popup-menu");
    ipcMain.handle("popup-menu", (_e, items: { label?: string }[]) => ((globalThis as unknown as { shown: unknown }).shown = items.map((i) => i.label), null));
  });
  await map.getByRole("button", { name: "这台 Mac" }).click({ button: "right" });
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as { shown: unknown }).shown)).toEqual(["机器设置…"]);
  await app.close();
});

// 用量: the fixture's turns, added up by the machine and hermeshd (GetUsage), on today's cell.
test("用量 shows a year of tokens with today's turns", async () => {
  const { app, page } = await launch();
  const usage = page.getByRole("region", { name: "用量" });
  await expect(usage).toContainText("近一年 · tokens");
  await expect(usage.getByRole("img", { name: /^近一年 \d+(\.\d)?[KM]? tokens，1 天有用量$/ })).toBeVisible();
  await expect(usage).toContainText("Gemini 100%");
  await expect(usage).toContainText("连续使用1 天");
  await usage.locator(".u-cells .today").hover();
  await expect(usage).toContainText(/\d+ tokens · \d+ 个会话/);
  await snap(page, "overview-usage");
  await app.close();
});

// One line across the top, as in Apple Music: the title and the sidebar's
// first button are centred on the same height. Adding lives in the sidebar.
test("the overview's title and the sidebar's first button share a centre line", async () => {
  const { app, page } = await launch();
  const centre = async (l: ReturnType<typeof page.locator>) => {
    const b = (await l.boundingBox())!;
    return b.y + b.height / 2;
  };
  const title = await centre(page.getByRole("heading", { name: "概览", level: 1 }));
  const first = await centre(page.getByRole("button", { name: /概览/ }));
  console.log("centres", { title, first });
  expect(Math.abs(title - first)).toBeLessThanOrEqual(1);
  await expect(page.getByRole("button", { name: "新建", exact: true })).toHaveCount(0);
  await app.close();
});

test("the overview stays centered when resizing the window or hiding the sidebar", async () => {
  const { app, page } = await launch();
  try {
    await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
    for (const width of [980, 1360]) {
      await app.evaluate(({ BrowserWindow }, width) => BrowserWindow.getAllWindows()[0].setContentSize(width, 900), width);
      for (const sidebar of [true, false]) {
        if (!sidebar) await page.getByRole("button", { name: "隐藏边栏" }).click();
        await page.locator(".mainwrap").evaluate((el) => Promise.all(el.getAnimations().map((animation) => animation.finished)));
        await expect.poll(() => page.locator(".ov").evaluate((el) => {
          const scroll = el.parentElement!;
          const box = el.getBoundingClientRect();
          const left = box.left - scroll.getBoundingClientRect().left;
          const right = scroll.clientWidth - left - box.width;
          return Math.abs(left - right);
        })).toBeLessThanOrEqual(1);
        const layout = await page.locator(".ov").evaluate((el) => ({
          width: el.getBoundingClientRect().width,
          available: el.parentElement!.clientWidth,
          overflow: el.parentElement!.scrollWidth - el.parentElement!.clientWidth,
        }));
        expect(layout.width).toBeCloseTo(Math.min(980, layout.available), 0);
        expect(layout.overflow).toBeLessThanOrEqual(1);
        await snap(page, `overview-centered-${width}-${sidebar ? "sidebar" : "no-sidebar"}`);
        if (!sidebar) await page.getByRole("button", { name: "显示边栏" }).click();
      }
    }
  } finally {
    await app.close();
  }
});
