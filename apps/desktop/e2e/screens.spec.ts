import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { fixture, launch, onWindows, snap } from "./app";

// The website's download page shows the app in a demo world (e2e
// TestScreens, HERMESH_SCREENS_DEMO=zh|en), on a Mac or a PC: the overview, a finished
// session, an agent's request waiting at the bottom right, and a question
// the agent waits on,
// dark, at 1440×900. It is no test of the app, and runs only then.
const lang = process.env.HERMESH_SCREENS_DEMO;
test.skip(!lang, "the download page's screenshots (TestScreens)");

const zh = lang === "zh";
const system = onWindows ? "windows" : "mac";
const t = (z: string, e: string) => (zh ? z : e);

test("the download page's screenshots", async () => {
  test.setTimeout(5 * 60_000);
  const { app, page } = await launch(fixture.socket, { systemLanguages: [zh ? "zh-Hans" : "en"], dark: true });
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setContentSize(1440, 900));
  type Main = { hermeshTest: { mainWindow(): { webContents: { send(channel: string, ...args: unknown[]): void } } } };
  // As a window in front: the tests' windows never take focus (background.cjs).
  const front = () => app.evaluate(() => (globalThis as unknown as Main).hermeshTest.mainWindow().webContents.send("window-focus", true));
  const sessions = page.getByRole("list", { name: t("会话", "Sessions") });
  const open = async (title: string) => {
    await sessions.getByText(title, { exact: true }).click();
    await expect(page.getByRole("heading", { name: title })).toBeVisible();
  };

  // A picture has no vibrancy behind the sidebar (it comes out pale): its dark as macOS draws it.
  await page.addStyleTag({ content: ".side{background:#1d1d21 !important}" });
  await expect(sessions.getByText(t("给设置页加深色模式", "Dark mode for Settings"), { exact: true })).toBeVisible();
  await front();
  await page.waitForTimeout(1500); // the overview's numbers and map settle
  await snap(page, `${system}-overview`);

  await open(t("修复登录按钮点击无反应 #142", "Fix Sign in doing nothing #142"));
  await front();
  await snap(page, `${system}-session`);

  // ChatGPT asks to connect: its request waits at the bottom right.
  const status = await page.evaluate(async () => (await fetch("app://hermesh/api/hermesh.local.v1.LocalService/GetStatus", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" })).json());
  const env = Object.fromEntries(fixture.cliEnv.map((kv) => kv.split(/=(.*)/s).slice(0, 2)));
  const login = spawn(fixture.cli, ["auth", "login", status.connectAddress, "--name", "ChatGPT"], {
    env: { ...process.env, ...env, HERMESH_HOME: fs.mkdtempSync(path.join(os.tmpdir(), "hmchatgpt-")) },
    stdio: "ignore",
  });
  try {
    await open(t("审查 PR #88：结账改用新支付接口", "Review PR #88: new payments API"));
    await expect(page.getByText("ChatGPT").first()).toBeVisible({ timeout: 30_000 });
    await front();
    await page.waitForTimeout(800);
    await snap(page, `${system}-request`);
  } finally {
    login.kill();
  }

  await open(t("升级到 React 19", "Upgrade to React 19"));
  await front();
  await page.waitForTimeout(600);
  await snap(page, `${system}-question`);
  await app.close();
});
