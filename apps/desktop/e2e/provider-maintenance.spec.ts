import { expect, test } from "@playwright/test";
import { codingAgentTitle } from "@hermesh/core";
import { launch } from "./app";

test("install or upgrade the four managed providers from the remote settings window", async () => {
  const stage = process.env.HERMESH_PROVIDER_UI_STAGE;
  const target = process.env.HERMESH_PROVIDER_UI_TARGET;
  test.skip(!target || (stage !== "install" && stage !== "update"), "requires a fresh or upgraded real target");
  test.setTimeout(12 * 60_000);
  const { app, page } = await launch();
  try {
    const [settings] = await Promise.all([
      app.waitForEvent("window"),
      page.evaluate((id) => window.hermesh?.openSettings(`env/${id}/providers`), target),
    ]);
    const providers = settings.getByRole("main", { name: "Agent 提供方" });
    const versions: Record<string, string[]> = {
      claude: ["2.1.331", "2.1.332"], codex: ["0.171.0", "0.172.0"],
      copilot: ["1.0.116", "1.0.117"], opencode: ["2.4.1", "2.4.2"],
    };
    for (const id of ["claude", "codex", "copilot", "opencode"]) {
      const name = codingAgentTitle(id);
      const row = providers.getByRole("listitem", { name, exact: true });
      if (stage === "install") {
        await row.getByRole("button", { name: "安装…" }).click();
        await settings.getByRole("dialog").getByRole("button", { name: "安装", exact: true }).click();
        await expect(providers.getByRole("list", { name: "Agent 提供方" }).getByRole("listitem", { name, exact: true }))
          .toContainText(versions[id][0], { timeout: 150_000 });
        if (id === "codex") {
          await providers.getByRole("list", { name: "Agent 提供方" }).getByRole("listitem", { name, exact: true }).click();
          const detail = settings.getByRole("region", { name, exact: true });
          const sw = detail.getByRole("switch", { name: "空闲时自动更新" });
          await sw.click();
          await expect(sw).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
          await settings.getByRole("button", { name: "返回Agent 提供方" }).click();
        }
      } else {
        await row.click();
        const detail = settings.getByRole("region", { name, exact: true });
        if (id !== "codex") {
          await detail.getByRole("button", { name: "更新", exact: true }).click();
        }
        await expect(detail.locator(".set-hero")).toContainText(versions[id][1], { timeout: 150_000 });
        await expect(detail).toContainText("已是最新");
        await settings.getByRole("button", { name: "返回Agent 提供方" }).click();
      }
    }
    await providers.getByRole("button", { name: "安装与更新记录" }).click();
    const history = settings.getByRole("main", { name: "安装与更新记录" });
    for (const id of ["claude", "codex", "copilot", "opencode"]) {
      await expect(history).toContainText(codingAgentTitle(id));
    }
  } finally {
    await app.close();
  }
});
