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
    const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("env/this/providers/antigravity"))]);
    await settings.waitForLoadState();
    const panel = settings.getByRole("main", { name: "Antigravity" });
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

    // A new session offers the same models, the machine's default effort for the one picked: not a list of Hermesh's own.
    await page.getByTestId("nav-new-thread").click();
    await page.getByTestId("thread-composer-input").click();
    await page.getByRole("button", { name: "模型" }).click();
    const picker = page.getByTestId("model-traits-popover");
    await expect(picker.getByTestId("picker-provider-antigravity").locator("[role=menuitemradio]")).toHaveText(["SMALL", "LARGE"]);
    await picker.getByTestId("picker-model-large").click();
    await expect(page.getByTestId("model-picker-trigger")).toHaveText("LARGE");
    // How hard it thinks, after the model's name and at the top of its panel: the picked model's levels, the machine's
    // default chosen.
    await expect(page.getByTestId("thinking-trigger")).toHaveText("超高");
    await page.getByRole("button", { name: "模型" }).click();
    await expect(picker.getByRole("radiogroup", { name: "推理强度" }).getByRole("radio")).toHaveText(["低", "高", "超高"]);
    await expect(page.getByTestId("picker-level-xhigh")).toHaveAttribute("aria-checked", "true");
    await snap(page, "antigravity-new-session-models");
  } finally {
    await app.close();
  }
});
