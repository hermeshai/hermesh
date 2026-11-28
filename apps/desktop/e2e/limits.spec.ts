import { expect, test } from "@playwright/test";
import { cli, launch, snap } from "./app";

// 额度, the overview's third view (docs/overview-usage.md §7.4): what each coding agent last said of its account's
// rate limits. The fixture's limits-mac ran a session in which its agent (the fake, as gemini) said five hours 86%
// used and a week 52%, on Pro; agy-mac's Antigravity is not queried (`agy -p /usage` disabled) and shows that Google's
// harnesses have no official compliant quota query support yet; Codex has not said any yet.
test("额度 shows what each agent last said, the least left first; a low window puts a dot on the 额度 tab that clears on click", async () => {
  const { app, page } = await launch();
  try {
    // Live: a low window is no longer a heavy card under 需要你处理; instead, 额度 carries a warning dot.
    const needs = page.getByRole("region", { name: "需要你处理" });
    await expect(needs.getByRole("listitem").filter({ hasText: "额度剩" })).toHaveCount(0);
    const limitsTab = page.getByRole("tab", { name: "额度" });
    const liveTab = page.getByRole("tab", { name: "实时" });
    await expect(limitsTab.getByTestId("limits-low-dot")).toBeVisible();

    // Clicking 额度 opens it and clears the dot; going back to 实时 keeps the dot gone.
    await limitsTab.click();
    await expect(limitsTab).toHaveAttribute("aria-selected", "true");
    await expect(limitsTab.getByTestId("limits-low-dot")).toHaveCount(0);
    await liveTab.click();
    await expect(liveTab).toHaveAttribute("aria-selected", "true");
    await expect(limitsTab.getByTestId("limits-low-dot")).toHaveCount(0);
    await limitsTab.click();
    await expect(limitsTab).toHaveAttribute("aria-selected", "true");

    const limits = page.getByRole("region", { name: "额度" });
    const cards = limits.getByRole("group").filter({ has: page.locator(".lim-id") });
    await expect(cards.first()).toHaveAttribute("aria-label", "Gemini CLI");
    const gemini = cards.first();
    await expect(gemini.locator(".lim-tx")).toContainText(/limits-mac 上报 · (刚刚|\d+ 分钟前)/);
    // Its plan, though a window runs low: that is said by the window, as it may be one of several.
    await expect(gemini.locator(".lim-badge")).toHaveText("Pro");
    const five = gemini.getByRole("group", { name: "5 小时窗口" });
    const week = gemini.getByRole("group", { name: "每周窗口" });
    await expect(five.locator(".lim-left")).toHaveText("14%剩余");
    await expect(five).toHaveClass(/\bwarn\b/);
    expect(await five.locator(".lim-left b").evaluate((el) => getComputedStyle(el).color)).not.toBe(await week.locator(".lim-left b").evaluate((el) => getComputedStyle(el).color));
    await expect(five.locator(".lim-reset")).toHaveText(/^(周. )?\d\d:\d\d 重置 · 还有 (5 小时|4 小时 \d+ 分)$/);
    await expect(week.locator(".lim-left")).toHaveText("48%剩余");
    await expect(week).not.toHaveClass(/\bwarn\b/);
    await expect(week.locator(".lim-reset")).toHaveText(/^周.+ \d\d:\d\d 重置$/);
    // The allowance's ticks light what is left; the time's line, what is left of the window (all of it, just started).
    expect(await five.locator(".lim-ticks u").evaluate((el) => el.getBoundingClientRect().width / el.parentElement!.getBoundingClientRect().width)).toBeCloseTo(0.14, 2);
    expect(await week.locator(".lim-time u").evaluate((el) => el.getBoundingClientRect().width / el.parentElement!.getBoundingClientRect().width)).toBeGreaterThan(0.98);
    // Google's harnesses have no official compliant quota query API (`agy -p /usage` is disabled).
    const agy = limits.getByRole("group", { name: "Antigravity", exact: true });
    await expect(agy.locator(".lim-none")).toHaveText("暂无官方合规余量查询支持");
    await expect(agy.locator(".lim-gname")).toHaveCount(0);
    await expect(agy.locator(".lim-badge")).toHaveCount(0);
    await expect(agy.locator(".lim-tx")).not.toContainText("上报");
    // An agent that reports its limits and has not yet: it says so.
    await expect(limits.getByRole("group", { name: "Codex" })).toContainText("还没有读到，用订阅账号登录后显示");
    await expect(limits).toContainText("Claude Code 和 Codex 查询");
    await expect(limits).toContainText("Hermesh 只转述，不读取账号");
    await snap(page, "overview-limits");

    // The agent says them again, lower: the page has it as the machine tells it, and the dot stays dismissed for the same window.
    await cli("session", "start", "--project", "limits-mac/quota", "-o", "json", "--agent", "gemini", "--title", "再用掉一些", "--prompt", "limits 300:93,10080:55,plan=pro\nsay 好了", "--wait");
    await expect(five.locator(".lim-left")).toHaveText("7%剩余", { timeout: 30_000 });
    await expect(week.locator(".lim-left")).toHaveText("45%剩余");
    await liveTab.click();
    await expect(limitsTab.getByTestId("limits-low-dot")).toHaveCount(0);
    await limitsTab.click();

    // In a narrow window the windows go under the name, and nothing runs off the side.
    await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setContentSize(900, 900));
    await expect.poll(async () => (await gemini.locator(".lim-ws").boundingBox())!.y - (await gemini.locator(".lim-id").boundingBox())!.y).toBeGreaterThan(40);
    expect(await page.locator(".ov").evaluate((el) => el.parentElement!.scrollWidth - el.parentElement!.clientWidth)).toBeLessThanOrEqual(1);
    await snap(page, "overview-limits-narrow");
  } finally {
    await app.close();
  }
});
