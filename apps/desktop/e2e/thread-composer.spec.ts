import { expect, test } from "@playwright/test";
import path from "node:path";
import { fixture, gitRepo, launch, snap } from "./app";

// The new-session composer (docs/thread-composer.md) against two real
// machines: this Mac (owner-mac) and work-mac, a machine joined over the
// relay with the scripted agent. A folder on work-mac becomes a project from
// here, a session starts there on a chosen model, keeps going, and a project's
// group in the list starts the next one.

test("a session on another machine: its folder added as a project, the model picked, started, continued, and started again from the project's group", async () => {
  const repo = gitRepo(`remote-app-${process.pid}`);
  const name = path.basename(repo);
  const { app, page } = await launch();
  try {
    // New Session sits over Her; it opens the composer.
    const nav = page.locator(".side-nav").getByRole("button");
    await expect(nav.first()).toContainText("新建会话");
    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("thread-launch-card")).toBeVisible();
    await expect(page.getByTestId("thread-launch-card").getByRole("heading", { name: "新建会话" })).toBeVisible();

    // The environment: work-mac. It has no project yet, so the session would use its temporary workspace.
    await page.getByRole("button", { name: "环境" }).click();
    await page.getByTestId("machine-item-work-mac").click();
    await expect(page.getByTestId("launch-machine-chip")).toHaveText("work-mac");
    await expect(page.getByTestId("launch-project-chip")).toHaveText("临时工作区");
    await snap(page, "composer-new");

    // The box is one line, growing as what is written needs more, and back.
    const input = page.getByTestId("thread-composer-input");
    const height = async () => (await input.boundingBox())!.height;
    expect(await height()).toBeLessThan(40);
    await input.fill("第一行\n第二行\n第三行");
    expect(await height()).toBeGreaterThan(60);
    await snap(page, "composer-lines");
    await input.fill("");
    expect(await height()).toBeLessThan(40);

    // Add a Project from the project menu: work-mac's folders, browsed from here.
    await page.getByRole("button", { name: "项目" }).click();
    await page.getByTestId("project-add").click();
    const sheet = page.getByRole("dialog", { name: "添加项目" });
    await expect(sheet.getByRole("group", { name: "环境" }).getByRole("button", { name: "work-mac" })).toHaveAttribute("aria-pressed", "true");
    await expect(sheet.getByTestId("add-project-recent")).toBeVisible();
    await sheet.getByRole("button", { name: "浏览…" }).click();
    const browser = sheet.getByTestId("folder-browser");
    await expect(browser.getByRole("button", { name: "主目录" })).toBeVisible();
    await expect(browser.getByRole("button", { name: "进入一个文件夹再选择" })).toBeDisabled();
    await browser.getByRole("button", { name: "输入路径…" }).click();
    await browser.getByLabel("输入路径…").fill(repo);
    await browser.getByLabel("输入路径…").press("Enter");
    await expect(browser).toContainText("这是一个 git 仓库，当前分支 main。");
    await snap(page, "composer-browse");
    await browser.getByRole("button", { name: `选择「${name}」` }).click();
    await expect(sheet.getByLabel("目录")).toHaveValue(repo);
    await expect(sheet.getByLabel("项目名")).toHaveValue(name);
    // The agent connected with every project (the fixture's bot) gets this one too: said before adding.
    await expect(sheet.getByTestId("add-project-reach")).toContainText("bot能用这个环境里的所有项目");
    await sheet.getByRole("button", { name: "添加", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: `已添加项目「${name}」` })).toBeVisible();
    await expect(sheet).toBeHidden();
    // The new session takes it, in its own worktree as a git repository's sessions are.
    await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
    await expect(page.getByTestId("launch-worktree")).toHaveAttribute("aria-checked", "true");

    // The model: one panel. Search across providers, a provider's models, how hard it thinks.
    await page.getByRole("button", { name: "模型" }).click();
    const panel = page.getByTestId("model-traits-popover");
    await panel.getByPlaceholder("搜索模型或服务商").fill("fast");
    await expect(panel.getByTestId("picker-model-scripted-pro")).toHaveCount(0);
    await expect(panel.getByTestId("picker-model-scripted-fast")).toBeVisible();
    await panel.getByPlaceholder("搜索模型或服务商").fill("");
    await panel.getByTestId("picker-provider-scripted").click();
    await panel.getByTestId("picker-model-scripted-fast").click();
    // Choosing a model leaves the panel open, for the level next.
    await panel.getByTestId("picker-level-high").click();
    await expect(page.getByTestId("model-picker-trigger")).toHaveText("Scripted Fast高");
    await snap(page, "composer-model");
    await page.keyboard.press("Escape");
    await expect(panel).toBeHidden();

    // Start it: the session opens, runs on work-mac in the project, and is in the list.
    await input.fill("say hello_from_work_mac");
    await input.press("Enter");
    await expect(page.getByTestId("turn-item-assistant").first()).toContainText("hello_from_work_mac", { timeout: 30_000 });
    await expect(page.getByTestId("composer-options")).toContainText(`work-mac·${name}`);
    const row = page.locator(".side-sessions [role=listitem]").filter({ hasText: "say hello_from_work_mac" });
    await expect(row).toBeVisible();
    await expect(row).toHaveAttribute("aria-current", "true");

    // Keep going in it.
    await input.fill("say second_turn_done");
    await input.press("Enter");
    await expect(page.getByTestId("turn-item-assistant").nth(1)).toContainText("second_turn_done", { timeout: 30_000 });

    // While a turn runs: Stop beside Send, and what is sent waits for the next turn.
    await input.fill("sleep 20s\nsay never");
    await input.press("Enter");
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 15_000 });
    await input.fill("say after_the_stop");
    await input.press("Enter");
    await expect(page.getByTestId("queued-run-0")).toContainText("say after_the_stop");
    await page.getByTestId("composer-stop-btn").click();
    await expect(page.getByTestId("turn-item-assistant").last()).toContainText("after_the_stop", { timeout: 30_000 });
    await expect(page.getByTestId("composer-stop-btn")).toHaveCount(0, { timeout: 15_000 });
    await snap(page, "composer-session");

    // Grouped by project, the project's group starts a new session in it, on work-mac.
    await page.getByRole("button", { name: "视图选项" }).click();
    await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /分组/ }).hover();
    await page.getByRole("menu", { name: "分组" }).getByRole("menuitemradio", { name: "按项目" }).click();
    await page.keyboard.press("Escape");
    const group = page.locator(".side-sessions").getByRole("group", { name });
    await expect(group.locator("[role=listitem]")).toHaveCount(1);
    await group.hover();
    await group.getByRole("button", { name: `在 ${name} 里新建会话` }).click();
    await expect(page.getByTestId("thread-launch-card")).toBeVisible();
    await expect(page.getByTestId("launch-machine-chip")).toHaveText("work-mac");
    await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
    // ⌘N afterwards opens on what the last session used: work-mac, the project, the model.
    await page.locator(".side-nav").getByRole("button", { name: /^概览/ }).click();
    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("launch-machine-chip")).toHaveText("work-mac");
    await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
    await expect(page.getByTestId("model-picker-trigger")).toHaveText("Scripted Fast高");
  } finally {
    await app.close();
  }
});

