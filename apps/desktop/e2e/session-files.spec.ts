import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { answerMenu, fixture, launch, onWindows, sessionList, snap } from "./app";

// A coding session's files (docs/session-files.md): the files a reply links are tags, each opening a
// tab of the side panel; a page runs there, offline, reading its own folder from the session's machine
// (another Mac here); code opens at the line the reply named, the tree beside it when asked for; a
// reply's file is as its turn left it, and the current one is a click away; what was read once opens
// with the machine gone.

async function open(page: Page, title: string) {
  await (await sessionList(page)).getByText(title, { exact: true }).click();
  await expect(page.getByRole("heading", { name: title })).toBeVisible();
}

/** The page shown in the side panel: its own web contents, apart from the app's. */
async function inPage<T>(app: ElectronApplication, script: string): Promise<T | null> {
  return app.evaluate(async ({ webContents }, js) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().startsWith("hermesh-file:") && !w.isLoading());
    return wc ? wc.executeJavaScript(js).catch(() => null) : null;
  }, script) as Promise<T | null>;
}

async function pageShot(app: ElectronApplication, name: string) {
  const dir = process.env.HERMESH_SCREENS;
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  const png = await app.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().startsWith("hermesh-file:"));
    return wc ? (await wc.capturePage()).toPNG().toString("base64") : "";
  });
  if (png) fs.writeFileSync(path.join(dir, `${name}.png`), Buffer.from(png, "base64"));
}

/** The page's own view over the window: its width, and whether it shows. */
function pageView(app: ElectronApplication) {
  return app.evaluate(({ BrowserWindow, WebContentsView }) => {
    for (const w of BrowserWindow.getAllWindows())
      for (const v of w.contentView.children) if (v instanceof WebContentsView && v.webContents.getURL().startsWith("hermesh-file:")) return { width: v.getBounds().width, visible: v.getVisible() };
    return null;
  });
}

