import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { fixture, launch, navTo, snap } from "./app";

// The README's screenshots (e2e TestReadmeScreens, HERMESH_README_DEMO=zh|en): Her on a Mac mini in a demo world,
// whose model and coding agents play a script (e2e/readme_test.go has their words; the owner's are here). She looks
// after an open source project: her daily check finds a crash that users report, she has it reproduced on a PC and
// comes to the owner with it; on one click she has it fixed on the Mac mini, checked on the PC and a server at
// once, fixed again where the PC still fails, checked again, and sends the release notes. Dark, in twice as many
// pixels. It is no test of the app, and runs only then.
const lang = process.env.HERMESH_README_DEMO;
test.skip(!lang, "the README's screenshots (TestReadmeScreens)");

const zh = lang === "zh";
const t = (z: string, e: string) => (zh ? z : e);

test("the README's screenshots", async () => {
  test.setTimeout(8 * 60_000);
  const { app, page } = await launch(fixture.socket, { systemLanguages: [zh ? "zh-Hans" : "en"], dark: true, args: ["--force-device-scale-factor=2"] });
  const size = (width: number, height: number) => app.evaluate(({ BrowserWindow }, s) => BrowserWindow.getAllWindows()[0].setContentSize(s[0], s[1]), [width, height]);
  await size(1180, 760);
  type Main = { hermeshTest: { mainWindow(): { webContents: { send(channel: string, ...args: unknown[]): void } } } };
  // As a window in front: the tests' windows never take focus (background.cjs).
  const front = () => app.evaluate(() => (globalThis as unknown as Main).hermeshTest.mainWindow().webContents.send("window-focus", true));
  const shot = async (name: string, settle = 1200) => {
    await front();
    await page.waitForTimeout(settle);
    await snap(page, name);
  };
  // A picture has no vibrancy behind the sidebar (it comes out pale): its dark as macOS draws it.
  // And the account is the demo's, not this computer's user.
  await page.addStyleTag({
    content: [
      ".side{background:#1d1d21 !important}",
      ".side-foot .who b, .side-foot .av{color:transparent !important;position:relative}",
      '.side-foot .who b::after{content:"Alex";color:var(--l1);position:absolute;inset:0}',
      '.side-foot .av::after{content:"A";color:var(--l2);position:absolute;inset:0;display:grid;place-items:center}',
    ].join("\n"),
  });

  const her = /^Her/;
  const log = page.getByRole("log", { name: t("对话", "Conversation") });
  const composer = page.getByRole("textbox", { name: t("消息", "Message") });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };
  const asking = page.getByRole("region", { name: t("等你批准", "Waiting for your approval") });
  // Allows what Her asks for, as an owner who lets her go on, until done holds.
  const allowUntil = async (done: () => Promise<void>, timeout = 120_000) => {
    await expect(async () => {
      if (await asking.isVisible()) await asking.getByRole("button", { name: zh ? /^允许/ : /^Allow/ }).first().click({ timeout: 2_000 });
      await done();
    }).toPass({ timeout });
  };
  const card = (title: string) => log.getByRole("button", { name: title });
  const said = (text: string) => () => expect(log).toContainText(text, { timeout: 1_000 });
  const shown = (title: string) => () => expect(card(title)).toBeVisible({ timeout: 1_000 });

  // Her is turned on for the Mac mini.
  await page.locator(".side-nav").getByRole("button", { name: her }).click();
  await page.getByRole("listitem").filter({ hasText: "mac-mini" }).getByRole("button").first().click({ timeout: 60_000 });
  await expect(composer).toBeVisible({ timeout: 120_000 });

  // The release notes she sends at the end, in her folder as her model would have written them.
  const notes = t("发布说明.md", "release-notes.md");
  const bots = path.join(fixture.herHome ?? "", "bots");
  const folder = path.join(bots, fs.readdirSync(bots).find((id) => fs.existsSync(path.join(bots, id, "bot.json")))!, "files");
  fs.mkdirSync(folder, { recursive: true });
  fs.writeFileSync(
    path.join(folder, notes),
    t(
      "# tidepool 0.9.4\n\n## 修复\n\n- Windows 上启动崩溃（#218、#220、#221）：配置路径不再手工拼接，盘符路径和 UNC 路径按绝对路径处理\n\n## 感谢\n\n报告这个问题的三位用户，以及在 X 上反馈的各位。\n",
      "# tidepool 0.9.4\n\n## Fixed\n\n- A crash at startup on Windows (#218, #220, #221): the config path is no longer joined by hand, and drive-letter and UNC paths count as absolute\n\n## Thanks\n\nTo the three people who reported it, and to everyone who told us on X.\n",
    ),
  );

  // Said once: she checks every morning. The pictures' window is taller; they are the frames of relay.gif.
  await size(1180, 1000);
  await send(t("帮我盯着 tidepool 的 GitHub 和 X，每天早上巡检一次。", "Keep an eye on tidepool's GitHub and X, and check them every morning."));

  // The morning comes: Run Now, on the task's page, in place of waiting for 9.
  const created = log.getByRole("button", { name: zh ? /已创建定时任务/ : /Created the scheduled task/ });
  await allowUntil(() => expect(created).toBeVisible({ timeout: 1_000 }));
  await expect(log).toContainText(t("有事我来找你", "I'll come to you"), { timeout: 60_000 });
  await created.click();
  const routines = page.getByRole("complementary", { name: t("定时任务", "Scheduled Tasks") });
  const runNow = routines.getByRole("button", { name: t("立即运行", "Run Now") });
  if (!(await runNow.isVisible().catch(() => false))) await routines.locator(".rtn-row").first().click();
  await runNow.click();
  await routines.getByRole("button", { name: t("关闭", "Close") }).first().click();
  await expect(routines).toBeHidden();

  // Her check runs, and she comes to the owner: reproduced on the PC, and a question.
  const fix = t("现在修", "Fix it now");
  await allowUntil(() => expect(log.getByRole("button", { name: fix, exact: true })).toBeVisible({ timeout: 1_000 }));
  await shot("relay-1");

  // One click. The fix, then two checks at once.
  await log.getByRole("button", { name: fix, exact: true }).click();
  const onWindows = t("Windows 验证", "Check on Windows");
  await allowUntil(shown(onWindows));
  await allowUntil(shown(t("Linux 验证", "Check on Linux")));
  await shot("relay-2", 600);
  // The two at work, on the map.
  await size(1180, 760);
  await navTo(page, zh ? /^概览/ : /^Overview/);
  await shot("overview-working", 1500);
  await navTo(page, her);
  await size(1180, 1000);

  // The PC still fails: she passes the log on, and the fix goes on.
  await allowUntil(said(t("我把日志转给 Claude 了", "I passed the log to Claude")));
  await shot("relay-3", 2500);
  await allowUntil(said("#222"), 180_000);
  await expect(page.locator(".side").getByText(t("暂时没有", "None right now"), { exact: true })).toBeVisible({ timeout: 60_000 });
  await shot("relay");
  await size(1180, 760);

  // The check on the PC, on its own page: what failed, and that it passes now.
  await card(onWindows).click();
  await expect(page.getByRole("heading", { name: onWindows })).toBeVisible();
  await shot("session");
  await app.close();
});
