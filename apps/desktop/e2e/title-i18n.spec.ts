import { expect, test, type Page } from "@playwright/test";
import { launch, pickFrom, sessionList } from "./app";

async function pickDemoAndScripted(page: Page, projectBtn: string, modelBtn: string) {
  await pickFrom(page.getByRole("button", { name: projectBtn }), page.getByTestId("project-item-demo"));
  await page.getByTestId("thread-composer-input").click();
  await pickFrom(page.getByRole("button", { name: modelBtn }), page.getByTestId("picker-provider-scripted").getByTestId("picker-model-scripted-pro"));
}

test("generates titles in the client's active UI language and switches language on Regenerate Title", async () => {
  // 1. Launch app in Simplified Chinese (zh-CN) and submit an English prompt.
  const zh = await launch();
  try {
    await zh.page.setViewportSize({ width: 1440, height: 800 });
    await zh.page.getByTestId("nav-new-thread").click();
    await expect(zh.page.getByTestId("thread-page")).toBeVisible();

    await pickDemoAndScripted(zh.page, "项目", "模型");
    await zh.page.getByTestId("thread-composer-input").fill("say Fix authentication bug in session_guard.ts");
    await zh.page.getByTestId("composer-submit-btn").click();

    const list = await sessionList(zh.page);
    await expect(list.getByRole("listitem").filter({ hasText: "修复 session_guard.ts 问题" })).toBeVisible({ timeout: 15_000 });
    await expect(zh.page.getByRole("heading", { name: "修复 session_guard.ts 问题" })).toBeVisible({ timeout: 15_000 });
  } finally {
    await zh.app.close();
  }

  // 2. Launch app in English (en) against the same daemon and click "Regenerate Title" in the sidebar ⋯ menu.
  const en = await launch(undefined, { systemLanguages: ["en-US"] });
  try {
    await en.page.setViewportSize({ width: 1440, height: 800 });
    const list = await sessionList(en.page);
    const oldRow = list.getByRole("listitem").filter({ hasText: "修复 session_guard.ts 问题" });
    await expect(oldRow).toBeVisible({ timeout: 15_000 });

    await oldRow.hover();
    await oldRow.getByRole("button", { name: "More" }).click();
    const menu = en.page.getByRole("menu", { name: "More" });
    await menu.getByRole("menuitem", { name: "Regenerate Title" }).click();

    await expect(list.getByRole("listitem").filter({ hasText: "Fix session_guard.ts issue" })).toBeVisible({ timeout: 15_000 });
  } finally {
    await en.app.close();
  }
});
