import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { fixture, gitRepo, launch, pickFrom, snap } from "./app";

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
    await expect(nav.first()).toContainText("新建");
    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("thread-launch-card")).toBeVisible();
    await expect(page.getByTestId("thread-title")).toHaveText("新会话");
    // The question in the middle, with the project in it and the environment under it; the box at the bottom.
    const card = page.getByTestId("thread-launch-card");
    await expect(card.getByRole("heading", { level: 1 })).toContainText(/^要在 .+ 里做什么？$/);
    const box = await page.getByTestId("thread-composer-input").boundingBox();
    const ask = await card.getByRole("heading", { level: 1 }).boundingBox();
    const area = await card.boundingBox();
    expect(box!.y).toBeGreaterThan(ask!.y);
    expect(area!.y + area!.height - (box!.y + box!.height)).toBeLessThan(90);

    // The environment: work-mac. It has no project yet, so the session would use its temporary workspace.
    await pickFrom(page.getByRole("button", { name: "环境" }), page.getByTestId("machine-item-work-mac"));
    await expect(page.getByTestId("launch-machine-chip")).toHaveText("work-mac");
    await expect(page.getByTestId("launch-project-chip")).toHaveText("临时工作区");
    await snap(page, "composer-new");

    // The box is always the text over a row along its bottom (＋ and the model at its left, Send at its right): it
    // does not fold to one line while empty, and it grows as what is written needs more.
    const input = page.getByTestId("thread-composer-input");
    const bar = page.getByTestId("composer-bar");
    const submit = bar.getByTestId("composer-submit-btn");
    const height = async () => (await input.boundingBox())!.height;
    const empty = await height();
    expect(empty).toBeGreaterThan(50);
    await expect(bar).toBeVisible();
    await expect(bar.getByRole("button", { name: "添加附件" })).toBeVisible();
    await expect(bar.getByRole("button", { name: "模型" })).toBeVisible();
    expect((await bar.boundingBox())!.y).toBeGreaterThanOrEqual((await input.boundingBox())!.y + empty);
    // Send is only an arrow, with no plate under it, until there is something to send.
    await expect(submit).toBeDisabled();
    await expect(submit).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
    await input.fill("第一行\n第二行\n第三行");
    expect(await height()).toBeGreaterThan(empty);
    await expect(submit).toBeEnabled();
    await expect(submit).not.toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
    await snap(page, "composer-lines");
    await input.fill("");
    // A click away leaves it as it is: the row stays, the text as tall as it was.
    await card.getByRole("heading", { level: 1 }).click({ position: { x: 2, y: 2 } });
    await expect(bar).toBeVisible();
    expect(await height()).toBe(empty);
    // Typing on the page, away from any field, goes into the box.
    await page.keyboard.type("x");
    await expect(input).toHaveValue("x");
    await input.fill("");

    // Add a Project from the project menu: work-mac's folders, browsed from here.
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-add"));
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
    // The new session takes it, in its own worktree as a git repository's sessions are, from its default branch.
    await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
    await expect(page.getByTestId("launch-worktree")).toHaveText("独立 worktree");
    await expect(page.getByTestId("branch-trigger")).toHaveText("基于 main");
    // The working copy over the box toggles on one click (no menu) and remembers what was chosen for this project.
    await page.getByRole("button", { name: /工作目录/ }).click();
    await expect(page.getByTestId("launch-worktree")).toHaveText("当前目录");
    await expect(page.getByTestId("branch-trigger")).toHaveText("main");
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-item-scratch"));
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId(`project-item-${name}`));
    await expect(page.getByTestId("launch-worktree")).toHaveText("当前目录");
    await page.getByRole("button", { name: /工作目录/ }).click();
    await expect(page.getByTestId("launch-worktree")).toHaveText("独立 worktree");
    await expect(page.getByTestId("branch-trigger")).toHaveText("基于 main");

    // The model: one panel, the providers one under another. Search across them; choosing one closes it.
    await input.click();
    await bar.getByRole("button", { name: "模型" }).click();
    const panel = page.getByTestId("model-traits-popover");
    await expect(panel.getByTestId("picker-provider-scripted")).toContainText("Scripted Agent");
    await panel.getByPlaceholder("搜索模型或服务商").fill("fast");
    await expect(panel.getByTestId("picker-model-scripted-pro")).toHaveCount(0);
    await expect(panel.getByTestId("picker-model-scripted-fast")).toBeVisible();
    await snap(page, "composer-model");
    await panel.getByTestId("picker-model-scripted-fast").click();
    await expect(panel).toBeHidden();
    await expect(page.getByTestId("model-picker-trigger")).toHaveText("Scripted Fast");
    // How hard it thinks: no button of its own, but after the model's name, and at the top of the model's panel: a row
    // of its levels. A click picks one and the panel stays; dragging along the row picks as it goes; with nothing
    // searched, ← and → step through them.
    await expect(bar.getByRole("button", { name: "思考强度" })).toHaveCount(0);
    await bar.getByRole("button", { name: "模型" }).click();
    const effort = panel.getByRole("radiogroup", { name: "推理强度" });
    await expect(effort.getByRole("radio")).toHaveText(["低", "中", "高"]);
    const effortBox = (await effort.boundingBox())!;
    expect(effortBox.y).toBeLessThan((await panel.getByPlaceholder("搜索模型或服务商").boundingBox())!.y);
    await page.getByTestId("picker-level-low").click();
    await expect(page.getByTestId("picker-level-low")).toHaveAttribute("aria-checked", "true");
    await expect(page.getByTestId("thinking-trigger")).toHaveText("低");
    await expect(panel).toBeVisible();
    await page.mouse.move(effortBox.x + 4, effortBox.y + effortBox.height / 2);
    await page.mouse.down();
    await page.mouse.move(effortBox.x + effortBox.width - 4, effortBox.y + effortBox.height / 2, { steps: 8 });
    await page.mouse.up();
    await expect(page.getByTestId("picker-level-high")).toHaveAttribute("aria-checked", "true");
    await panel.getByPlaceholder("搜索模型或服务商").press("ArrowLeft");
    await expect(page.getByTestId("picker-level-medium")).toHaveAttribute("aria-checked", "true");
    await panel.getByPlaceholder("搜索模型或服务商").press("ArrowRight");
    await expect(page.getByTestId("picker-level-high")).toHaveAttribute("aria-checked", "true");
    await snap(page, "composer-effort");
    await page.keyboard.press("Escape");
    await expect(panel).toBeHidden();
    await expect(page.getByTestId("model-picker-trigger")).toHaveText("Scripted Fast");
    await expect(page.getByTestId("thinking-trigger")).toHaveText("高");
    // The permission mode is in ＋, with adding files; not shown in the box.
    await bar.getByRole("button", { name: "添加附件" }).click();
    const plus = page.getByTestId("composer-plus-menu");
    await expect(plus.getByTestId("composer-attach")).toBeVisible();
    await expect(plus.getByRole("menuitemradio")).toHaveCount(4);
    await expect(plus.getByRole("menuitemradio", { checked: true })).toContainText("智能自动");
    await snap(page, "composer-plus");
    await page.keyboard.press("Escape");

    // Start it: the session opens, runs on work-mac in the project, and is in the list.
    await input.fill("say hello_from_work_mac");
    await input.press("Enter");
    // At once, as the session will be: the message under its title, not the composer waiting for the machine.
    await expect(page.getByRole("log").locator(".um").first()).toContainText("say hello_from_work_mac", { timeout: 5_000 });
    await expect(page.getByTestId("thread-launch-card")).toHaveCount(0);
    // Its first turn makes the worktree, on a line of its own under the message; opened, how and where.
    const workspace = page.getByTestId("thread-workspace");
    await expect(workspace).toHaveAttribute("data-state", "done", { timeout: 30_000 });
    await expect(workspace).toContainText(/新建了分支\s*hermesh\//);
    await expect(page.getByRole("log").locator(".am").first()).toContainText("hello_from_work_mac", { timeout: 30_000 });
    expect((await workspace.boundingBox())!.y).toBeGreaterThan((await page.getByRole("log").locator(".um").first().boundingBox())!.y);
    expect((await workspace.boundingBox())!.y).toBeLessThan((await page.getByRole("log").locator(".am").first().boundingBox())!.y);
    await workspace.getByRole("button").click();
    await expect(workspace).toContainText("方式  独立 worktree");
    await snap(page, "composer-workspace");
    await workspace.getByRole("button").first().click();
    // Where it runs, fixed, beside the title; nothing under the box.
    await expect(page.getByTestId("session-where")).toHaveText(`work-mac · ${name}`);
    const row = page.locator(".side-sessions [role=listitem]").filter({ hasText: "say hello_from_work_mac" });
    await expect(row).toBeVisible();
    await expect(row).toHaveAttribute("aria-current", "true");

    // Keep going in it.
    await input.fill("say second_turn_done");
    await input.press("Enter");
    await expect(page.getByRole("log").locator(".am").nth(1)).toContainText("second_turn_done", { timeout: 30_000 });

    // While a turn runs: Stop beside Send, and what is sent waits for the next turn.
    await input.fill("think weighing it\nsleep 4s\ntool-sleep 4s\nsleep 4s\nsay on it\nsleep 20s\nsay never");
    await input.press("Enter");
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 15_000 });
    // And a line at the end of the conversation, as the iPhone's: the mark at work, what the agent does now and how
    // long; under the last message, over the box.
    const working = page.getByTestId("thread-working");
    await expect(working).toContainText("思考中", { timeout: 15_000 }); // its reasoning streams
    await expect(working).toContainText(/\d+:\d\d/);
    await expect(working).toContainText("处理中"); // a tool runs
    await expect(working).toContainText("工作中"); // between steps: no reasoning, tool or reply under way
    await expect(working).toContainText("回复中"); // its reply streams
    const under = (await page.getByRole("log").locator(".um").filter({ hasText: "sleep 20s" }).boundingBox())!;
    const line = (await working.boundingBox())!;
    expect(line.y).toBeGreaterThan(under.y + under.height);
    expect(line.y + line.height).toBeLessThan((await input.boundingBox())!.y);
    await snap(page, "composer-working");
    await input.fill("say after_the_stop");
    await page.getByTestId("composer-queue-btn").click();
    await expect(page.getByTestId("queued-run-0")).toContainText("say after_the_stop");
    await page.getByTestId("composer-stop-btn").click();
    await expect(page.getByRole("log").locator(".am").last()).toContainText("after_the_stop", { timeout: 30_000 });
    await expect(page.getByTestId("composer-stop-btn")).toHaveCount(0, { timeout: 15_000 });
    await expect(working).toHaveCount(0); // nothing runs: the line goes
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
    await input.click();
    await expect(page.getByTestId("model-picker-trigger")).toHaveText("Scripted Fast");
    await expect(page.getByTestId("thinking-trigger")).toHaveText("高");
  } finally {
    await app.close();
  }
});

