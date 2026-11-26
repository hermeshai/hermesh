import { expect, test, type Page } from "@playwright/test";
import { launch, pickFrom, sessionList } from "./app";

async function pickDemoAndScripted(page: Page, projectBtn: string, modelBtn: string) {
  await pickFrom(page.getByRole("button", { name: projectBtn }), page.getByTestId("project-item-demo"));
  await page.getByTestId("thread-composer-input").click();
  await pickFrom(page.getByRole("button", { name: modelBtn }), page.getByTestId("picker-provider-scripted").getByTestId("picker-model-scripted-pro"));
}

test("generates titles in the client's active UI language", async () => {
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

  // 2. Launch app in English (en) against the same daemon and submit a Chinese prompt.
  const en = await launch(undefined, { systemLanguages: ["en-US"] });
  try {
    await en.page.setViewportSize({ width: 1440, height: 800 });
    await en.page.getByTestId("nav-new-thread").click();
    await expect(en.page.getByTestId("thread-page")).toBeVisible();

    await pickDemoAndScripted(en.page, "Project", "Model");
    await en.page.getByTestId("thread-composer-input").fill("say 修复 auth_guard.ts 里的认证问题");
    await en.page.getByTestId("composer-submit-btn").click();

    const list = await sessionList(en.page);
    await expect(list.getByRole("listitem").filter({ hasText: "Fix auth_guard.ts issue" })).toBeVisible({ timeout: 15_000 });
    await expect(en.page.getByRole("heading", { name: "Fix auth_guard.ts issue" })).toBeVisible({ timeout: 15_000 });
  } finally {
    await en.app.close();
  }
});
