import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { fixture, launch, onWindows, sessionList, snap } from "./app";

// A coding session's files (docs/session-files.md): the files a reply links are tags; a page opens
// over the window and runs, offline, reading its own folder from the session's machine (another
// Mac here); code opens at the line the reply named; a reply's file is as its turn left it, and
// the current one is a click away; what was read once opens with the machine gone.

async function open(page: Page, title: string) {
  await (await sessionList(page)).getByText(title, { exact: true }).click();
  await expect(page.getByRole("heading", { name: title })).toBeVisible();
}

/** The page shown over the window: its own web contents, apart from the app's. */
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

test("a reply's files are tags; a page runs over the window offline; code opens in the file panel at its line, with the tree; a turn's file and the current one", async () => {
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

  // The page, as turn 1 left it: it runs (its script beside it read too), and cannot reach the network.
  await demo.click();
  const view = page.getByTestId("file-view");
  await expect(view).toBeVisible();
  await expect(view.locator(".fpv-name")).toHaveText("demo.html");
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 20_000 }).toBe("第一版");
  await expect.poll(() => inPage<string>(app, "document.title")).toBe("demo.js 读到了");
  await inPage(app, "document.getElementById('b').click()");
  await expect.poll(() => inPage<string>(app, "document.getElementById('t').textContent")).toBe("点过了");
  expect(await inPage<string>(app, "fetch('https://example.com/').then(() => 'reached', () => 'blocked')")).toBe("blocked");
  expect(await inPage<string>(app, "fetch('demo.js').then((r) => r.ok ? 'read' : 'refused', () => 'blocked')")).toBe("read");
  expect(await inPage<string>(app, "fetch('../README.md').then((r) => r.ok ? 'read' : 'refused', () => 'blocked')")).not.toBe("read");
  // It is a turn's file, changed since: the bar says so, and shows the current one.
  const bar = view.getByTestId("file-version");
  await expect(bar).toContainText("这是第 1 轮结束时的版本，之后改过");
  await snap(page, "files-page-turn");
  await pageShot(app, "files-page-turn-content");
  // A page has no source button here (its source is in the panel); it can take the whole window, the page with it.
  await expect(view.getByRole("button", { name: "源码" })).toHaveCount(0);
  const pageWidth = () =>
    app.evaluate(({ BrowserWindow, WebContentsView }) => {
      for (const w of BrowserWindow.getAllWindows())
        for (const v of w.contentView.children) if (v instanceof WebContentsView && v.webContents.getURL().startsWith("hermesh-file:")) return v.getBounds().width;
      return 0;
    });
  const before = await pageWidth();
  await view.getByTestId("file-view-max").click();
  const windowWidth = await page.evaluate(() => window.innerWidth);
  await expect.poll(pageWidth).toBeGreaterThan(Math.max(before, windowWidth - 40));
  await snap(page, "files-page-full-window");
  await pageShot(app, "files-page-full-window-content");
  await view.getByTestId("file-view-max").click();
  await expect.poll(pageWidth).toBeLessThan(windowWidth - 40);
  await bar.getByRole("button", { name: "查看当前版本" }).click();
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 20_000 }).toBe("第二版");
  await expect(bar).toContainText("这是当前版本");
  await page.keyboard.press("Escape");
  await expect(view).toBeHidden();
  await expect.poll(() => app.evaluate(({ webContents }) => webContents.getAllWebContents().some((w) => w.getURL().startsWith("hermesh-file:")))).toBe(false);

  // Code, and anything a reply names a line of, opens in the file panel at that line, the tree opened to it.
  await lines.click();
  const panel = page.getByTestId("file-panel");
  await expect(panel).toBeVisible();
  await expect(panel.locator(".sfp-tab.on")).toHaveText("lines.txt");
  const code = panel.getByTestId("file-code");
  await expect(code.locator(".cm-target")).toHaveText("line 30");
  await expect(code.locator(".cm-target")).toBeInViewport();
  const tree = panel.getByTestId("file-tree");
  await expect(tree.locator('.sft-row.on[data-path="src/lines.txt"]')).toBeVisible();
  // The info card steps aside while the panel is open.
  await expect(page.getByTestId("toolbar-files-btn")).toBeHidden();
  await snap(page, "files-panel-code");

  // The tree: a folder opens, a file in it opens as a tab of its own; a page shows its source, and runs over the window.
  await tree.locator('.sft-row[data-path="docs"]').click();
  await tree.locator('.sft-row[data-path="docs/demo.js"]').click();
  await expect(panel.locator(".sfp-tab.on")).toHaveText("demo.js");
  await expect(panel.getByTestId("file-code")).toContainText("document.title");
  // A page runs in the panel too, as it is now.
  await tree.locator('.sft-row[data-path="docs/demo.html"]').click();
  await expect(panel.getByTestId("panel-page")).toBeVisible();
  await expect(panel.locator(".sfp-tab")).toHaveCount(3);
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 20_000 }).toBe("第二版");
  await snap(page, "files-panel-tree");
  await pageShot(app, "files-panel-page-content");

  // Taking the window, and back; put away, its tabs stay for next time.
  await panel.getByRole("button", { name: "放大" }).click();
  await expect(page.locator(".dmain")).toBeHidden();
  await snap(page, "files-panel-max");
  await panel.getByRole("button", { name: "还原" }).click();
  await expect(page.locator(".dmain")).toBeVisible();
  await page.getByTestId("file-panel-close").click();
  await expect(panel).toBeHidden();
  await page.getByTestId("toolbar-files-btn").click();
  await expect(panel.locator(".sfp-tab")).toHaveCount(3);
  // A tab closed; the reply's tag opens it again.
  for (const name of ["lines.txt", "demo.js", "demo.html"]) await panel.locator(".sfp-tab", { hasText: name }).getByRole("button").click();
  await expect(panel.locator(".sfp-tab")).toHaveCount(0);

  // Inline session images & blurred Lightbox (docs/session-images.md): external simulator screenshot
  // renders inline once while its file:// link stays a clickable tag (no duplicate image); linked
  // before/after PNGs render side-by-side in an image strip; Lightbox shows centered header + ⋯/✕ only.
  const shotReply = log.locator(".am").filter({ hasText: "截图验证：" });
  await expect(shotReply.getByTestId("session-image")).toHaveCount(3);
  const shotCard = shotReply.locator('[data-testid=session-image][data-path$="ios_simulator_screenshot.png"]');
  const shotTag = shotReply.locator('[data-testid=file-tag][data-path$="ios_simulator_screenshot.png"]');
  await expect(shotCard).toBeVisible();
  await expect(shotCard.locator("img")).toBeVisible();
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

  // files-mac gone: what was read before still opens, in the panel, over the window, and in the Lightbox.
  process.kill(-fixture.otherMac, "SIGTERM");
  await lines.click();
  await expect(panel.getByTestId("file-code").locator(".cm-target")).toHaveText("line 30", { timeout: 30_000 });
  await demo.click();
  await expect.poll(() => inPage<string>(app, "document.getElementById('t')?.textContent ?? ''"), { timeout: 30_000 }).toBe("第一版");
  await page.keyboard.press("Escape");
  await shotCard.click();
  await expect(lb.getByTestId("lightbox-image")).toBeVisible();
  await page.keyboard.press("Escape");
  await app.close();
});
