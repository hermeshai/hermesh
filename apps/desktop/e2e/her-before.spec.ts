import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap } from "./app";

// A machine whose Hermesh is from before Her (docs/version-skew.md §6.7): old-box answers, without Her in its
// status. Run by TestDesktopHerBeforeHer. Where Her runs shows it, saying its Hermesh is older and needs an update
// there, with no Turn On, and so does its switch in Settings › Machines, disabled. Updated (its hermeshd started
// again as it is now), both offer Her by themselves.

const needsUpdate = "这台电脑上的 Hermesh 版本较旧，在那里更新后才能运行 Her";

/** Updates old-box: it says it is from before Her no more, and its hermeshd stops, to be started again. */
function update() {
  fs.writeFileSync(fixture.beforeHer!, "0");
  const daemon = fixture.beforeHerDaemon!.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  execFileSync("pkill", ["-TERM", "-f", `^${daemon}`]);
}

test("Her: a machine from before Her says it needs an update, and offers Her once it has one", async () => {
  test.setTimeout(240_000);
  const { app, page } = await launch(fixture.socket, { profile: fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-")) });
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  const box = page.getByRole("list", { name: "选择 Her 在哪里运行" }).getByRole("listitem").filter({ hasText: "old-box" });
  await expect(box).toContainText(needsUpdate, { timeout: 60_000 });
  await expect(box.getByRole("button", { name: "开启 Her" })).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText("unknown admin command");
  await snap(page, "her-before-her");

  const [win] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("env"))]);
  await win.waitForLoadState();
  await win.getByRole("navigation", { name: "设置" }).getByRole("button", { name: /^old-box/ }).click();
  await win.getByRole("main", { name: "old-box" }).getByRole("listitem", { name: "Her" }).click();
  const machines = win.getByRole("main", { name: "Her" });
  const run = machines.getByRole("switch", { name: "在这个环境运行 Her" });
  await expect(machines).toContainText(`${needsUpdate}。`, { timeout: 30_000 });
  await expect(run).toBeDisabled();
  await snap(win, "her-before-her-settings");

  update();
  await expect(run).toBeEnabled({ timeout: 90_000 });
  await expect(machines).not.toContainText(needsUpdate);
  await expect(box.getByRole("button", { name: "开启 Her" })).toBeVisible({ timeout: 30_000 });
  await expect(box).not.toContainText(needsUpdate);
});
