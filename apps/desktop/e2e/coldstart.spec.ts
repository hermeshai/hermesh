import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, gitRepo, launch, pickAnswers, snap, sys } from "./app";

// The first run on a computer that can run Her and belongs to no one (docs/cold-start-prd.md §5,
// TestDesktopColdStart): welcome, this Mac's agent providers and a project, Her turned on with no
// Touch ID and given a model by its key, the example she plays, the sign-in step skipped, done, and
// the main window without an account, Her ready there. Then the same window, opened again, goes
// straight to the main window; and with reduced motion nothing moves.

test.describe.configure({ timeout: 240_000, mode: "serial" });

const daemon = createClient(LocalService, createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", nodeOptions: { socketPath: fixture.fresh[0].socket } }));
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmcold-"));
const width = (app: ElectronApplication) => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0);
/** How many pixels of the particles' canvas are drawn in a colour (the ambient lights are white). */
const colouredParticles = (page: Page) =>
  page.locator(".fr-fx").evaluate((c: HTMLCanvasElement) => {
    const d = c.getContext("2d")?.getImageData(0, 0, c.width, c.height).data;
    let n = 0;
    for (let i = 0; d && i < d.length; i += 4) if (d[i + 3] > 80 && d[i + 2] > d[i] + 60) n++;
    return n;
  });
/** Whether a part of the page is drawn moving (its animation runs). */
const moving = (page: Page, selector: string) => page.locator(selector).first().evaluate((el) => getComputedStyle(el).animationName !== "none");

