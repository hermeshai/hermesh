import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launch } from "./app";

// The app's language: the first of the system's preferred languages it has
// (English or Simplified Chinese), else English; or the one chosen in
// Settings › General, from the next start. The other specs run it in Chinese.

// Set by the page's script once it runs (index.html starts out in English).
const lang = (page: Page) => page.evaluate(() => document.documentElement.lang);
const menus = (app: ElectronApplication) => app.evaluate(({ Menu }) => Menu.getApplicationMenu()?.items.map((i) => i.label) ?? []);

test("on an English system the app is in English, menus and pages", async () => {
  const { app, page } = await launch(undefined, { systemLanguages: ["en-GB", "zh-Hans"] });
  await expect.poll(() => lang(page)).toBe("en");
  await expect.poll(() => menus(app)).toEqual(["Hermesh", "File", "Edit", "View", "Session", "Window", "Help"]);
  await expect(page.getByRole("list", { name: "Sessions" })).toContainText("写 hello"); // a session's title stays as written
  await expect(page.getByRole("heading", { name: "Overview", level: 1 })).toBeVisible();
  await app.close();
});

test("the first preferred language the app has wins; Traditional Chinese isn't Simplified", async () => {
  for (const [system, want] of [
    [["zh-Hant-TW", "en"], "en"],
    [["fr-FR", "zh-Hans-CN", "en"], "zh-CN"],
    [["ja"], "en"],
  ] as const) {
    const { app, page } = await launch(undefined, { systemLanguages: [...system] });
    await expect.poll(() => lang(page), { message: system.join(", ") }).toBe(want);
    await app.close();
  }
});

test("a language chosen in settings takes over from the next start", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  const first = await launch(undefined, { systemLanguages: ["en"], profile });
  const [settings] = await Promise.all([first.app.waitForEvent("window"), first.page.evaluate(() => window.hermesh?.openSettings())]);
  await settings.waitForLoadState();
  const general = settings.getByRole("main", { name: "General" });
  const select = general.getByRole("combobox", { name: "Language" });
  await expect(select).toHaveValue("system");
  await expect(select.getByRole("option", { name: "System (English)" })).toBeAttached();
  await expect(general.getByRole("button", { name: "Restart Now" })).toHaveCount(0);

  await select.selectOption("zh-CN");
  await expect(general.getByText("Takes effect after Hermesh restarts.")).toBeVisible();
  await expect(general.getByRole("button", { name: "Restart Now" })).toBeVisible();
  expect((await first.page.evaluate(() => window.hermesh?.getPreferences()))?.language).toBe("zh-CN");
  // Still English until then.
  await expect.poll(() => lang(settings)).toBe("en");
  await first.app.close();

  const second = await launch(undefined, { systemLanguages: ["en"], profile });
  await expect.poll(() => lang(second.page)).toBe("zh-CN");
  await expect.poll(() => menus(second.app)).toContain("文件");
  await expect(second.page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  await second.app.close();
});
