import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { fixture, launch, snap } from "./app";

// The files Her sent, in a list of their own (docs/her-deliverables.md), as the owner uses it, against her itself
// with her-mock as its model. Run by TestDesktopHerDeliveries, which needs her built. One story: Her is turned on
// here and sends a report; it is changed on the machine, as her model would change it, and sent again. The button
// counts the new files; the panel lists the report once with its two versions, each opening as it was sent; Show in
// Conversation goes to its message; Reply quotes it, and Her is told which file; search and the kinds filter; and
// closing the panel is having seen the list.

test("Her: the files she sent, listed apart, each version as sent, and the way back to its message", async () => {
  test.setTimeout(300_000);
  const { page } = await launch(fixture.socket, {});
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await choices.getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });

  // Her's folder on this Mac, where her model would write the report.
  const bots = path.join(fixture.herHome ?? "", "bots");
  const folder = path.join(bots, fs.readdirSync(bots).find((id) => fs.existsSync(path.join(bots, id, "bot.json")))!, "files");
  fs.mkdirSync(folder, { recursive: true });
  const report = path.join(folder, "季度报告.md");

  // Sent: the button counts one new file.
  fs.writeFileSync(report, "# 季度报告\n\n初稿：只有营收。\n");
  await send("[[mock:attach:季度报告.md]] 发我报告");
  await expect(log.getByRole("button", { name: "查看 季度报告.md" })).toBeVisible({ timeout: 60_000 });
  const button = page.getByTestId("her-files");
  await expect(button).toHaveAttribute("aria-label", "文件，1 份新的", { timeout: 30_000 });

  // Changed and sent again: the same file, a version later; still one file new.
  fs.writeFileSync(report, "# 季度报告\n\n第二版：营收和利润。\n");
  await send("[[mock:attach:季度报告.md]] 改好再发");
  await expect(log.getByRole("button", { name: "查看 季度报告.md" })).toHaveCount(2, { timeout: 60_000 });
  await expect(button).toHaveAttribute("aria-label", "文件，1 份新的");
  await snap(page, "her-files-button");

  // The panel: the report once, new, with two versions.
  await button.click();
  const panel = page.getByRole("complementary", { name: "文件" });
  const row = panel.getByTestId("her-file-row").filter({ hasText: "季度报告" });
  await expect(row).toHaveCount(1, { timeout: 30_000 });
  await expect(panel.locator(".fhead").first()).toContainText("今天");
  await expect(row.getByLabel("新的")).toBeVisible();
  await expect(button).toHaveAttribute("aria-label", "文件");
  const versions = panel.getByRole("button", { name: "2 个版本" });
  await versions.click();
  await expect(panel.locator(".dlv-ver")).toHaveCount(2);
  await snap(page, "her-files-panel");

  // Each version as it was sent: the first is the draft, though the file now holds the second.
  await panel.locator(".dlv-ver").filter({ hasText: "第 1 版" }).click();
  const preview = page.getByTestId("her-file-preview");
  await expect(preview).toContainText("初稿：只有营收。", { timeout: 30_000 });
  await expect(preview).toContainText("第 1 版");
  await snap(page, "her-files-preview-v1");
  await preview.getByRole("radio", { name: "第 2 版" }).click();
  await expect(preview).toContainText("第二版：营收和利润。", { timeout: 30_000 });
  await expect(preview).not.toContainText("初稿");

  // Show in Conversation: the preview closes, and the message comes into view, marked a moment.
  await preview.getByRole("button", { name: "在对话中查看" }).click();
  await expect(preview).toBeHidden();
  await expect(log.locator(".rtn-flash")).toHaveCount(1, { timeout: 10_000 });

  // Reply from a row: the composer quotes the file, and Her is told which file it is.
  await row.hover();
  await row.getByRole("button", { name: "回复，让 Her 接着改" }).click();
  const bar = page.getByTestId("her-reply-bar");
  await expect(bar).toContainText("季度报告.md");
  await expect(composer).toBeFocused();
  await send("[[mock:send]] 加一节现金流");
  const heard = log.locator(".bm").filter({ hasText: "加一节现金流" });
  await expect(heard).toContainText("Replying to this earlier message of yours, quoted in part:", { timeout: 60_000 });
  await expect(heard.locator("blockquote")).toContainText("季度报告.md");

  // Search and the kinds: nothing found says so; a kind with none of them is empty.
  const search = panel.getByRole("textbox", { name: "搜文件名，或 Her 说过的话" });
  await search.fill("没有这个");
  await expect(panel.getByTestId("files-empty")).toContainText("没有找到", { timeout: 10_000 });
  await search.fill("季度");
  await expect(row).toHaveCount(1, { timeout: 10_000 });
  await panel.getByRole("radio", { name: "图片" }).click();
  await expect(panel.getByTestId("files-empty")).toContainText("没有找到", { timeout: 10_000 });
  await panel.getByRole("radio", { name: "文档" }).click();
  await expect(row).toHaveCount(1, { timeout: 10_000 });
  await search.fill("");

  // Grouped by what each came from: what the owner asked for in the conversation.
  await panel.getByRole("button", { name: "分组" }).click();
  await page.getByRole("menuitemradio", { name: "按来源" }).click();
  await expect(panel.locator(".fhead").first()).toContainText("对话里要的");

  // Closed, the list is seen: the next file is the only new one, and nothing old is marked.
  await panel.getByRole("button", { name: "关闭" }).click();
  await expect(panel).toBeHidden();
  fs.writeFileSync(path.join(folder, "数据.csv"), "a,b\n1,2\n");
  await send("[[mock:attach:数据.csv]] 数据也发我");
  await expect(button).toHaveAttribute("aria-label", "文件，1 份新的", { timeout: 60_000 });
  await button.click();
  await expect(panel.getByTestId("her-file-row")).toHaveCount(2, { timeout: 30_000 });
  await expect(panel.getByTestId("her-file-row").filter({ hasText: "数据" }).getByLabel("新的")).toBeVisible();
  await expect(row.getByLabel("新的")).toHaveCount(0);
});
