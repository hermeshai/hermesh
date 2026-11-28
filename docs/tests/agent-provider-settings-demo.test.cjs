// Run with: node --test docs/tests/agent-provider-settings-demo.test.cjs
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const desktop = createRequire(path.resolve(__dirname, '../../apps/desktop/package.json'));
const { chromium, expect } = desktop('@playwright/test');
const url = pathToFileURL(path.resolve(__dirname, '../agent-provider-settings-demo.html')).href;

function overlaps(a, b) {
  return a.x < b.x + b.width && a.x + a.width > b.x && a.y < b.y + b.height && a.y + a.height > b.y;
}

test('redesigned provider settings demo resolves layout overlap, unifies testing, and installs in place across Mac and iPhone', async () => {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1200, height: 900 } });
    await page.clock.install();
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    const errors = [];
    page.on('pageerror', err => errors.push(err.message));

    await page.goto(url);

    // 1. Providers list: Gemini CLI removed; no empty history button initially; Copilot/Qwen/Kimi/CodeBuddy/Grok installable; Cursor/Goose/Kiro external
    await expect(page.locator('.win')).not.toContainText('Gemini CLI');
    await expect(page.locator('.win')).not.toContainText('安装详情');
    await expect(page.locator('.win')).not.toContainText('安装与更新记录');
    const copilotRow = page.locator('[data-missing-row="copilot"]');
    await expect(copilotRow).toContainText('可由 Hermesh 安装');
    await expect(copilotRow.getByRole('button', { name: '安装…' })).toBeVisible();
    await expect(page.locator('[data-missing-row="qwen"]').getByRole('button', { name: '安装…' })).toBeVisible();
    const cursorRow = page.locator('[data-missing-row="cursor"]');
    await expect(cursorRow).toContainText('安装后会自动发现');
    await expect(cursorRow).toContainText('安装说明');

    // Qoder (app without CLI) offers direct one-click CLI installation
    await page.click('[data-jump="qoder"]');
    await expect(page.locator('.win')).toContainText('@qoder-ai/qodercli@1.1.100');
    await expect(page.locator('.win').getByRole('button', { name: '安装 CLI…' })).toBeVisible();
    await page.click('[data-jump="list"]');

    // 2. Install Copilot CLI via confirmation sheet (never navigating to an empty detail page)
    await copilotRow.getByRole('button', { name: '安装…' }).click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('在「yee-homebook」上安装 Copilot CLI？');
    await expect(dlg).toContainText('yipengfei');
    await expect(dlg).toContainText('@github/copilot@1.0.117');
    await expect(dlg).toContainText('默认全局目录');
    await dlg.getByRole('switch', { name: '空闲时自动更新稳定版' }).click();
    await dlg.getByRole('button', { name: '安装', exact: true }).click();
    await expect(dlg).toHaveCount(0);

    // Still on Agent 提供方 list with in-place preflight status & cancel button
    await expect(page.locator('.bar-top h1')).toHaveText('Agent 提供方');
    await expect(copilotRow).toContainText('正在预检');
    await expect(copilotRow.getByRole('button', { name: '取消' })).toBeVisible();

    // Bottom actions bar has both history and recheck on the SAME horizontal row
    const histBox = await page.locator('[data-nav="history"]').boundingBox();
    const recheckBox = await page.locator('[data-recheck="1"]').boundingBox();
    assert.ok(Math.abs(histBox.y - recheckBox.y) < 4, 'history and recheck buttons must sit on the same row');

    // Advance clock through preflight -> installing -> verifying -> succeeded
    await page.clock.runFor(1000);
    await expect(copilotRow).toContainText('正在安装');
    await page.clock.runFor(1000);
    await expect(copilotRow).toContainText('正在验证 CLI');
    await page.clock.runFor(1000);
    // Moved to installed list as unsigned!
    await expect(page.locator('[data-missing-row="copilot"]')).toHaveCount(0);
    await expect(page.locator('[data-nav="prov:copilot"]')).toContainText('还没登录');

    // 3. DeepSeek Harness page (Screenshot 1 scenario): Default Model first, CLI card second, zero overlap, unified Test
    await page.locator('[data-nav="prov:deepseek"]').click();
    await expect(page.locator('.win')).not.toContainText('验证命令行工具');
    await expect(page.locator('.win')).not.toContainText('用此机账号测试模型');
    const heads = await page.locator('.win .ghead').allTextContents();
    assert.deepEqual(heads, ['默认模型', '命令行工具']);

    // Verify no overlap between description text and Test button
    const testBtn = page.locator('[data-run-test="deepseek"]');
    const descEl = page.locator('.win [role="status"]');
    const btnBox = await testBtn.boundingBox();
    const descBox = await descEl.boundingBox();
    assert.ok(!overlaps(btnBox, descBox), 'description text must never overlap action button');

    // Run unified Test (normal success)
    await testBtn.click();
    await expect(descEl).toContainText('正在检查 CLI 协议握手');
    await page.clock.runFor(550);
    await expect(descEl).toContainText('CLI 协议正常');
    await page.clock.runFor(700);
    await expect(descEl).toContainText('可正常使用');

    // Run unified Test with simulated auth fault: proves CLI vs Account distinction without two buttons
    await page.selectOption('#sim-fault', 'auth');
    await testBtn.click();
    await page.clock.runFor(1300);
    await expect(descEl).toContainText('CLI 协议正常，但会话未通过');
    await page.selectOption('#sim-fault', '');

    // 4. Codex page: Managed update + busy idle queue + Switch for auto-update
    await page.click('[data-jump="codex"]');
    await page.check('#sim-busy');
    await expect(page.locator('[data-run-update="codex"]')).toHaveText('空闲时更新');
    await page.locator('[data-run-update="codex"]').click();
    await expect(page.locator('.win')).toContainText('等待机器空闲');
    await page.uncheck('#sim-busy');
    await page.clock.runFor(3200);
    await expect(page.locator('.win')).toContainText('0.172.0');
    await expect(page.locator('.win')).toContainText('已是最新');

    // 5. Switch to iPhone Settings view and verify unified single-page model + install modal
    await page.click('[data-plat="iphone"]');
    await page.click('[data-jump="list"]');
    await expect(page.locator('.phone')).toContainText('OpenCode');
    await page.locator('[data-missing-row="opencode"]').getByRole('button', { name: '安装…' }).click();
    await expect(page.getByRole('dialog')).toContainText('在「yee-homebook」安装 OpenCode？');
    await page.getByRole('dialog').getByRole('button', { name: '安装', exact: true }).click();
    await page.clock.runFor(3200);
    await expect(page.locator('.phone [data-nav="prov:opencode"]')).toContainText('还没登录');

    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});
