import { expect, test } from "@playwright/test";
import { codingAgentIds, codingAgentTitle } from "@hermesh/core";
import { launch } from "./app";

// Real, separately paired target prepared by TestProviderMaintenanceReal.
test("every real provider completes a new verification from the remote environment's settings", async () => {
  const target = process.env.HERMESH_PROVIDER_UI_TARGET;
  test.skip(!target, "requires a real provider acceptance target");
  test.setTimeout(12 * 60_000);
  const { app, page } = await launch();
  try {
    const [settings] = await Promise.all([
      app.waitForEvent("window"),
      page.evaluate((id) => window.hermesh?.openSettings(`env/${id}/providers`), target),
    ]);
    const providers = settings.getByRole("main", { name: "Agent 提供方" });
    for (const agent of codingAgentIds) {
      const name = codingAgentTitle(agent);
      await providers.getByRole("listitem", { name, exact: true }).click();
      const detail = settings.getByRole("region", { name, exact: true });
      const readReceipt = () => settings.evaluate(async ({ target, agent }) => {
        const response = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListMachines", {
          method: "POST", headers: { "content-type": "application/json" }, body: "{}",
        });
        type Receipt = { request: { provider: string; requestId: string }; phase: string };
        const body: { machines: { machineId: string; status?: { providerOperations?: Receipt[] } }[] } = await response.json();
        return body.machines.find((m) => m.machineId === target)?.status?.providerOperations?.findLast((op) => op.request.provider === agent);
      }, { target, agent });
      const before = (await readReceipt())?.request.requestId;
      await detail.getByRole("button", { name: "验证命令行工具" }).click();
      await expect.poll(async () => {
        const receipt = await readReceipt();
        return receipt?.request.requestId !== before ? receipt?.phase : "";
      }, { message: name, timeout: 90_000 }).toBe("succeeded");
      await expect(detail.getByRole("status").filter({ hasText: "已完成" })).toBeVisible();
      await settings.getByRole("button", { name: "返回Agent 提供方" }).click();
    }
    await providers.getByRole("button", { name: "提供方任务记录" }).click();
    const history = settings.getByRole("main", { name: "提供方任务记录" });
    for (const id of codingAgentIds) await expect(history).toContainText(codingAgentTitle(id) + " · provider-target");
  } finally {
    await app.close();
  }
});

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
    for (const id of ["claude", "codex", "copilot", "opencode"]) {
      const name = codingAgentTitle(id);
      const row = providers.getByRole("listitem", { name, exact: true });
      if (stage === "install") await row.getByRole("button", { name: "安装详情…" }).click();
      else await row.click();
      const detail = settings.getByRole("region", { name, exact: true });
      if (stage === "install" || id !== "codex") {
        await detail.getByRole("button", { name: stage === "install" ? "安装" : "更新", exact: true }).click();
      }
      // The installed state comes from the target's own driver after activation.
      const versions: Record<string, string[]> = {
        claude: ["2.1.331", "2.1.332"], codex: ["0.171.0", "0.172.0"],
        copilot: ["1.0.116", "1.0.117"], opencode: ["2.4.1", "2.4.2"],
      };
      await expect(detail.locator(".set-hero")).toContainText(versions[id][stage === "install" ? 0 : 1], { timeout: 150_000 });
      await expect(detail.getByRole("status").filter({ hasText: "已完成" })).toBeVisible();
      if (id === "codex" && stage === "install") {
        await detail.getByRole("button", { name: "开启自动更新" }).click();
        await expect(detail.getByRole("button", { name: "关闭自动更新" })).toBeVisible();
      }
      await settings.getByRole("button", { name: "返回Agent 提供方" }).click();
    }
  } finally {
    await app.close();
  }
});
