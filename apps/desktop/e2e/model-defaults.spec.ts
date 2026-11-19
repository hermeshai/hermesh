import type { ElectronApplication } from "@playwright/test";
import { expect, test } from "@playwright/test";
import { launch, snap } from "./app";

/** The native menu can't be clicked: the next one shown answers the item labelled label, and is kept to be read. */
async function answerMenuLabel(app: ElectronApplication, label: string) {
  await app.evaluate(({ ipcMain }, label) => {
    ipcMain.removeHandler("popup-menu");
    ipcMain.handle("popup-menu", (_e, items: { id: string; label: string }[]) => {
      (globalThis as { shownMenu?: string[] }).shownMenu = items.map((x) => x.label);
      return items.find((x) => x.label === label)?.id ?? null;
    });
  }, label);
}
const shownMenu = (app: ElectronApplication) => app.evaluate(() => (globalThis as { shownMenu?: string[] }).shownMenu);

test("Antigravity's default model and reasoning effort are picked from the models it offers", async () => {
  test.skip(process.env.HERMESH_MODEL_DEFAULTS_TEST !== "1", "TestAntigravityModelDefaultsDesktop runs fakeacp as Antigravity");
  const { app, page } = await launch();
  try {
    const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("machines"))]);
    await settings.waitForLoadState();
    const panel = settings.getByRole("main", { name: "机器" });
    await panel.getByRole("list", { name: "机器" }).getByRole("button", { name: /^owner-mac/ }).click();
    await panel.getByRole("list", { name: "编码 Agent" }).getByRole("listitem", { name: "Antigravity" }).getByRole("button", { name: /^Antigravity/ }).click();
    const defaults = panel.getByRole("region", { name: "默认模型" });
    const model = defaults.getByRole("group", { name: "模型", exact: true });
    const effort = defaults.getByRole("group", { name: "推理强度" });
    await expect(model.getByRole("button")).toHaveText("跟随 Antigravity（SMALL）");
    await expect(effort.getByRole("button")).toHaveText("跟随 Antigravity（高）");
    await expect(defaults.getByRole("group", { name: "速度" })).toHaveCount(0);

    await answerMenuLabel(app, "LARGE");
    await model.getByRole("button").click();
    await expect(model.getByRole("button")).toHaveText("LARGE");
    expect(await shownMenu(app)).toEqual(["跟随 Antigravity（SMALL）", "SMALL", "LARGE"]);

    // The efforts are the picked model's: large has xhigh, small does not.
    await answerMenuLabel(app, "超高");
    await effort.getByRole("button").click();
    await expect(effort.getByRole("button")).toHaveText("超高");
    expect(await shownMenu(app)).toEqual(["跟随 Antigravity（高）", "低", "高", "超高"]);
    await snap(settings, "antigravity-default-model");
  } finally {
    await app.close();
  }
});
