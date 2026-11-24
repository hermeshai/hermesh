import { expect, test, type ElectronApplication, type Locator } from "@playwright/test";
import { launch, mapHeight, snap, thisComputer, sessionList, navTo } from "./app";

test("map height waits for the first resize delivery after navigation", async () => {
  const { app, page } = await launch();
  try {
    await (await sessionList(page)).getByRole("listitem").first().click(); // Unmount the overview before observing its next first layout.
    await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setSize(1009, 800));
    await page.evaluate(() => {
      const real = window.ResizeObserver;
      const deliveries: (() => void)[] = [];
      (window as unknown as { deliverMapResize(): void }).deliverMapResize = () => deliveries.splice(0).forEach((f) => f());
      (window as unknown as { mapResizePending(): number }).mapResizePending = () => deliveries.length;
      window.ResizeObserver = class extends real {
        constructor(callback: ResizeObserverCallback) {
          super((entries, observer) => {
            if (entries.some((e) => e.target.classList.contains("lmap-box"))) deliveries.push(() => callback(entries, observer));
            else callback(entries, observer);
          });
        }
      };
    });
    await navTo(page, /^概览/);
    const map = page.getByRole("region", { name: "链路" });
    await expect(map).toBeVisible();
    const stage = map.locator(".lmap-stage");
    await expect(stage).toHaveJSProperty("offsetWidth", 820);
    expect(await map.locator(".lmap-box").evaluate((el) => el.clientWidth)).toBeLessThan(820);
    await expect.poll(() => page.evaluate(() => (window as unknown as { mapResizePending(): number }).mapResizePending())).toBeGreaterThan(0);
    const measurement = mapHeight(map);
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => {
      (window as unknown as { deliverMapResize(): void }).deliverMapResize();
      resolve();
    })));
    await expect.poll(() => stage.evaluate((el) => el.style.transform)).not.toBe("scale(1)");
    expect(await measurement).toBe((await map.boundingBox())!.height);
  } finally {
    await app.close();
  }
});

// The overview's Live view: what needs the owner, the link map of grants,
// machines and projects, what runs now and what finished last, from the
// fixture's sessions. The numbers are its other views (docs/overview-usage.md).
test("进行中 is a row per session at work: who sent it to whom, the tool in use and the time worked; what waits or failed is under 需要你处理", async () => {
  const { app, page } = await launch();
  await expect(page.getByRole("tab", { name: "实时" })).toHaveAttribute("aria-selected", "true");
  const work = page.getByRole("region", { name: "进行中" });
  const rows = work.getByRole("listitem");
  await expect(rows).toHaveCount(1);
  await expect(work.locator(".sec-h .n")).toHaveText("1");
  const running = rows.filter({ hasText: "跑很久" });
  await expect(running).toContainText("bot → Gemini · demo");
  await expect(running.locator(".tm-when")).toHaveText(/^\d+:\d\d$/);
  // The question and the failures are under 需要你处理 only (docs/her-team.md §14.4), who waits before what failed,
  // and the overview's row says so with a dot.
  for (const title of ["选颜色", "限流", "崩溃"]) await expect(work).not.toContainText(title);
  const needs = page.getByRole("region", { name: "需要你处理" });
  await expect(needs.getByRole("listitem")).toHaveText([/选颜色/, /限流|崩溃/, /限流|崩溃/]);
  await expect(page.getByRole("button", { name: /^概览/ }).getByRole("img", { name: /项需要处理/ })).toBeVisible();
  // What finished last, under it; every session is the sidebar's list, so 查看全部 is there only while it is hidden.
  const finished = page.getByRole("region", { name: "最近完成" });
  await expect(finished.locator(".tm-row")).toHaveText(/写 hello.*2 个文件/);
  await expect(finished.getByRole("button", { name: "查看全部" })).toHaveCount(0);
  await page.getByRole("button", { name: "隐藏边栏" }).click();
  await finished.getByRole("button", { name: "查看全部" }).click();
  await expect(await sessionList(page)).toBeVisible();
  await expect(finished.getByRole("button", { name: "查看全部" })).toHaveCount(0);
  await snap(page, "overview");
  await running.getByRole("button").click();
  await expect(page.getByRole("heading", { name: "跑很久" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "概览" })).toHaveCount(0);
  await app.close();
});