test("Add a Project on another machine from its own folders: a recent repository's place, a repository's top for a folder inside it, a new folder made there; an older machine says it needs an update", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await page.getByRole("button", { name: "环境" }).click();
    await page.getByTestId("machine-item-work-mac").click();

    // work-mac's recent repositories, read from it.
    await page.getByRole("button", { name: "项目" }).click();
    await page.getByTestId("project-add").click();
    const sheet = page.getByRole("dialog", { name: "添加项目" });
    const recent = sheet.getByTestId("add-project-recent");
    await expect(recent.getByRole("button", { name: /~\/code\/recent-app/ })).toBeVisible();
    await expect(recent.getByRole("button", { name: /~\/code\/mono/ })).toBeVisible();

    // A folder inside a repository: the machine names the repository's top, which the sheet offers instead.
    await sheet.getByLabel("目录").fill("~/code/mono/web");
    await expect(sheet.getByLabel("项目名")).toHaveValue("web");
    await sheet.getByRole("button", { name: "添加", exact: true }).click();
    const inside = sheet.getByTestId("add-project-inside-repo");
    await expect(inside).toContainText("这个目录在仓库");
    await inside.getByRole("button", { name: "改选仓库根" }).click();
    await expect(sheet.getByLabel("目录")).toHaveValue(/[\\/]code[\\/]mono$/);
    await expect(sheet.getByLabel("项目名")).toHaveValue("mono");
    await sheet.getByRole("button", { name: "添加", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "已添加项目「mono」" })).toBeVisible();
    await expect(page.getByTestId("launch-project-chip")).toHaveText("mono");

    // A new folder, made on work-mac under its home folder, as a git repository; then added.
    await page.getByRole("button", { name: "项目" }).click();
    await page.getByTestId("project-add").click();
    await sheet.getByRole("button", { name: "浏览…" }).click();
    const browser = sheet.getByTestId("folder-browser");
    await browser.getByRole("button", { name: /^code/ }).click();
    await expect(browser.getByRole("button", { name: /^recent-app/ })).toBeVisible();
    await browser.getByRole("button", { name: "新建文件夹…" }).click();
    const make = browser.getByTestId("new-folder");
    await make.getByLabel("文件夹名").fill("fresh-app");
    await expect(make.getByRole("checkbox", { name: "初始化为 git 仓库" })).toBeChecked();
    await make.getByRole("button", { name: "新建" }).click();
    await expect(browser).toContainText("这是一个 git 仓库");
    await snap(page, "composer-new-folder");
    await browser.getByRole("button", { name: "选择「fresh-app」" }).click();
    await expect(sheet.getByLabel("项目名")).toHaveValue("fresh-app");
    await sheet.getByRole("button", { name: "添加", exact: true }).click();
    await expect(page.getByTestId("launch-project-chip")).toHaveText("fresh-app");
    await expect(page.getByTestId("launch-worktree")).toHaveAttribute("aria-checked", "true");
    // Made again, the name is taken.
    await page.getByRole("button", { name: "项目" }).click();
    await page.getByTestId("project-add").click();
    await sheet.getByRole("button", { name: "浏览…" }).click();
    await browser.getByRole("button", { name: /^code/ }).click();
    await browser.getByRole("button", { name: "新建文件夹…" }).click();
    await make.getByLabel("文件夹名").fill("fresh-app");
    await make.getByRole("button", { name: "新建" }).click();
    await expect(browser).toContainText("fresh-app is already there");
    await page.keyboard.press("Escape");

    // old-mac runs a Hermesh from before threads: listed, marked, and a session can't be sent there.
    await page.getByRole("button", { name: "环境" }).click();
    await expect(page.getByTestId("machine-item-old-mac")).toContainText("需要更新");
    await page.getByTestId("machine-item-old-mac").click();
    await expect(page.getByTestId("launch-needs-update")).toContainText("old-mac 上的 Hermesh 需要更新才能新建会话");
    await page.getByTestId("thread-composer-input").fill("say hi");
    await expect(page.getByTestId("composer-submit-btn")).toBeDisabled();
  } finally {
    await app.close();
  }
});

