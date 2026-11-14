import type { Locator, Page } from "@playwright/test";

/** Opens a place from the sidebar, or from its rail when that is what shows (docs/her-team.md §14.9). */
export async function navTo(page: Page, name: RegExp): Promise<void> {
  const nav = page.locator(".side-nav, .rail-nav");
  await nav.first().waitFor({ timeout: 30_000 });
  await nav.getByRole("button", { name }).first().click();
}

/**
 * The session list's column (docs/her-team.md §14.2): All Sessions opens it when it is not showing; a session
 * chosen in it stays beside it.
 */
export async function sessionList(page: Page): Promise<Locator> {
  const list = page.getByRole("list", { name: /^(会话|Sessions)$/ });
  await page.locator(".side-nav, .rail-nav").first().waitFor({ timeout: 30_000 });
  if (!(await list.isVisible())) await navTo(page, /^(全部会话|All Sessions)/);
  return list;
}