test("pointers are macOS's: the hand on text links only, the arrow on the map's cards; the hub's icon follows light and dark", async () => {
  const { app, page } = await launch();
  const map = page.getByRole("region", { name: "链路" });
  const cursor = (l: ReturnType<typeof page.locator>) => l.evaluate((el) => getComputedStyle(el).cursor);
  for (const name of ["bot", thisComputer, "demo", "+ 连接更多", "+ 添加环境"]) expect(await cursor(map.getByRole("button", { name, exact: true }))).toBe("default");
  expect(await cursor(page.getByRole("button", { name: "管理授权…" }))).toBe("pointer");
  // The session list: a heading the arrow, a text link the hand; then back to the overview.
  expect(await cursor((await sessionList(page)).getByRole("button", { name: /^已结束/ }))).toBe("default");
  expect(await cursor(page.getByRole("button", { name: "显示已归档" }))).toBe("pointer");
  await navTo(page, /^概览/);
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

test("a node clicked on the link map turns 进行中 into the sessions through it, whatever their state, and names the node", async () => {
  const { app, page } = await launch();
  const map = page.getByRole("region", { name: "链路" });
  // The map says who may use what and what goes between them now: no counts, and no line or legend by state.
  await expect(map.getByRole("status")).toHaveCount(0);
  for (const word of ["今天", "tokens", "运行中", "等你回答", "出错", "点击任意节点"]) await expect(map).not.toContainText(word);
  const bot = map.getByRole("button", { name: "bot", exact: true });
  await expect(bot).toHaveText(/bot个人 agent$/); // its grant is not about to expire; no count of what runs
  // Hovering a grant shows where it may work: here, every machine and project.
  await bot.hover();
  await expect(map.locator(".lm-edge.scope")).toHaveCount(2); // hub → this Mac, this Mac → demo
  await snap(page, "overview-scope");
  await page.mouse.move(0, 0);
  await expect(map.locator(".lm-edge.scope")).toHaveCount(0);
  const work = page.getByRole("region", { name: "进行中" });
  const condition = work.getByRole("status");
  const rows = work.getByRole("listitem");
  await expect(rows).toHaveCount(1);

  for (const [name, text] of [
    ["bot", "bot 发起的任务"],
    [thisComputer, `${thisComputer} 上的任务`],
    ["demo", "demo 项目的任务"],
  ]) {
    const node = map.getByRole("button", { name, exact: true });
    await node.click();
    await expect(node).toHaveAttribute("aria-pressed", "true");
    await expect(condition).toContainText(text);
    await expect(condition).toContainText("5 个");
    // Who waits, what failed, what runs, then what finished: each says how it stands.
    await expect(rows).toHaveText([/选颜色.*等你回答/, /出错|已崩溃/, /出错|已崩溃/, /跑很久.*\d+:\d\d/, /写 hello/]);
    await expect(page.getByRole("region", { name: "最近完成" })).toHaveCount(0);
  }
  await page.mouse.move(0, 0);
  await snap(page, "overview-pick");
  // Clicking it again, the hub or 显示全部 goes back to all.
  await map.getByRole("button", { name: "demo", exact: true }).click();
  await expect(condition).toHaveCount(0);
  await expect(page.getByRole("region", { name: "最近完成" })).toBeVisible();
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
  await map.getByRole("button", { name: thisComputer }).click({ button: "right" });
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as { shown: unknown }).shown)).toEqual(["环境设置…"]);
  await app.close();
});

type Shown = { label: string; detail?: string; checked?: boolean; submenu?: Shown[] };

