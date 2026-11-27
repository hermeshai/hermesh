import { expect, test } from "@playwright/test";
import { launch, snap } from "./app";

// The ⌘K palette: split-pane search with essential popover filters, keyword
// highlighting, live task preview, and keyboard navigation.
test("search finds a session with keyword highlight, previews it on the right, and ↩ opens it", async () => {
  const { app, page } = await launch();
  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  const palette = page.getByRole("dialog", { name: "搜索任务" });
  const results = palette.getByRole("listbox", { name: "搜索结果" });
  const preview = palette.getByRole("complementary", { name: "任务预览" });
  await expect(palette.getByRole("textbox", { name: "搜索任务" })).toBeFocused();
  // Grant filter stays hidden by default unless deep-linked from Settings.
  await expect(palette.getByRole("button", { name: /^授权/ })).toHaveCount(0);
  // Regression guard: result rows stay horizontal (no .pr flex-direction:column collision).
  const dir = await results.getByRole("option").first().evaluate((el) => getComputedStyle(el).flexDirection);
  expect(dir).toBe("row");

  await snap(page, "search");
  await page.keyboard.type("hello");
  const selected = results.getByRole("option", { selected: true });
  await expect(selected).toContainText("写 hello");
  await expect(selected.locator("mark.pal-hl")).toHaveText("hello");
  await expect(preview).toContainText("写 hello");
  await expect(palette).toContainText("任务 · 1 个");
  await snap(page, "search-hello");
  await page.keyboard.press("Enter");
  await expect(palette).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  await app.close();
});

test("↑↓ update both selection and preview pane, popover filters narrow tasks, Esc and scrim close", async () => {
  const { app, page } = await launch();
  const palette = page.getByRole("dialog", { name: "搜索任务" });
  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  const options = palette.getByRole("listbox", { name: "搜索结果" }).getByRole("option");
  const preview = palette.getByRole("complementary", { name: "任务预览" });
  await expect(options.first()).toHaveAttribute("aria-selected", "true");
  const second = (await options.nth(1).locator(".t").textContent()) ?? "";
  await page.keyboard.press("ArrowDown");
  await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(preview).toContainText(second);

  // Opening a filter menu and pressing Escape closes only the menu first, keeping the palette open.
  await palette.getByRole("button", { name: "项目" }).click();
  const menu = page.getByRole("menu", { name: "项目" });
  await expect(menu).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(palette).toBeVisible();

  // Picking a project filter hides generic quick actions when query is empty, and shows Reset.
  await palette.getByRole("button", { name: "项目" }).click();
  await page.getByRole("menu", { name: "项目" }).getByRole("menuitemradio").nth(1).click();
  await expect(palette.getByRole("button", { name: "重置" })).toBeVisible();
  await expect(palette).not.toContainText("快捷操作");
  await palette.getByRole("button", { name: "重置" }).click();
  await expect(palette).toContainText("快捷操作");

  await page.keyboard.press("Escape");
  await expect(palette).toHaveCount(0);

  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  await page.mouse.click(10, 400); // the scrim, beside the palette
  await expect(palette).toHaveCount(0);

  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: second })).toBeVisible();
  await app.close();
});

test("archived sessions are found only with 包含已归档", async () => {
  const { app, page } = await launch();
  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  const palette = page.getByRole("dialog", { name: "搜索任务" });
  const results = palette.getByRole("listbox", { name: "搜索结果" });
  await page.keyboard.type("归档的");
  await expect(palette).toContainText("没有找到任务");
  await palette.getByRole("checkbox", { name: "包含已归档" }).click();
  await expect(results.getByRole("option", { selected: true })).toContainText("归档的");
  await expect(results.getByRole("option", { selected: true })).toContainText("已归档");
  await app.close();
});