test("a new session aimed at a machine gone offline: its projects and models as last read, said so", async () => {
  test.skip(process.platform === "win32", "it stops the other machine by its process group");
  const { app, page } = await launch();
  try {
    // Read once while work-mac answers: its projects and its models.
    await page.getByTestId("nav-new-thread").click();
    await page.getByRole("button", { name: "环境" }).click();
    await page.getByTestId("machine-item-work-mac").click();
    await page.getByRole("button", { name: "项目" }).click();
    await expect(page.getByTestId("project-item-mono")).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "模型" }).click();
    await expect(page.getByTestId("picker-provider-scripted")).toBeVisible();
    await page.keyboard.press("Escape");

    process.kill(-fixture.otherMac, "SIGTERM");
    await page.getByRole("button", { name: "环境" }).click();
    await expect(page.getByTestId("machine-item-work-mac")).toContainText("离线 · 消息会排队到它上线", { timeout: 60_000 });
    await page.getByTestId("machine-item-work-mac").click();

    // Its projects and models are still there to choose, marked as what was last read.
    await page.getByRole("button", { name: "项目" }).click();
    await expect(page.getByRole("menu", { name: "项目" })).toContainText("work-mac 上的项目 · 上次读到的");
    await expect(page.getByTestId("project-item-mono")).toBeVisible();
    await page.getByTestId("project-item-mono").click();
    await page.getByRole("button", { name: "模型" }).click();
    const panel = page.getByTestId("model-traits-popover");
    await expect(panel).toContainText("work-mac 离线，这是上次读到的模型");
    await expect(panel.getByTestId("picker-provider-scripted")).toBeVisible();
    await snap(page, "composer-offline");
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("composer-submit-btn")).toHaveAttribute("title", "离线入队");
    // Sent anyway: the session opens, waiting for work-mac.
    await page.getByTestId("thread-composer-input").fill("say queued_while_offline");
    await page.getByTestId("thread-composer-input").press("Enter");
    await expect(page.getByTestId("offline-badge")).toHaveText("离线排队中", { timeout: 15_000 });
  } finally {
    await app.close();
  }
});
