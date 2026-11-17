import type { Locator, Page } from "@playwright/test";

/** Opens a place from the sidebar, or from its rail when that is what shows (docs/her-team.md §16). */
export async function navTo(page: Page, name: RegExp): Promise<void> {
  const nav = page.locator(".side-nav, .rail-nav");
  await nav.first().waitFor({ timeout: 30_000 });
  await nav.getByRole("button", { name }).first().click();
}

/** The session list, in the sidebar (docs/her-team.md §16): the sidebar is expanded when only its rail shows. */
export async function sessionList(page: Page): Promise<Locator> {
  const list = page.getByRole("list", { name: /^(任务|Tasks)$/ });
  await page.locator(".side-nav, .rail-nav").first().waitFor({ timeout: 30_000 });
  if (!(await list.isVisible())) await page.getByRole("button", { name: /^(展开边栏|Expand Sidebar)$/ }).click();
  await list.waitFor();
  return list;
}
