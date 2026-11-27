// Run with node --test after installing the desktop Playwright dependencies.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const desktop = createRequire(path.resolve(__dirname, '../../apps/desktop/package.json'));
const { chromium, expect } = desktop('@playwright/test');
const url = pathToFileURL(path.resolve(__dirname, '../agent-provider-install-demo.html')).href;

test('updates run in place and history follows each machine', async () => {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({viewport:{width:1200,height:800}});
    await page.clock.install();
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(url);
    const row = key => page.locator('[data-provider="' + key + '"]');
    const click = name => page.getByRole('button', {name, exact:true}).click();
    const select = name => page.getByRole('navigation').getByRole('button', {name:new RegExp(name)}).click();
    const firstRowY = (await row('gemini').boundingBox()).y;
    await click('更新');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.locator('#activity')).toBeHidden();
    await expect(row('gemini')).toContainText('检查环境');
    assert.equal((await row('gemini').boundingBox()).y, firstRowY);
    await select('我的 Mac');
    await row('qoder').getByRole('button').click();
    await expect(page.getByRole('dialog')).toContainText('yee@homebook');
    await click('在此机器安装');
    await page.locator('#show-activity').click();
    await expect(page.locator('.history-row')).toHaveCount(2);
    await expect(page.locator('.history-row').filter({hasText:'Gemini CLI'})).toContainText('开发服务器');
    await expect(page.locator('.history-row').filter({hasText:'Qoder'})).toContainText('我的 Mac');
    await page.clock.fastForward(6000);
    await expect(page.locator('.history-row').filter({hasText:'已完成'})).toHaveCount(2);
    await select('开发服务器');
    await expect(row('gemini')).toContainText('v0.41.0');
    await expect(row('qoder')).toContainText('安装 CLI');
    await select('我的 Mac');
    await expect(row('qoder')).toContainText('待登录');

    await page.locator('.tools summary').click();
    await page.getByLabel('此环境有活动会话').check();
    await click('空闲时更新');
    await expect(row('gemini')).toContainText('等待会话结束');
    await page.getByLabel('此环境有活动会话').uncheck();
    await page.clock.fastForward(6000);
    await expect(row('gemini')).toContainText('已是最新');
    await select('办公工作站');
    await expect(row('qoder').getByRole('button')).toBeDisabled();
    await click('模拟连接「办公工作站」');
    await click('Gemini CLI 更新设置');
    await expect(page.getByRole('dialog')).toContainText('npm install');
    await page.getByLabel('此环境空闲时自动更新').check();
    await click('保存更新设置');
    await expect(row('gemini')).toContainText('检查环境');
    await page.clock.fastForward(6000);
    await expect(row('gemini')).toContainText('v0.41.0');
    await page.reload();
    await page.locator('#show-activity').click();
    await expect(page.locator('.history-row')).toHaveCount(4);
    await expect(page.locator('.history-row').filter({hasText:'自动更新'})).toContainText('办公工作站');

    await page.setViewportSize({width:375,height:720});
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await select('开发服务器');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

test('remote task reports retain their identity and offer recovery appropriate to the last confirmed stage', async () => {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    await page.clock.install();
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    await page.goto(url);
    const card = page.getByRole('region', {name:'目标机器任务'});
    const row = page.locator('[data-provider="qoder"]');
    const scenario = page.getByLabel('下次任务场景');
    const start = async code => {
      await scenario.selectOption(code);
      await row.getByRole('button').click();
      await page.getByRole('button', {name:'在此机器安装'}).click();
      await row.getByRole('button', {name:'安装中'}).click();
    };
    await page.locator('.tools summary').click();
    await start('');
    await card.getByRole('button', {name:'取消任务'}).click();
    await expect(card).toContainText('已取消');
    await page.clock.fastForward(10000);
    await page.getByRole('button', {name:/开发服务器.*Ubuntu/}).click();
    await expect(row.getByRole('button')).toHaveText('安装 CLI');

    await start('denied');
    await page.clock.fastForward(1500);
    await expect(card).toContainText('没有安装目录的写权限');
    const jobId = await card.locator('code').textContent();
    await scenario.selectOption('timeout');
    await card.getByRole('button', {name:'重试'}).click();
    await page.clock.fastForward(2000);
    await expect(card.locator('.badge')).toHaveText('下载 CLI');
    await page.getByRole('button', {name:'模拟断开「开发服务器」'}).click();
    const lastReport = await card.innerText();
    await page.clock.fastForward(8000);
    assert.equal(await card.innerText(), lastReport);
    await expect(card.getByRole('button')).toHaveCount(0);
    await page.reload();
    await row.getByRole('button', {name:'等待重连'}).click();
    await expect(card).toContainText('等待重新连接');
    await page.locator('.tools summary').click();
    await page.getByRole('button', {name:'模拟连接「开发服务器」'}).click();
    await expect(card.locator('code')).toHaveText(jobId);
    await expect(card).toContainText('下载超时');
    await scenario.selectOption('verify');
    await card.getByRole('button', {name:'重试'}).click();
    await page.clock.fastForward(3000);
    await expect(card.locator('.badge')).toHaveText('写入安装目录');
    await expect(card.getByRole('button', {name:'取消任务'})).toHaveCount(0);
    await page.clock.fastForward(3000);
    await expect(card).toContainText('CLI 复检失败');
    await expect(card).toContainText('文件已写入');
    await card.locator('summary').click();
    const installs = card.locator('.events li').filter({hasText:'安装程序正在写入文件'});
    await expect(installs).toHaveCount(1);
    await card.getByRole('button', {name:'重新检测'}).click();
    await expect(card.locator('.badge')).toHaveText('验证 CLI');
    await page.clock.fastForward(1500);
    await expect(card.locator('.badge')).toHaveText('已完成');
    await expect(card.locator('code')).toHaveText(jobId);
    await expect(installs).toHaveCount(1);
    await page.getByRole('button', {name:/开发服务器.*Ubuntu/}).click();
    await expect(row).toContainText('待登录');
  } finally {
    await browser.close();
  }
});