/**
 * Stands in for the system's pop-up menus: the next one shown is kept (labels, second lines, checks and submenus,
 * and where it opened), and the item at `want` (a label, or a label and one in its submenu) is chosen; null: none.
 */
async function menus(app: ElectronApplication) {
  await app.evaluate(({ ipcMain }) => {
    type Item = { id?: string; label?: string; detail?: string; checked?: boolean; submenu?: Item[]; separator?: boolean };
    const g = globalThis as unknown as { shown?: unknown; at?: unknown; want?: string[] | null };
    ipcMain.removeHandler("popup-menu");
    ipcMain.handle("popup-menu", (_e, items: Item[], at: unknown) => {
      const strip = (list: Item[]): unknown[] => list.filter((i) => !i.separator).map((i) => ({ label: i.label, detail: i.detail, checked: i.checked, submenu: i.submenu && strip(i.submenu) }));
      g.shown = strip(items);
      g.at = at;
      let list = items;
      let id: string | null = null;
      for (const label of g.want ?? []) {
        const it = list.find((i) => i.label === label);
        id = it?.id ?? null;
        list = it?.submenu ?? [];
      }
      return id;
    });
  });
  return {
    /** Opens the menu of the pop-up button and chooses want in it; what the menu showed. */
    async open(button: Locator, want: string[] | null): Promise<Shown[]> {
      await app.evaluate((_, want) => {
        const g = globalThis as unknown as { shown?: unknown; want?: string[] | null };
        g.shown = undefined;
        g.want = want;
      }, want);
      await button.click();
      await expect.poll(() => app.evaluate(() => (globalThis as unknown as { shown?: unknown }).shown !== undefined)).toBe(true);
      return app.evaluate(() => (globalThis as unknown as { shown: Shown[] }).shown);
    },
    at: () => app.evaluate(() => (globalThis as unknown as { at: { x: number; y: number } }).at),
  };
}

