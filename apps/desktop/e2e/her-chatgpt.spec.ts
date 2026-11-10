import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap } from "./app";

// Her's onboarding signs in to ChatGPT all the way, on this Mac, whose her has no model yet. her's
// ChatGPT fixture stands in for OpenAI and approves at once; Node's fetch stands in for the person in
// the browser. Run by TestDesktopHerChatGPT (her.spec.ts stops its sign-in halfway).

test("Her: the onboarding signs in to ChatGPT", async () => {
  test.setTimeout(240_000);
  // The app, a development build, opens the fixture's sign-in page although it is not https.
  const { app, page } = await launch(fixture.socket, {
    profile: fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-")),
    env: { HERMESH_TEST_CHATGPT_BASE: fixture.chatgpt! },
  });
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });

  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  await page.getByRole("list", { name: "选择 Her 在哪里运行" }).getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click();
  await expect(log.getByText(/^你好，我是 .+开始之前，我需要接上一个模型/)).toBeVisible({ timeout: 120_000 });
  await expect(composer).toBeDisabled();

  await app.evaluate(({ shell }) => {
    const opened: string[] = [];
    Object.assign(globalThis, { opened });
    shell.openExternal = async (url: string) => void opened.push(url);
  });
  const browser = () => app.evaluate(() => (globalThis as unknown as { opened: string[] }).opened);
  await log.getByRole("button", { name: "用 ChatGPT 登录" }).click();
  await expect.poll(browser, { timeout: 60_000 }).toHaveLength(1);
  // The fixture approves and sends the browser back to this Mac's hermeshd, as OpenAI does once the person
  // signed in; her, on the Her Server, then takes the tokens.
  const [signInPage] = await browser();
  expect((await fetch(signInPage)).status).toBeLessThan(400);

  await expect(log).toContainText("接上了 ChatGPT（tester@example.invalid）。", { timeout: 60_000 });
  await expect(log.getByText(/^好了，我会用 .+ 和你聊，想换可以去助手设置。有什么要我做的？$/)).toBeVisible({ timeout: 60_000 });
  await expect(log).toContainText("已接上 ChatGPT");
  await expect(composer).toBeEnabled();
  await snap(page, "her-chatgpt-signed-in");
  await app.close();
});
