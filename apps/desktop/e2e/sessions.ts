import type { Locator, Page } from "@playwright/test";

/** Opens a place from the sidebar, shown first when it is hidden. */
export async function navTo(page: Page, name: RegExp): Promise<void> {
  await showSidebar(page);
  await page.locator(".side-nav").getByRole("button", { name }).first().click();
}

/** Shows the sidebar when it is hidden (beside the page, or over it in a narrow window). */
async function showSidebar(page: Page): Promise<void> {
  await page.locator(".side-nav, .side-float").first().waitFor({ timeout: 30_000 });
  if (!(await page.locator(".side-nav").isVisible())) await page.getByRole("button", { name: /^(显示边栏|Show Sidebar)$/ }).click();
}

/** The session list, in the sidebar (docs/her-team.md §16), shown first when it is hidden. */
export async function sessionList(page: Page): Promise<Locator> {
  const list = page.getByRole("list", { name: /^(任务|Tasks)$/ });
  await showSidebar(page);
  await list.waitFor();
  return list;
}