test("a reply's files are tags opening side panel tabs; a page runs there offline; code at its line, the tree in its own tab or beside it; a turn's file and the current one", async () => {
  test.skip(onWindows, "it stops the other machine by its process group");
  const { app, page } = await launch();
  await open(page, "文件预览");
  const log = page.getByRole("log", { name: "对话" });
  const reply = log.locator(".am").filter({ hasText: "原型：" });
  const demo = reply.locator('[data-testid=file-tag][data-path="docs/demo.html"]');
  await expect(demo).toBeVisible();
  await expect(demo).toHaveText("demo.html");
  // Inline code that is a file is a tag too, with its line; a link to nothing is grey, one outside the folder too.
  const lines = reply.locator('[data-testid=file-tag][data-path="src/lines.txt"]');
  await expect(lines).toHaveText("src/lines.txt:30");
  await expect(reply.locator('[data-testid=file-tag][data-path="gone.md"]')).toHaveClass(/\bgone\b/);
  await expect(reply.locator('[data-testid=file-tag][data-path="/etc/hosts"]')).toHaveClass(/\bgone\b/);
  await expect(reply.locator('[data-testid=file-tag][data-path="/etc/hosts"]')).toHaveAttribute("title", /会话的文件夹之外/);
  await snap(page, "files-tags");

  // The page, as turn 1 left it, in a tab of its own: it runs (its script beside it read too), and cannot reach the network.
  await demo.click();
  const panel = page.getByTestId("side-panel");
  await expect(panel).toBeVisible();
  const tabs = panel.getByRole("tab");
  await expect(tabs).toHaveCount(1);
  await expect(panel.locator(".sfp-tab.on")).toHaveText("demo.html");
  await expect(panel.locator(".sfp-tab.on")).toHaveAttribute("data-kind", "page");
  await expect(panel.locator(".sfp-addr")).toHaveText("docs/demo.html");
  await expect(panel.getByTestId("panel-page")).toBeVisible();
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 20_000 }).toBe("第一版");
  await expect.poll(() => inPage<string>(app, "document.title")).toBe("demo.js 读到了");
  await inPage(app, "document.getElementById('b').click()");
  await expect.poll(() => inPage<string>(app, "document.getElementById('t').textContent")).toBe("点过了");
  expect(await inPage<string>(app, "fetch('https://example.com/').then(() => 'reached', () => 'blocked')")).toBe("blocked");
  expect(await inPage<string>(app, "fetch('demo.js').then((r) => r.ok ? 'read' : 'refused', () => 'blocked')")).toBe("read");
  expect(await inPage<string>(app, "fetch('../README.md').then((r) => r.ok ? 'read' : 'refused', () => 'blocked')")).not.toBe("read");
  // The toolbar: the panel's button pressed; the info card cannot be shown while the panel is open.
  await expect(page.getByTestId("toolbar-panel-btn")).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("toolbar-info-btn")).toBeDisabled();
  // It is a turn's file, changed since: the bar says so, and shows the current one.
  const bar = panel.getByTestId("file-version");
  await expect(bar).toContainText("这是第 1 轮结束时的版本，之后改过");
  await snap(page, "files-page-turn");
  await pageShot(app, "files-page-turn-content");
  // A page tab has no source button and no tree; it can take the window, the page with it.
  await expect(panel.getByRole("button", { name: "源码" })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "显示或隐藏目录树" })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "在浏览器中打开" })).toBeVisible();
  const before = (await pageView(app))?.width ?? 0;
  const windowWidth = await page.evaluate(() => window.innerWidth);
  await panel.getByRole("button", { name: "放大" }).click();
  await expect(page.locator(".dmain")).toBeHidden();
  await expect.poll(async () => (await pageView(app))?.width ?? 0).toBeGreaterThan(Math.max(before, windowWidth - 400));
  await snap(page, "files-panel-max");
  await panel.getByRole("button", { name: "还原" }).click();
  await expect(page.locator(".dmain")).toBeVisible();
  await expect.poll(async () => (await pageView(app))?.width ?? 0).toBeLessThan(windowWidth - 400);
  await bar.getByRole("button", { name: "查看当前版本" }).click();
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 20_000 }).toBe("第二版");
  await expect(bar).toContainText("这是当前版本");

  // Code, and anything a reply names a line of, opens in a tab of its own at that line; the tree is hidden at first.
  await lines.click();
  await expect(tabs).toHaveCount(2);
  await expect(panel.locator(".sfp-tab.on")).toHaveText("lines.txt");
  await expect(panel.locator(".sfp-tab.on")).toHaveAttribute("data-kind", "files");
  await expect(panel.locator(".sfp-path")).toHaveText("src/lines.txt");
  const code = panel.getByTestId("file-code");
  await expect(code.locator(".cm-target")).toHaveText("line 30");
  await expect(code.locator(".cm-target")).toBeInViewport();
  await expect.poll(() => app.evaluate(({ webContents }) => webContents.getAllWebContents().some((w) => w.getURL().startsWith("hermesh-file:")))).toBe(false);
  const tree = panel.getByTestId("file-tree");
  await expect(tree).toHaveCount(0);
  // The tree beside it, opened to the file.
  await panel.getByRole("button", { name: "显示或隐藏目录树" }).click();
  await expect(tree.locator('.sft-row.on[data-path="src/lines.txt"]')).toBeVisible();
  await snap(page, "files-panel-code");

  // A file picked in the tree opens in this tab, the tree kept; a page picked there goes to its page tab.
  // (docs is open already: the tree opens to each tab's file, the page's too.)
  await expect(tree.locator('.sft-row[data-path="docs"]')).toHaveAttribute("aria-expanded", "true");
  await tree.locator('.sft-row[data-path="docs/demo.js"]').click();
  await expect(tabs).toHaveCount(2);
  await expect(panel.locator(".sfp-tab.on")).toHaveText("demo.js");
  await expect(panel.getByTestId("file-code")).toContainText("document.title");
  await expect(tree.locator('.sft-row.on[data-path="docs/demo.js"]')).toBeVisible();
  await tree.locator('.sft-row[data-path="docs/demo.html"]').click();
  await expect(tabs).toHaveCount(2);
  await expect(panel.locator(".sfp-tab.on")).toHaveAttribute("data-kind", "page");
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 20_000 }).toBe("第二版");

  // ＋: a new tab of a kind; today 项目文件, the tree alone, files with their sizes.
  await answerMenu(app, "files");
  await panel.getByTestId("side-panel-new").click();
  await expect(tabs).toHaveCount(3);
  await expect(panel.locator(".sfp-tab.on")).toHaveText("项目文件");
  await expect(tree.locator('.sft-row[data-path="README.md"] .sft-size')).toHaveText("7 B");
  await snap(page, "files-panel-tree");
  // A file picked there opens in that tab.
  await tree.locator('.sft-row[data-path="README.md"]').click();
  await expect(tabs).toHaveCount(3);
  await expect(panel.locator(".sfp-tab.on")).toHaveText("README.md");

  // Put away, its tabs stay for next time; every tab closed, the panel goes, and comes back with 项目文件.
  await page.getByTestId("toolbar-panel-btn").click();
  await expect(panel).toBeHidden();
  await expect(page.getByTestId("toolbar-info-btn")).toBeEnabled();
  await page.getByTestId("toolbar-panel-btn").click();
  await expect(tabs).toHaveCount(3);
  for (let i = 0; i < 3; i++) await tabs.first().getByRole("button").click();
  await expect(panel).toBeHidden();
  await page.getByTestId("toolbar-panel-btn").click();
  await expect(tabs).toHaveCount(1);
  await expect(panel.locator(".sfp-tab.on")).toHaveText("项目文件");
  await page.getByTestId("toolbar-panel-btn").click();

  // Inline session images & blurred Lightbox (docs/session-images.md): external simulator screenshot
  // renders inline once while its file:// link stays a clickable tag (no duplicate image); linked
  // before/after PNGs render side-by-side in an image strip; Lightbox shows centered header + ⋯/✕ only.
  const shotReply = log.locator(".am").filter({ hasText: "截图验证：" });
  await expect(shotReply.getByTestId("session-image")).toHaveCount(3);
  const shotCard = shotReply.locator('[data-testid=session-image][data-path$="ios_simulator_screenshot.png"]');
  const shotTag = shotReply.locator('[data-testid=file-tag][data-path$="ios_simulator_screenshot.png"]');
  await expect(shotCard).toBeVisible();
  await expect(shotCard.locator("img")).toBeVisible();
  // A picture and a file's tag in a reply show the hand: they open.
  await expect(shotCard).toHaveCSS("cursor", "pointer");
  await expect(shotTag).toHaveCSS("cursor", "pointer");
  await expect(shotTag).toBeVisible();
  await expect(shotTag).not.toHaveClass(/\bgone\b/);
  await expect(shotReply.getByTestId("session-image-row").getByTestId("session-image")).toHaveCount(2);
  await snap(page, "files-inline-images");

  await shotCard.click();
  const lb = page.getByTestId("image-lightbox");
  await expect(lb).toBeVisible();
  await expect(lb.getByTestId("lightbox-title")).toHaveText("ios_simulator_screenshot.png");
  await expect(lb.getByTestId("lightbox-meta")).toContainText("1 / 3 · 120 × 260");
  await expect(lb.locator(".slb-actions button")).toHaveCount(2);
  await expect(lb.getByTestId("lightbox-image")).toBeVisible();
  await snap(page, "files-image-lightbox");
  await page.keyboard.press("ArrowRight");
  await expect(lb.getByTestId("lightbox-title")).toHaveText("before.png");
  await expect(lb.getByTestId("lightbox-meta")).toContainText("2 / 3 · 160 × 120");
  await page.keyboard.press("Escape");
  await expect(lb).toBeHidden();

  // Clicking the deduplicated file:// tag opens the same Lightbox.
  await shotTag.click();
  await expect(lb).toBeVisible();
  await expect(lb.getByTestId("lightbox-title")).toHaveText("ios_simulator_screenshot.png");
  await page.keyboard.press("Escape");
  await expect(lb).toBeHidden();

  // A page tab in front, the Lightbox over the panel: the page's view, drawn over the app, steps aside meanwhile.
  await demo.click();
  await expect.poll(async () => (await pageView(app))?.visible).toBe(true);
  await shotCard.click();
  await expect(lb).toBeVisible();
  await expect.poll(async () => (await pageView(app))?.visible).toBe(false);
  await page.keyboard.press("Escape");
  await expect.poll(async () => (await pageView(app))?.visible).toBe(true);

  // Antigravity ````carousel```` + [!NOTE] callout + outside brain/walkthrough.md artifact preview with relative images.
  await open(page, "走查预览");
  const walkReply = log.locator(".am").filter({ hasText: "走查结果：" });
  await expect(walkReply.locator('.md-callout[data-kind="note"]')).toContainText("兼容 Antigravity 轮播与提示框");
  await expect(walkReply).not.toContainText("[!NOTE]");
  await expect(walkReply.locator("pre")).toHaveCount(0);
  await expect(walkReply.getByTestId("session-image-row").getByTestId("session-image")).toHaveCount(2);

  // 3+ consecutive standalone images (separated by blank lines) merge into a Focus Stage + Filmstrip carousel.
  const stageReply = log.locator(".am").filter({ hasText: "多图舞台：" });
  const stageBox = stageReply.getByTestId("session-image-row");
  await expect(stageBox).toHaveAttribute("data-simg-mode", "stage");
  await expect(stageBox.getByTestId("session-image-thumb")).toHaveCount(4);
  await expect(stageBox.getByTestId("session-image-counter")).toContainText("1 / 4 · 240 × 135");
  await expect(stageBox.getByTestId("session-image-caption")).toHaveText("方案一 [默认收起]：连体抽屉托盘");
  await stageBox.getByTestId("session-image-thumb").nth(2).click();
  await expect(stageBox.getByTestId("session-image-counter")).toContainText("3 / 4 · 240 × 135");
  await expect(stageBox.getByTestId("session-image-caption")).toHaveText("方案三：浅色模式对比");
  await stageBox.getByTestId("session-image").click();
  await expect(lb).toBeVisible();
  await expect(lb.getByTestId("lightbox-title")).toHaveText("stage_3.png");
  await expect(lb.getByTestId("lightbox-meta")).toContainText("3 / 4 · 240 × 135");
  await page.keyboard.press("Escape");
  await expect(lb).toBeHidden();

  const walkTag = walkReply.locator('[data-testid=file-tag][data-path$="walkthrough.md"]');
  await expect(walkTag).toBeVisible();
  await expect(walkTag).not.toHaveClass(/\bgone\b/);
  await walkTag.click();
  const mdPreview = panel.getByTestId("file-markdown");
  await expect(mdPreview).toBeVisible();
  await expect(mdPreview.locator('.md-callout[data-kind="note"]')).toContainText("已合并同源连线并渲染轮播对比。");
  await expect(mdPreview.locator("pre")).toHaveCount(0);
  const mdImgs = mdPreview.getByTestId("session-image-row").getByTestId("session-image");
  await expect(mdImgs).toHaveCount(2);
  await expect(mdImgs.first().locator("img")).toBeVisible();
  await mdImgs.first().click();
  await expect(lb).toBeVisible();
  await expect(lb.getByTestId("lightbox-title")).toHaveText("carousel_dark.png");
  await expect(lb.getByTestId("lightbox-meta")).toContainText("1 / 2 · 150 × 220");
  await page.keyboard.press("Escape");
  await expect(lb).toBeHidden();
  await open(page, "文件预览");

  // files-mac gone: what was read before still opens, in the panel and in the Lightbox.
  process.kill(-fixture.otherMac, "SIGTERM");
  await lines.click();
  await expect(panel.getByTestId("file-code").locator(".cm-target")).toHaveText("line 30", { timeout: 30_000 });
  await demo.click();
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 30_000 }).toBe("第一版");
  await shotCard.click();
  await expect(lb.getByTestId("lightbox-image")).toBeVisible();
  await page.keyboard.press("Escape");
  await app.close();
});
