#!/usr/bin/env node
'use strict';

const fs = require('fs');
const path = require('path');
const { chromium } = require('../task010/browser_matrix/node_modules/playwright');

const baseURL = process.env.WEKNORA_BASE_URL;
const email = process.env.TASK012_EMAIL;
const password = process.env.TASK012_PASSWORD;
const runID = process.env.TASK012_RUN_ID;
const evidenceDir = process.env.TASK012_BROWSER_EVIDENCE;

if (!baseURL || !email || !password || !runID || !evidenceDir) {
  console.error('WEKNORA_BASE_URL, TASK012_EMAIL, TASK012_PASSWORD, TASK012_RUN_ID and TASK012_BROWSER_EVIDENCE are required');
  process.exit(2);
}

const results = [];
const consoleErrors = [];
const pageErrors = [];

function record(id, ok, detail) {
  results.push({ id, status: ok ? 'PASS' : 'FAIL', detail });
  if (!ok) throw new Error(`${id}: ${detail}`);
}

async function main() {
  fs.rmSync(evidenceDir, { recursive: true, force: true });
  fs.mkdirSync(evidenceDir, { recursive: true });

  const browser = await chromium.launch({
    headless: true,
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    args: ['--no-sandbox'],
  });

  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'zh-CN' });
    const page = await context.newPage();
    page.setDefaultTimeout(60_000);
    page.on('console', (message) => {
      if (message.type() === 'error') consoleErrors.push(message.text());
    });
    page.on('pageerror', (error) => pageErrors.push(error.message));

    const dismissTour = async () => {
      const closeButtons = page.locator('.guide:visible .guide__close');
      for (let i = 0; i < 3 && await closeButtons.count(); i += 1) {
        try {
          await closeButtons.first().click({ force: true, timeout: 2_000 });
          await page.waitForTimeout(100);
        } catch { break; }
      }
    };

    await page.goto(`${baseURL}/login`, { waitUntil: 'domcontentloaded' });
    await page.locator('input[autocomplete="email"]').fill(email);
    await page.locator('input[autocomplete="current-password"]').fill(password);
    await page.locator('button[type="submit"]').click();
    await page.waitForURL(/\/platform(?:\/|$)/);
    await dismissTour();
    record('AUTH', true, '真实账户登录并进入平台');

    await page.goto(`${baseURL}/platform/settings?section=models`, { waitUntil: 'domcontentloaded' });
    await dismissTour();
    const usagePanel = page.locator('.model-usage:visible').last();
    await usagePanel.waitFor({ state: 'visible' });
    await page.waitForTimeout(1_500);
    await dismissTour();
    const allText = await usagePanel.innerText();
    fs.writeFileSync(path.join(evidenceDir, 'model_settings_all.txt'), `${allText}\n`);
    await page.screenshot({ path: path.join(evidenceDir, 'model_settings_all.png'), fullPage: true });
    record('UI01', /(逻辑调用|Logical calls)[\s\S]{0,30}\b5\b/i.test(allText), '租户窗口展示 5 次逻辑调用');
    record('UI02', /0\.001963\s*CNY|CNY\s*0\.001963/.test(allText), '租户窗口展示 0.001963 CNY');
    record('UI03', /(3\s*次调用价格未知|(?:未知(?:价格)?调用|Unknown(?:-price)? calls)[\s\S]{0,30}\b3\b)/i.test(allText), '租户窗口展示 3 次未知价调用');
    record('UI04', /(当前空间|current (?:space|tenant))/i.test(allText) && /(时间窗口|time window)/i.test(allText), '展示租户/空间与时间窗口边界说明');

    const modelSelect = usagePanel.locator('[aria-label="model-usage-model"]');
    await modelSelect.click();
    const modelOption = page.locator('.t-select-option:visible').filter({ hasText: 'Task012 Fixed Qwen3-14B' });
    await modelOption.last().click();
    await page.waitForTimeout(1_000);
    const selectedText = await usagePanel.innerText();
    fs.writeFileSync(path.join(evidenceDir, 'model_settings_chat.txt'), `${selectedText}\n`);
    await page.screenshot({ path: path.join(evidenceDir, 'model_settings_chat.png'), fullPage: true });
    record('UI05', /(逻辑调用|Logical calls)[\s\S]{0,30}\b2\b/i.test(selectedText), '模型筛选展示 2 次 Chat 调用');
    record('UI06', /0\.001963\s*CNY|CNY\s*0\.001963/.test(selectedText), '模型筛选总价保持 0.001963 CNY');

    await page.goto(`${baseURL}/platform/evaluations/${runID}`, { waitUntil: 'domcontentloaded' });
    await dismissTour();
    const root = page.getByTestId('evaluation-run-detail');
    await root.waitFor({ state: 'visible' });
    await page.getByTestId('report-body').waitFor({ state: 'visible' });
    const detailText = await root.innerText();
    fs.writeFileSync(path.join(evidenceDir, 'run_detail.txt'), `${detailText}\n`);
    await page.screenshot({ path: path.join(evidenceDir, 'run_detail.png'), fullPage: true });
    record('UI07', /COMPLETED|已完成/i.test(detailText), '真实 Evaluation Run 为 COMPLETED');
    record('UI08', detailText.includes('0.001963 CNY'), 'Run Report 展示 0.001963 CNY');
    record('UI09', /(逻辑调用|Logical calls)[\s\S]{0,30}\b5\b/i.test(detailText), 'Run Report 展示 5 次调用');
    record('UI10', /(3\s*次调用价格未知|(?:未知(?:价格)?调用|Unknown(?:-price)? calls)[\s\S]{0,30}\b3\b)/i.test(detailText), 'Run Report 展示 3 次未知价调用');

    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.getByTestId('report-body').waitFor({ state: 'visible' });
    const reloadText = await root.innerText();
    record('UI11', reloadText.includes('0.001963 CNY') && /(逻辑调用|Logical calls)[\s\S]{0,30}\b5\b/i.test(reloadText), '刷新后金额与调用数不变');

    let refreshFocused = false;
    for (let i = 0; i < 30; i += 1) {
      await page.keyboard.press('Tab');
      if (await page.getByTestId('refresh-report').evaluate((element) => element === document.activeElement)) {
        refreshFocused = true;
        break;
      }
    }
    record('UI12', refreshFocused, '刷新按钮键盘可达');
    record('UI13', (await page.getByTestId('run-status').innerText()).trim().length > 0, '运行状态具备文本表达');

    await page.setViewportSize({ width: 390, height: 844 });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.getByTestId('report-body').waitFor({ state: 'visible' });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
    await page.screenshot({ path: path.join(evidenceDir, 'run_detail_mobile.png'), fullPage: true });
    record('UI14', !overflow, '390px 视口无页面横向溢出');
    for (const id of ['run-metadata', 'cost-summary', 'usage-summary', 'trust-summary']) {
      record(`UI15-${id}`, await page.getByTestId(id).isVisible(), `${id} 在移动端可见`);
    }

    const applicationConsoleErrors = consoleErrors.filter((message) => !message.startsWith('Failed to load resource:'));
    record('UI16', applicationConsoleErrors.length === 0 && pageErrors.length === 0, '无应用控制台错误或页面异常');

    const summary = {
      schema_version: 'task012-real-browser/v1',
      run_id: runID,
      environment: { browser: 'Google Chrome', headless: true, frontend: 'vite-source', backend: 'real-postgresql-provider-stack' },
      results,
      diagnostics: { console_errors: consoleErrors, page_errors: pageErrors },
      verdict: 'PASS',
    };
    fs.writeFileSync(path.join(evidenceDir, 'browser_summary.json'), `${JSON.stringify(summary, null, 2)}\n`);
    console.log(results.map((row) => `${row.id}\t${row.status}\t${row.detail}`).join('\n'));
    console.log('verdict=PASS');
  } finally {
    await browser.close();
  }
}

main().catch((error) => {
  fs.mkdirSync(evidenceDir, { recursive: true });
  fs.writeFileSync(path.join(evidenceDir, 'browser_failure.txt'), `${error.stack || error}\n`);
  console.error(error.stack || error);
  process.exit(1);
});