// A new session's branch (docs/thread-composer.md §16), read from the project's folder on work-mac: a worktree from
// another local branch, and the project's own folder switched to one.
test("a new session's branch: a worktree from another branch, and the project's folder on another branch", async () => {
  const repo = gitRepo(`branches-app-${process.pid}`);
  const name = path.basename(repo);
  const git = (...args: string[]) => execFileSync("git", args, { cwd: repo, env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" } }).toString().trim();
  git("checkout", "-q", "-b", "feat/x");
  git("commit", "-q", "--allow-empty", "-m", "on feat/x");
  git("checkout", "-q", "main");
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickFrom(page.getByRole("button", { name: "环境" }), page.getByTestId("machine-item-work-mac"));
    await addProject(page, repo, name);
    await expect(page.getByTestId("branch-trigger")).toHaveText("基于 main");
    const input = page.getByTestId("thread-composer-input");

    // The branches: the one checked out first, marked as such and as the default; starting from the remote is a choice.
    await page.getByRole("button", { name: "基于分支" }).click();
    const panel = page.getByTestId("branch-panel");
    await expect(panel.getByTestId("branch-item-main")).toContainText("当前 · 默认");
    await expect(panel.getByTestId("branch-item-feat/x")).toBeVisible();
    await expect(panel.getByTestId("branch-remote")).toBeVisible();
    await panel.getByTestId("branch-find").fill("feat");
    await expect(panel.getByTestId("branch-item-main")).toHaveCount(0);
    await snap(page, "composer-branches");
    await panel.getByTestId("branch-item-feat/x").click();
    await expect(page.getByTestId("branch-trigger")).toHaveText("基于 feat/x");

    // Started: its worktree's branch starts at feat/x's commit, and the folder stays on main.
    await input.fill("say from_feat_base");
    await input.press("Enter");
    await expect(page.getByRole("log").locator(".am").first()).toContainText("from_feat_base", { timeout: 30_000 });
    // Said in the conversation, on a line of its own: the worktree's branch, from feat/x.
    await expect(page.getByTestId("thread-workspace")).toContainText(/从 feat\/x 新建了分支\s*hermesh\//);
    const sessionBranch = git("branch", "--list", "hermesh/*", "--format=%(refname:short)");
    expect(sessionBranch).not.toBe("");
    expect(git("log", "-1", "--format=%s", sessionBranch)).toBe("on feat/x");
    expect(git("branch", "--show-current")).toBe("main");

    // In the project's own folder: one click switches it, and the branch is the one to work on; another one switches the folder first.
    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
    await expect(page.getByTestId("launch-worktree")).toHaveText("独立 worktree");
    await page.getByRole("button", { name: /工作目录/ }).click();
    await expect(page.getByTestId("launch-worktree")).toHaveText("当前目录");
    await expect(page.getByTestId("branch-trigger")).toHaveText("main");
    await page.getByRole("button", { name: "在哪个分支上工作" }).click();
    await expect(panel.getByTestId("branch-remote")).toHaveCount(0);
    await panel.getByTestId("branch-item-feat/x").click();
    await expect(page.getByTestId("branch-trigger")).toHaveText("feat/x");
    await page.getByRole("button", { name: "在哪个分支上工作" }).click();
    await expect(panel).toContainText("开始前会把项目目录切到这个分支");
    await page.keyboard.press("Escape");
    await input.fill("say in_place_on_feat");
    await input.press("Enter");
    await expect(page.getByRole("log").locator(".am").first()).toContainText("in_place_on_feat", { timeout: 30_000 });
    // Said in the conversation: the folder switched, and from where.
    const switched = page.getByTestId("thread-workspace");
    await expect(switched).toContainText(/切到了分支\s*feat\/x/);
    await switched.getByRole("button").click();
    await expect(switched).toContainText("feat/x（原来在 main）");
    expect(git("branch", "--show-current")).toBe("feat/x");

    // Next session in the same project remembers "当前目录", while another repository on work-mac still keeps its own setting.
    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
    await expect(page.getByTestId("launch-worktree")).toHaveText("当前目录");
    await expect(page.getByTestId("branch-trigger")).toHaveText("feat/x");
    await pickFrom(page.getByRole("button", { name: "项目" }), page.locator('[data-testid^="project-item-remote-app-"]'));
    await expect(page.getByTestId("launch-worktree")).toHaveText("独立 worktree");
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId(`project-item-${name}`));
    await expect(page.getByTestId("launch-worktree")).toHaveText("当前目录");
  } finally {
    await app.close();
  }
});

/** Add a Project on the chosen machine, its folder typed in. */
async function addProject(page: Page, dir: string, name: string) {
  await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-add"));
  const sheet = page.getByRole("dialog", { name: "添加项目" });
  await sheet.getByLabel("目录").fill(dir);
  await expect(sheet.getByLabel("项目名")).toHaveValue(name);
  await sheet.getByRole("button", { name: "添加", exact: true }).click();
  await expect(sheet).toBeHidden();
  await expect(page.getByTestId("launch-project-chip")).toHaveText(name);
}

test("Add a Project on another machine from its own folders: a recent repository's place, a repository's top for a folder inside it, and a new folder made there", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickFrom(page.getByRole("button", { name: "环境" }), page.getByTestId("machine-item-work-mac"));

    // work-mac's recent repositories, read from it.
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-add"));
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
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-add"));
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
    await expect(page.getByTestId("launch-worktree")).toHaveText("独立 worktree");
    // Made again, the name is taken.
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-add"));
    await sheet.getByRole("button", { name: "浏览…" }).click();
    await browser.getByRole("button", { name: /^code/ }).click();
    await browser.getByRole("button", { name: "新建文件夹…" }).click();
    await make.getByLabel("文件夹名").fill("fresh-app");
    await make.getByRole("button", { name: "新建" }).click();
    await expect(browser).toContainText("fresh-app is already there");
    await page.keyboard.press("Escape");
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
    await pickFrom(page.getByRole("button", { name: "环境" }), page.getByTestId("machine-item-work-mac"));
    await page.getByRole("button", { name: "项目" }).click();
    await expect(page.getByTestId("project-item-mono")).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByTestId("thread-composer-input").click();
    await page.getByRole("button", { name: "模型" }).click();
    await expect(page.getByTestId("picker-provider-scripted")).toBeVisible();
    await page.keyboard.press("Escape");

    // Aimed at work-mac while it answers: no connection notice under the environment.
    const conn = page.getByTestId("connection-line");
    await expect(conn).toHaveCount(0);

    // Start a running turn on work-mac, then stop work-mac: the single WorkLine merges the connection state in place
    // instead of stacking a second bar above the composer.
    await page.getByTestId("thread-composer-input").fill("sleep 30s\nsay done");
    await page.getByTestId("thread-composer-input").press("Enter");
    const working = page.getByTestId("thread-working");
    await expect(working).toContainText("工作中", { timeout: 30_000 });
    await expect(conn).toHaveCount(0);
    process.kill(-fixture.otherMac, "SIGTERM");
    await expect(working.getByTestId("connection-line")).toBeVisible({ timeout: 30_000 });
    await expect(page.locator(".th-work")).toHaveCount(1);
    await expect(page.locator(".cmp-col").getByTestId("connection-line")).toHaveCount(0);
    await expect(conn).toHaveText("work-mac 离线 · 消息会排队到它上线", { timeout: 60_000 });

    // Back on New Session aimed at work-mac: the notice sits under the environment picker, not over the composer.
    await page.getByTestId("nav-new-thread").click();
    await page.getByRole("button", { name: "环境" }).click();
    await expect(page.getByTestId("machine-item-work-mac")).toContainText("离线 · 消息会排队到它上线", { timeout: 60_000 });
    await page.getByTestId("machine-item-work-mac").click();
    await expect(page.locator(".th-ask-after").getByTestId("connection-line")).toHaveText("work-mac 离线 · 消息会排队到它上线", { timeout: 15_000 });
    await expect(page.locator(".cmp-col").getByTestId("connection-line")).toHaveCount(0);

    // Its projects and models are still there to choose, marked as what was last read.
    await page.getByRole("button", { name: "项目" }).click();
    await expect(page.getByRole("menu", { name: "项目" })).toContainText("work-mac 上的项目 · 上次读到的");
    await expect(page.getByTestId("project-item-mono")).toBeVisible();
    await page.getByTestId("project-item-mono").click();
    await page.getByTestId("thread-composer-input").click();
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
    // Said under the message, faded, with Discard; nothing at work, and the inline connection line at the conversation's end.
    const waiting = page.getByRole("log").locator(".um.waiting").filter({ hasText: "say queued_while_offline" });
    await expect(waiting.getByTestId("prompt-note")).toContainText("等 work-mac 在线后送达", { timeout: 15_000 });
    await expect(waiting.getByTestId("prompt-discard")).toBeVisible();
    await expect(page.getByTestId("thread-working")).toHaveCount(0);
    await expect(page.locator(".dbody").getByTestId("connection-line")).toHaveText("work-mac 离线 · 消息会排队到它上线", { timeout: 15_000 });
    await snap(page, "session-offline-capsule");
  } finally {
    await app.close();
  }
});
