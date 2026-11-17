import { expect, test } from "@playwright/test";
import { fixture, launch, snap } from "./app";

test("Antigravity shows scoped model observations, unknown defaults and expiry", async () => {
  test.skip(process.env.HERMESH_MODEL_CATALOG_TEST !== "1", "TestAntigravityModelCatalogDesktop supplies fake ACP observations");
  const { app, page } = await launch();
  try {
    const [settings] = await Promise.all([
      app.waitForEvent("window"),
      page.evaluate(() => window.hermesh?.openSettings("machines")),
    ]);
    await settings.waitForLoadState();
    const panel = settings.getByRole("main", { name: "机器" });
    await panel.getByRole("list", { name: "机器" }).getByRole("button", { name: /^owner-mac/ }).click();
    const agents = panel.getByRole("list", { name: "编码 Agent" });
    await agents.getByRole("listitem", { name: "Antigravity" }).getByRole("button", { name: /^Antigravity/ }).click();
    const observations = panel.getByRole("region", { name: "已观察到的模型" });
    await expect(observations).toContainText("未知，等待一次正常任务返回模型选项");
    await expect(observations).toContainText("来源：acp_initialize");
    await expect(observations).toContainText("来源：acp_update");
    await expect(observations.getByText("small", { exact: true })).toBeVisible();
    await expect(observations.getByText("large", { exact: true })).toBeVisible();
    await expect(observations).toContainText("初始模型：small");
    await expect(observations).toContainText("当前模型：large");
    await expect(observations).toContainText(`demo/${fixture.sessions.observed}`);
    // Scoped observations must not become the machine-wide defaults editor.
    await expect(panel.getByRole("group", { name: "模型", exact: true })).toHaveCount(0);
    await snap(settings, "antigravity-model-observations");
    await settings.clock.install({ time: new Date() });
    await settings.clock.fastForward(6 * 60 * 1000);
    await expect(observations.getByText("观察结果已过期 · 可用性需重新验证")).toHaveCount(2);
    await snap(settings, "antigravity-model-observations-expired");
  } finally {
    await app.close();
  }
});
