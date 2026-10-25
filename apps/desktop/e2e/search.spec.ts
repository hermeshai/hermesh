import { expect, test } from "@playwright/test";
import { launch, snap } from "./app";

// The ⌘K palette: find a session by what the owner remembers of it, open it
// from the keyboard, or narrow the sidebar to what the palette's filters match.
test("search finds a session by its words and ↩ opens it", async () => {
  const { app, page } = await launch();
  await page.getByRole("navigation").getByRole("button", { name: /搜索/ }).click();
  const palette = page.getByRole("dialog", { name: "搜索会话" });
  const results = palette.getByRole("listbox", { name: "搜索结果" });
  await expect(palette.getByRole("textbox", { name: "搜索会话" })).toBeFocused();
  await snap(page, "search");
  await page.keyboard.type("hello");
  await expect(results.getByRole("option", { selected: true })).toContainText("写 hello");
  await expect(palette).toContainText("会话 · 1 个");
  await snap(page, "search-hello");
  await page.keyboard.press("Enter");
  await expect(palette).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  await app.close();
});

test("↑↓ choose the result, Esc and the scrim close the palette", async () => {
  const { app, page } = await launch();
  const palette = page.getByRole("dialog", { name: "搜索会话" });
  await page.getByRole("navigation").getByRole("button", { name: /搜索/ }).click();
  const options = palette.getByRole("listbox", { name: "搜索结果" }).getByRole("option");
  await expect(options.first()).toHaveAttribute("aria-selected", "true");
  const second = (await options.nth(1).locator(".t").textContent()) ?? "";
  await page.keyboard.press("ArrowDown");
  await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Escape");
  await expect(palette).toHaveCount(0);

  await page.getByRole("navigation").getByRole("button", { name: /搜索/ }).click();
  await page.mouse.click(10, 400); // the scrim, beside the palette
  await expect(palette).toHaveCount(0);

  await page.getByRole("navigation").getByRole("button", { name: /搜索/ }).click();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: second })).toBeVisible();
  await app.close();
});

test("archived sessions are found only with 包含已归档", async () => {
  const { app, page } = await launch();
  await page.getByRole("navigation").getByRole("button", { name: /搜索/ }).click();
  const palette = page.getByRole("dialog", { name: "搜索会话" });
  const results = palette.getByRole("listbox", { name: "搜索结果" });
  await page.keyboard.type("归档的");
  await expect(palette).toContainText("没有找到会话");
  await palette.getByRole("checkbox", { name: "包含已归档" }).click();
  await expect(results.getByRole("option", { selected: true })).toContainText("归档的");
  await expect(results.getByRole("option", { selected: true })).toContainText("已归档");
  await app.close();
});
