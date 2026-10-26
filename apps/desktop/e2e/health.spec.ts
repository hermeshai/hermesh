import { expect, test } from "@playwright/test";
import { spawn } from "node:child_process";
import { launch, snap } from "./app";

// The Mac running out of processes (the fixture's hermeshd may start only
// 80 more than the user runs): every place that shows it says so in words
// the person acts on, and it recovers by itself.

test.describe.configure({ timeout: 180_000 });

/** A program that forks n children and never reaps them, holding them until killed. */
function zombies(n: number) {
  const p = spawn("/usr/bin/perl", ["-e", `$|=1; for (1..${n}) { exit 0 unless fork } print "ready\\n"; sleep 600`], { stdio: ["ignore", "pipe", "ignore"] });
  const ready = new Promise((r) => p.stdout.once("data", r));
  return { ready, kill: () => p.kill("SIGKILL") };
}

test("out of processes: needs-you, the map, this Mac's page and its agents say why; diagnostics copy; it recovers when the culprit quits", async () => {
  const { app, page } = await launch();
  const culprit = zombies(200);
  await culprit.ready;
  try {
    // 需要你处理 says what is wrong, what it breaks, how it recovers.
    const attention = page.getByRole("region", { name: "需要你处理" });
    const row = attention.getByRole("listitem").filter({ hasText: "这台 Mac 开不了新程序了" });
    await expect(row).toBeVisible({ timeout: 60_000 });
    await expect(row).toContainText(/进程数到了上限（\d+ \/ \d+），其中 \d+ 个是 \S+ 结束后没回收的。Hermesh 启动不了编码 agent/);
    await expect(row.getByRole("button", { name: "打开活动监视器" })).toBeVisible();
    // The map's node for this Mac too.
    const map = page.getByRole("region", { name: "链路" });
    await expect(map.getByRole("button", { name: "这台 Mac", exact: true })).toContainText("开不了新程序");
    await snap(page, "health-overview");

    // Its page in settings: the problem on top, and each coding agent's why.
    const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("machines"))]);
    await settings.waitForLoadState();
    const panel = settings.getByRole("main", { name: "机器" });
    await panel.getByRole("list", { name: "机器" }).getByRole("button", { name: /^owner-mac/ }).click();
    await expect(panel.getByRole("list", { name: "问题" })).toContainText("这台 Mac 开不了新程序了");
    const codex = panel.getByRole("list", { name: "编码 Agent" }).getByRole("listitem", { name: "Codex" });
    await expect(codex).toContainText("运行不了", { timeout: 60_000 });
    await expect(codex).toContainText("找到了，但启动不了：这台机器开不了新程序了（进程数已满）。");
    await snap(settings, "health-settings");
    // What to send us, at hand.
    await panel.getByRole("button", { name: "复制诊断信息" }).click();
    await expect(settings.getByRole("status")).toContainText("诊断信息已拷贝");
    const text = await app.evaluate(({ clipboard }) => clipboard.readText());
    expect(text).toMatch(/processes {2}\d+ \/ \d+ · \d+ ended, not reaped, under /);
    expect(text).toMatch(/codex {6}cannot run \(out of processes\)/);

    // The culprit quits: the problem goes by itself, and Codex is checked again.
    culprit.kill();
    await expect(row).toHaveCount(0, { timeout: 90_000 });
    await expect(codex).not.toContainText("运行不了", { timeout: 60_000 });
    await expect(codex).toContainText("还没登录");
  } finally {
    culprit.kill();
    await app.close();
  }
});
