import { expect, test } from "@playwright/test";
import { fixture, launch, snap } from "./app";

// Her's page (docs/her-panel-demo.html), as the owner uses it, against her itself with her-mock as its model. Run by
// TestDesktopHerPage, which needs her built. One story: Her is turned on on work-mac, with Tencent Cloud's Token
// Plan (her-mock in its place) connected as she asks; her avatar opens her page beside the conversation.
// Under Abilities, her built-in skill is turned off and on and its page shows its SKILL.md; web search is switched
// off, which its card says, and on again; Her is let control browsers and a site is set apart. Under Settings, the
// Model page lists the plan connected, which opens its models; her Environment page opens the storage of her data.
// Every page has its way back to her page.

test("Her's page: its tabs, and the pages its cards and rows open beside the conversation", async () => {
  test.setTimeout(240_000);
  const { page } = await launch(fixture.socket, {});
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await choices.getByRole("listitem").filter({ hasText: "work-mac" }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
  const log = page.getByRole("log", { name: "对话" });
  await expect(log.getByText(/^你好，我是 .+，在 work-mac 上为你做事。开始之前，我需要接上一个模型/)).toBeVisible({ timeout: 120_000 });
  await log.getByRole("group", { name: "接入方式" }).getByRole("button", { name: "接入 Token Plan" }).click();
  await log.getByRole("group", { name: "Token Plan" }).getByRole("button", { name: "腾讯云 Token Plan" }).click();
  await log.getByLabel("腾讯云 Token Plan 的 API Key").fill("sk-e2e-plan-key");
  await log.getByRole("button", { name: "保存" }).click();
  await expect(log).toContainText("已接上 腾讯云 Token Plan", { timeout: 60_000 });

  const herPage = page.getByRole("complementary", { name: "助手设置" });
  await page.getByTestId("her-avatar").click();
  await expect(herPage.getByRole("tab", { name: "工作" })).toHaveAttribute("aria-selected", "true");

  // Skills: her built-in skill-creator, turned off and on here, and its page with its files.
  await herPage.getByRole("tab", { name: "能力" }).click();
  await herPage.getByTestId("her-skills").click();
  const skills = page.getByTestId("her-skills-page");
  const creator = skills.getByTestId("her-skill-skill-creator");
  await expect(creator).toBeVisible({ timeout: 30_000 });
  await expect(skills).toContainText("还没有自己的技能");
  await expect(skills).toContainText("内置");
  await snap(page, "her-page-skills");
  const toggle = skills.getByTestId("her-skill-switch-skill-creator");
  await expect(toggle).toHaveAttribute("aria-checked", "true");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-checked", "false", { timeout: 30_000 });
  await expect(creator).toContainText("已停用");
  await creator.locator(".hp-open").click();
  const skill = page.getByTestId("her-skill-page");
  await expect(skill).toContainText("/skill:skill-creator", { timeout: 30_000 });
  await expect(skill).toContainText("Her 内置");
  await expect(skill).toContainText("只能手动调用");
  await skill.getByRole("button", { name: /^SKILL\.md/ }).click();
  await expect(skill.locator(".hp-file")).toContainText("name: skill-creator", { timeout: 30_000 });
  await snap(page, "her-page-skill");
  const on = skill.getByTestId("her-skill-on");
  await expect(on).toHaveAttribute("aria-checked", "false");
  await on.click();
  await expect(on).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
  await skill.getByRole("button", { name: "返回" }).click();
  await expect(toggle).toHaveAttribute("aria-checked", "true");
  await skills.getByRole("button", { name: "返回" }).click();

  // Web search: its card says how it stands; switched off on its page, the card says so, and on again.
  const searchCard = herPage.getByTestId("her-search");
  await expect(searchCard).toHaveAttribute("aria-label", "联网搜索，已开启", { timeout: 30_000 });
  await searchCard.click();
  const search = page.getByTestId("her-search-page");
  const master = search.getByTestId("her-search-on");
  await expect(master).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
  await expect(search).toContainText("搜索服务");
  await snap(page, "her-page-search");
  await master.click();
  await expect(page.getByText("联网搜索已关闭")).toBeVisible({ timeout: 30_000 });
  await expect(master).toHaveAttribute("aria-checked", "false");
  await search.getByRole("button", { name: "返回" }).click();
  await expect(searchCard).toHaveAttribute("aria-label", "联网搜索，已关闭", { timeout: 30_000 });
  await searchCard.click();
  await master.click();
  await expect(master).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
  await search.getByRole("button", { name: "返回" }).click();

  // The browsers: work-mac has no window of its own, so Her controls none until she is let; then the owner's
  // Chrome as it stands, and a site set apart from All Sites.
  const browserCard = herPage.getByTestId("her-browser");
  await expect(browserCard).toHaveAttribute("aria-label", "浏览器，已关闭", { timeout: 30_000 });
  await browserCard.click();
  const browser = page.getByTestId("her-browser-page");
  const control = browser.getByTestId("her-browser-on");
  await expect(control).toHaveAttribute("aria-checked", "false", { timeout: 30_000 });
  await control.click();
  await expect(control).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
  await expect(browser).toContainText("你的 Chrome");
  await expect(browser).toContainText("还没有例外");
  await browser.getByTestId("her-browser-site").fill("example.com");
  await browser.getByTestId("her-browser-site-add").click();
  await expect(page.getByText("example.com: 打开和操作")).toBeVisible({ timeout: 30_000 });
  await expect(browser).not.toContainText("还没有例外");
  await snap(page, "her-page-browser");
  await browser.getByRole("button", { name: "返回" }).click();
  await expect(browserCard).toHaveAttribute("aria-label", "浏览器，已开启", { timeout: 30_000 });

  // Settings: the Model page, with the providers she has, each opening its models.
  await herPage.getByRole("tab", { name: "设置" }).click();
  await herPage.getByTestId("her-model-page").click();
  const models = page.getByTestId("her-models-page");
  await expect(models).toContainText("使用订阅");
  await expect(models).toContainText("API 与自定义服务");
  await expect(models.getByTestId("her-chatgpt-sign-in")).toBeVisible();
  const provider = models.getByTestId("her-provider-tencent-token-plan");
  await expect(provider).toContainText("腾讯云 Token Plan", { timeout: 30_000 });
  await snap(page, "her-page-model");
  await provider.click();
  const providerPage = page.getByTestId("her-provider-page");
  await expect(providerPage.locator('[data-testid^="her-model-shown-"]').first()).toBeVisible({ timeout: 30_000 });
  await snap(page, "her-page-provider");
  await providerPage.getByRole("button", { name: "返回" }).click();
  await models.getByRole("button", { name: "返回" }).click();

  // Her environment, and the storage of her data there.
  await herPage.getByTestId("her-environment").click();
  const env = page.getByTestId("her-environment-page");
  await expect(env).toContainText("运行在");
  await expect(env.getByTestId("her-turn-off")).toBeVisible();
  await env.getByTestId("her-storage").click();
  const storage = page.getByTestId("her-storage-page");
  await expect(storage).toContainText("存放内容", { timeout: 30_000 });
  await expect(storage).toContainText("这个文件夹是 Her 的助手、对话、记忆和产出文件的唯一一份");
  await snap(page, "her-page-storage");
  await storage.getByRole("button", { name: "返回" }).click();
  await env.getByRole("button", { name: "返回" }).click();
  await expect(herPage.getByRole("tab", { name: "设置" })).toHaveAttribute("aria-selected", "true");
  await herPage.getByRole("button", { name: "关闭" }).click();
  await expect(herPage).toBeHidden();
});