test("without an account: agents, a project, Her and her model, the sign-in left for later, the main window", async () => {
  const { app, page } = await launch(fixture.fresh[0].socket, { profile, start: true });
  // A first start shows the window at the first run's size, never the main window's first (no jump).
  const shown: number[] = [];
  for (let i = 0; i < 30; i++) {
    shown.push(await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().filter((w) => w.isVisible()).map((w) => w.getBounds().width)[0] ?? 0));
    await page.waitForTimeout(50);
  }
  expect(shown.filter((w) => w > 0)).toEqual(shown.filter((w) => w > 0).map(() => 720));

  // The mark gathers from particles of the aurora's colours before it is drawn (the canvas is attached in time).
  await expect.poll(() => colouredParticles(page), { timeout: 1_500, intervals: [100] }).toBeGreaterThan(50);

  // 1 Welcome: one line, one button; nothing about accounts or servers.
  await expect(page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("Her 是住在你电脑上的私人助理，带着一支跨机 Agent 团队。")).toBeVisible();
  await expect(page.getByText("登录")).toHaveCount(0);
  await expect(page.getByText("服务器")).toHaveCount(0);
  await expect.poll(() => width(app)).toBe(720);
  // The window is in ink whatever the appearance: near black, the one button white.
  expect(await page.locator(".fr-stage").evaluate((el) => getComputedStyle(el).backgroundColor)).toBe("rgb(9, 9, 11)");
  const start = page.getByRole("button", { name: "开始设置" });
  await expect(start).toBeVisible();
  expect(await start.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe("rgb(255, 255, 255)");
  await snap(page, "cold-01-welcome");
  await start.click();

  // 2 Agent providers: looked for, then the installed ones, and a project.
  await expect(page.getByRole("heading", { name: "正在查找 Agent 提供方…" })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^已找到 \d+ 个 Agent 提供方$/ })).toBeVisible({ timeout: 30_000 });
  const agents = page.getByRole("list", { name: "Agent 提供方" });
  await expect(agents.getByRole("listitem", { name: "Claude Code" })).toContainText("已登录");
  // Each provider one row across the list: its logo, name and state side by side (not an icon's box).
  const row = (await agents.getByRole("listitem", { name: "Claude Code" }).boundingBox())!;
  const list = (await agents.boundingBox())!;
  expect(row.width).toBeGreaterThan(list.width - 4);
  expect(row.height).toBeLessThan(70);
  await expect(agents.getByRole("listitem", { name: "Codex" })).toContainText("还没登录");
  await expect(agents.getByRole("listitem", { name: "Codex" }).getByRole("button", { name: "在终端运行" })).toBeVisible();
  await expect(page.getByRole("button", { name: "继续" })).toBeDisabled(); // no project yet, and it says so
  await expect(page.getByRole("list", { name: "项目" })).toContainText("添加一个项目后即可继续，Her 会在项目里给 Agent 派活。");
  await pickAnswers(app, gitRepo("cold-app"));
  await page.getByRole("button", { name: "添加项目…" }).click();
  await expect(page.getByRole("list", { name: "项目" }).getByRole("listitem", { name: "cold-app" })).toBeVisible();
  await snap(page, "cold-02-team");
  await page.getByRole("button", { name: "继续" }).click();

  // 3 Her: on at once, with no Touch ID; her example plays.
  await expect(page.getByRole("heading", { name: "Her 带着一支 Agent 团队为你工作" })).toBeVisible();
  await expect.poll(async () => (await daemon.getStatus({})).initialized).toBe(false);
  const show = page.getByLabel("示例").first();
  await expect(show).toBeVisible();
  const said = page.locator(".fr-say");
  await expect(said).toHaveText("今天巡检有一件事：GitHub 上 3 个 issue、X 上 9 个人，说的是同一个崩溃。");
  await expect(said).toHaveText("我先让 Windows 电脑上的 Codex 复现。", { timeout: 5_000 }); // the next frame, 2.1s on
  await expect(page.getByRole("tab", { name: "主动巡检" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "跨机接力" }).click();
  await expect(said).toHaveText("好，交给这台 Mac 上的 Claude Code 修。");
  await expect(show.getByLabel("Claude Code")).toContainText("运行中");
  await snap(page, "cold-03-her");

  // Her's model by its key: the providers people know, the key, checked by her.
  const key = page.getByRole("button", { name: "填写 API Key" });
  // Until her runs the buttons wait, and the page says why.
  const waiting = await page.locator(".fr-pick").evaluate((el) => [!!el.querySelector(".fr-way:disabled"), el.querySelector(":scope > span")?.textContent]);
  if (waiting[0]) expect(waiting[1]).toBe("正在启动 Her…");
  await expect(key).toBeEnabled({ timeout: 60_000 }); // once her runs
  await expect(page.getByText("密钥和登录信息只保存在这台 Mac 上。")).toBeVisible();
  await key.click();
  const sheet = page.getByRole("dialog");
  await expect(sheet.getByText("用哪家的？")).toBeVisible();
  await sheet.getByRole("listitem").filter({ hasText: "DeepSeek" }).click();
  // This Mac by its own name, as in Settings, not its machine's short one.
  const computer = await page.evaluate(() => window.hermesh!.computerName());
  await expect(sheet).toContainText(`它只存进 ${computer} 的凭据库`);
  await sheet.getByLabel(/API Key$/).fill("sk-cold-start");
  await snap(page, "cold-03-key");
  await sheet.getByRole("button", { name: "保存" }).click();

  // She wakes, and says so.
  await expect(page.getByLabel("你好，我是 Her。")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByLabel("已准备就绪，随时可以开始。")).toBeVisible();
  await expect(page.getByText(/^当前模型：.+ · 可在助手设置中更换$/)).toBeVisible();
  await snap(page, "cold-04-awake");
  await page.getByRole("button", { name: "继续" }).click();

  // 4 Sign in: the network at work, and later is fine.
  await expect(page.getByRole("heading", { name: "登录，接入 Hermesh 网络" })).toBeVisible();
  await expect(page.locator(".fr-bubble.up")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText("两两互通", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "使用自己的服务器…" })).toBeVisible();
  await snap(page, "cold-05-network");
  await page.getByRole("button", { name: "暂不登录" }).click();

  // 5 Done, and in.
  await expect(page.getByRole("heading", { name: "设置完成" })).toBeVisible();
  await expect(page.getByText("Her 已准备好为你工作。")).toBeVisible();
  // The confetti falls behind the words and the button.
  expect(await page.locator(".fr-fx").evaluate((el) => getComputedStyle(el).zIndex)).toBe("0");
  await snap(page, "cold-06-done");
  await page.getByRole("button", { name: "进入 Hermesh" }).click();
  await expect.poll(() => width(app)).toBeGreaterThan(900);
  await expect(page.locator(".side-nav")).toBeVisible();
  // No account: said under the name; Her answers on this Mac.
  await expect(page.locator(".side-foot")).toContainText("未登录");
  await expect(page.getByText(/好了，我会用 .+ 和你聊，想换可以去助手设置。有什么要我做的？/)).toBeVisible({ timeout: 30_000 });
  const status = await daemon.getStatus({});
  expect(status.initialized).toBe(false);
  expect(status.localMachineEnabled).toBe(true);
  expect(status.relayHealth?.phase).toBe("local");
  // What needs an account says so, and offers to sign in.
  await page.locator(".side-foot").click();
  await page.getByRole("menuitem", { name: "添加环境…" }).click();
  const need = page.getByRole("dialog", { name: "添加环境需要登录" });
  await expect(need).toBeVisible();
  await expect(need).toContainText(sys("登录 Hermesh 账号后，其他设备和 agent 才能连接这台 Mac。"));
  await snap(page, "cold-07-needs-account");
  await need.getByRole("button", { name: "取消" }).click();
  await expect(need).toBeHidden();
  await app.close();

  // Opened again: the main window, no first run.
  const again = await launch(fixture.fresh[0].socket, { profile, start: true });
  await expect(again.page.locator(".side-nav")).toBeVisible({ timeout: 30_000 });
  await expect(again.page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toHaveCount(0);
  await again.app.close();
});

test("with reduced motion, the first run stands still", async () => {
  // A new window of its own (no first run done there yet), on the same computer.
  const { app, page } = await launch(fixture.fresh[0].socket, { profile: fs.mkdtempSync(path.join(os.tmpdir(), "hmcold-")), start: true });
  // Before anything moves: the page loaded again under the setting.
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.reload();
  // This window's profile has not been through the first run, and the computer belongs to no one: it shows.
  await expect(page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("button", { name: "开始设置" })).toBeVisible();
  expect(await moving(page, ".fr-aurora")).toBe(false);
  expect(await moving(page, ".fr-page .in")).toBe(false);
  // Nothing drawn on the particles' canvas.
  const drawn = await page.locator(".fr-fx").evaluate((c: HTMLCanvasElement) => {
    const ctx = c.getContext("2d");
    if (!ctx || !c.width) return 0;
    const d = ctx.getImageData(0, 0, c.width, c.height).data;
    let n = 0;
    for (let i = 3; i < d.length; i += 4) if (d[i]) n++;
    return n;
  });
  expect(drawn).toBe(0);
  await app.close();
});