// 用量, the overview's other view (docs/overview-usage.md §6): the fixture's turns, counted by the machine and read
// by hermeshd (QueryWork) under the filters of the row at its top.
test("用量 counts a range under its filters: a measure a row beside its chart, the menus' choices with their work, and a year of days", async () => {
  const { app, page } = await launch();
  try {
    const usage = page.getByRole("region", { name: "用量" });
    await expect(usage).toHaveCount(0); // Live asks for no counts
    await page.getByRole("tab", { name: "用量" }).click();
    await expect(page.getByRole("region", { name: "链路" })).toHaveCount(0);
    const menu = await menus(app);

    // Seven days ending today, everything: the tasks that worked and their tokens, each beside its chart.
    const range = usage.getByRole("button", { name: "时间范围" });
    await expect(range).toHaveText("7 天");
    const tasks = usage.getByRole("group", { name: "任务" });
    const tokens = usage.getByRole("group", { name: "Token" });
    await expect(tasks.locator(".us-big")).toHaveText(/^[1-9]\d*个任务运行过$/);
    const ran = Number((await tasks.locator(".us-big b").textContent())!);
    await expect(tasks.locator(".us-facts")).toHaveText(/^已完成\d+出错\d+ 轮工作时长.+$/);
    await expect(tokens.locator(".us-big")).toHaveText(/^\d+(\.\d)?[KM]?token$/);
    await expect(tokens.locator(".us-facts")).toHaveText(/^输入\d+(\.\d)?[KM]?输出\d+(\.\d)?[KM]?$/);
    // Nothing ran before today: no change to say, and today's is the only bar, named under the chart.
    await expect(tasks.locator(".us-delta")).toHaveText(" ");
    await expect(tasks.locator(".us-slots i")).toHaveCount(7);
    await expect(tasks.locator(".us-x span.now")).toHaveText("今天");

    // The bars are in parts of the agents to start with (分组): a part an agent, in its own colour, named over the chart
    // with its number and share; the left side's figures stay. Here one agent did it all.
    const group = usage.getByRole("radiogroup", { name: "分组" });
    await expect(group.getByRole("radio")).toHaveText(["不分组", "Agent 提供方", "环境 / 项目"]);
    await expect(group.getByRole("radio", { name: "Agent 提供方" })).toHaveAttribute("aria-checked", "true");
    const legend = tasks.locator(".us-legend").getByRole("button");
    await expect(legend).toHaveText([`Gemini CLI${ran}100%`]);
    await expect(tokens.locator(".us-legend").getByRole("button")).toHaveText([/^Gemini CLI\d+(\.\d)?[KM]?100%$/]);
    await expect(tasks.locator(".us-facts")).toHaveText(/^已完成\d+出错\d+ 轮工作时长.+$/);
    const part = tasks.locator(".us-slots .us-bar u");
    await expect(part).toHaveCount(1);
    expect(await part.evaluate((el) => el.style.background)).toBe("var(--us-s3)"); // Gemini's, the third coding agent
    // A bar's top is barely rounded, as the cards' corners are.
    expect(await tasks.locator(".us-bar").evaluate((el) => getComputedStyle(el).borderTopLeftRadius)).toBe("3px");
    await tasks.locator(".us-slots i").last().hover();
    await expect(tasks.locator(".u-tip")).toHaveText(new RegExp(`^${ran} 个任务运行过.*周.*Gemini CLI${ran}$`));
    await snap(page, "overview-usage");
    // Clicked in the legend a part is seen alone in both charts; the page's filters and numbers stay. Again, all.
    await legend.first().click();
    await expect(legend.first()).toHaveAttribute("aria-pressed", "true");
    await tasks.locator(".us-slots i").last().hover();
    await expect(tasks.locator(".u-tip")).toHaveText(new RegExp(`^${ran} 个任务运行过Gemini CLI · .*周.$`));
    await expect(usage.getByRole("button", { name: "Agent 提供方" })).toHaveText("Agent 提供方");
    await expect(tasks.locator(".us-big b")).toHaveText(String(ran));
    await legend.first().click();
    await expect(legend.first()).toHaveAttribute("aria-pressed", "false");
    // By where: one environment worked, so its projects are the parts.
    await group.getByRole("radio", { name: "环境 / 项目" }).click();
    await expect(legend).toHaveText([`demo${ran}100%`]);
    // The total: a bar a bucket, today's in colour, as before there were parts.
    await group.getByRole("radio", { name: "不分组" }).click();
    await expect(tasks.locator(".us-legend")).toHaveCount(0);
    await expect(tasks.locator(".us-slots u")).toHaveCount(1);
    await expect(tasks.locator(".us-slots i.now u")).toHaveCount(1);
    expect(await tasks.locator(".us-slots u").evaluate((el) => getComputedStyle(el).borderTopLeftRadius)).toBe("3px");
    await tasks.locator(".us-slots i.now").hover();
    await expect(tasks.locator(".u-tip")).toHaveText(new RegExp(`^${ran} 个任务运行过.*周`));
    await tokens.locator(".us-slots i.now").hover();
    await expect(tokens.locator(".u-tip")).toHaveText(/^\d+(\.\d)?[KM]? token/);
    await snap(page, "overview-usage-total");

    // A filter's menu is the system's, under its button: each choice with its work in the range, the chosen checked.
    const agent = usage.getByRole("button", { name: "Agent 提供方" });
    const counts = new RegExp(`^${ran} 个任务 · \\d+(\\.\\d)?[KM]? token$`);
    let shown = await menu.open(agent, null);
    expect(shown.map((i) => i.label)).toEqual(["全部", "Gemini CLI"]);
    expect(shown[0].checked).toBe(true);
    expect(shown[1].detail).toMatch(counts);
    const box = (await agent.boundingBox())!;
    const at = await menu.at();
    expect(Math.abs(at.x - box.x)).toBeLessThanOrEqual(1);
    expect(at.y).toBeGreaterThanOrEqual(box.y + box.height);
    await expect(agent).toHaveText("Agent 提供方"); // nothing chosen: nothing changed
    await menu.open(agent, ["Gemini CLI"]);
    await expect(agent).toHaveText("Gemini CLI");
    await expect(agent).toHaveClass(/\bon\b/);
    await expect(tasks.locator(".us-big b")).toHaveText(String(ran)); // the only agent that worked
    shown = await menu.open(agent, null);
    expect(shown.map((i) => i.checked)).toEqual([false, true]);

    // Where is one choice of two levels: an environment opens its projects, its whole the first line inside.
    const place = usage.getByRole("button", { name: "环境 / 项目" });
    shown = await menu.open(place, [thisComputer, "demo"]);
    expect(shown.map((i) => i.label)).toEqual(["全部", thisComputer]);
    expect(shown[1].detail).toMatch(counts);
    expect(shown[1].submenu!.map((i) => i.label)).toEqual(["全部项目", "demo"]);
    expect(shown[1].submenu![1].detail).toMatch(counts);
    await expect(place).toHaveText(`${thisComputer}/demo`);
    await expect(tasks.locator(".us-big b")).toHaveText(String(ran));
    shown = await menu.open(place, [thisComputer, "全部项目"]);
    expect(shown[1].submenu!.map((i) => i.checked)).toEqual([false, true]);
    await expect(place).toHaveText(thisComputer);
    const sender = usage.getByRole("button", { name: "发起方" });
    shown = await menu.open(sender, ["bot"]);
    expect(shown.map((i) => i.label)).toEqual(["全部", "bot"]);
    await expect(sender).toHaveText("bot");
    // All back to everything.
    await menu.open(agent, ["全部"]);
    await menu.open(place, ["全部"]);
    await menu.open(sender, ["全部"]);
    for (const [b, text] of [[agent, "Agent 提供方"], [place, "环境 / 项目"], [sender, "发起方"]] as const) {
      await expect(b).toHaveText(text);
      await expect(b).not.toHaveClass(/\bon\b/);
    }

    // Today is by hour, up to now; the choice is kept while the overview shows its other view.
    shown = await menu.open(range, ["今天"]);
    expect(shown.map((i) => [i.label, i.checked])).toEqual([["今天", false], ["7 天", true], ["30 天", false]]);
    await expect(range).toHaveText("今天");
    await expect(tasks.locator(".us-slots i")).toHaveCount(24);
    await expect(tasks.locator(".us-x span.now")).toHaveText("现在");
    await expect(tasks.locator(".us-big b")).toHaveText(String(ran));
    await page.getByRole("tab", { name: "实时" }).click();
    await page.getByRole("tab", { name: "用量" }).click();
    await expect(range).toHaveText("今天");
    await expect(group.getByRole("radio", { name: "不分组" })).toHaveAttribute("aria-checked", "true");

    // A year of tokens by day, under the filters: today's is the one day with any.
    const year = usage.getByRole("group", { name: "近一年" });
    await expect(year).toContainText("活跃天数 1 · 连续使用 1 天");
    await expect(year.getByRole("img", { name: /^近一年 \d+(\.\d)?[KM]? tokens，1 天有用量$/ })).toBeVisible();
    await year.locator(".us-cells div:not(.none):not(.l0)").hover();
    await expect(year.locator(".u-tip")).toHaveText(/^\d+(\.\d)?[KM]? token.*周/);
    await expect(usage).toContainText("输入含各提供方的缓存读写。");

    // In a narrow window a chart goes under its number, and nothing runs off the side.
    await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setContentSize(900, 900));
    await expect.poll(async () => (await tasks.locator(".us-plot").boundingBox())!.y - (await tasks.locator(".us-lead").boundingBox())!.y).toBeGreaterThan(100);
    expect(await page.locator(".ov").evaluate((el) => el.parentElement!.scrollWidth - el.parentElement!.clientWidth)).toBeLessThanOrEqual(1);
    await snap(page, "overview-usage-narrow");
  } finally {
    await app.close();
  }
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
  const first = await centre(page.locator(".side-nav .nav-i").first());
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
