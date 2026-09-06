#!/usr/bin/env node
/* Task010 B01-B18 strict browser acceptance matrix.
 *
 * The shell runner starts a source Vite server and a deterministic fixture
 * backed by the real repository/service/handler stack. This script contains
 * no SKIP/WARN path: exactly eighteen PASS rows are required for exit 0.
 */
'use strict';

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const baseURL = process.env.WEKNORA_BASE_URL;
const evidenceDir = process.env.TASK010_BROWSER_EVIDENCE;
const headless = process.env.HEADLESS !== 'false';
// Cold Vite compilation on a clean host can exceed the normal request budget.
// Keep a bounded but realistic browser navigation timeout.
const timeout = 60_000;

if (!baseURL || !evidenceDir) {
  console.error('WEKNORA_BASE_URL and TASK010_BROWSER_EVIDENCE are required');
  process.exit(2);
}

const ids = {
  completed: '00000000-0000-0000-0000-0000000000a1',
  unknownCost: '00000000-0000-0000-0000-0000000000b2',
  partial: '00000000-0000-0000-0000-0000000000c3',
  failed: '00000000-0000-0000-0000-0000000000e5',
  interrupted: '00000000-0000-0000-0000-0000000000f6',
  loading: '00000000-0000-0000-0000-00000000b002',
  polling: '00000000-0000-0000-0000-00000000b004',
  staleSlow: '00000000-0000-0000-0000-00000000a014',
  staleFast: '00000000-0000-0000-0000-00000000b014',
  zeroCost: '00000000-0000-0000-0000-000000000009',
  mixedCost: '00000000-0000-0000-0000-000000000010',
  unsupported: '00000000-0000-0000-0000-000000000012',
  otherTenant: '00000000-0000-0000-0000-0000000000aa',
};

const expectedIds = Array.from({ length: 18 }, (_, i) => `B${String(i + 1).padStart(2, '0')}`);
const results = [];
const consoleErrors = [];
const pageErrors = [];

fs.rmSync(evidenceDir, { recursive: true, force: true });
fs.mkdirSync(evidenceDir, { recursive: true });

function safeDetail(value) {
  return String(value).replace(/[\t\r\n]+/g, ' ').slice(0, 500);
}

async function capture(page, id) {
  const target = path.join(evidenceDir, `${id}.png`);
  await page.screenshot({ path: target, fullPage: true });
  return path.basename(target);
}

async function reportText(page) {
  return page.getByTestId('report-body').innerText();
}

async function openReport(page, runId) {
  await page.goto(`${baseURL}/platform/evaluations/${runId}`, { waitUntil: 'domcontentloaded', timeout });
  await page.getByTestId('report-body').waitFor({ state: 'visible', timeout });
}

async function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function execute(page, id, title, test) {
  try {
    await page.setViewportSize({ width: 1280, height: 900 });
    await test();
    const screenshot = await capture(page, id);
    results.push({ test_id: id, status: 'PASS', detail: title, evidence: screenshot });
    console.log(`${id}\tPASS\t${title}`);
  } catch (error) {
    let screenshot = '';
    try { screenshot = await capture(page, `${id}_FAIL`); } catch { /* page may have closed */ }
    results.push({ test_id: id, status: 'FAIL', detail: safeDetail(error.message), evidence: screenshot });
    console.error(`${id}\tFAIL\t${safeDetail(error.message)}`);
  }
}

async function main() {
  const browser = await chromium.launch({ headless, args: ['--no-sandbox'] });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await context.addInitScript(() => {
    const now = '2026-09-01T00:00:00Z';
    localStorage.setItem('locale', 'en-US');
    localStorage.setItem('weknora_token', 'task010-browser-fixture');
    localStorage.setItem('weknora_user', JSON.stringify({
      id: 'task010-browser-user', username: 'Browser Fixture', email: '',
      tenant_id: '42', is_system_admin: false, created_at: now, updated_at: now,
    }));
    localStorage.setItem('weknora_tenant', JSON.stringify({
      id: '42', name: 'Task010 Fixture', owner_id: 'fixture-owner',
      created_at: now, updated_at: now,
    }));
    localStorage.setItem('weknora_selected_tenant_id', '42');
    localStorage.setItem('weknora_selected_tenant_name', 'Task010 Fixture');
    localStorage.setItem('weknora_memberships', JSON.stringify([
      { tenant_id: 42, tenant_name: 'Task010 Fixture', role: 'viewer' },
    ]));
  });
  const page = await context.newPage();
  page.setDefaultTimeout(timeout);
  page.on('console', (msg) => { if (msg.type() === 'error') consoleErrors.push(msg.text()); });
  page.on('pageerror', (err) => pageErrors.push(err.message));

  await execute(page, 'B01', 'direct run URL renders stable run identity and lifecycle', async () => {
    await openReport(page, ids.completed);
    await assert((await page.getByTestId('evaluation-run-detail').locator('h2').innerText()).includes(ids.completed.slice(0, 8)), 'short run id missing from heading');
    await assert((await page.getByTestId('run-status').innerText()).trim().length > 0, 'text lifecycle status missing');
  });

  await execute(page, 'B02', 'loading is visible and no fabricated metric is shown before response', async () => {
    await page.goto(`${baseURL}/platform/evaluations/${ids.loading}`, { waitUntil: 'domcontentloaded', timeout });
    await page.getByTestId('report-loading').waitFor({ state: 'visible', timeout: 2_000 });
    await assert(await page.getByTestId('report-body').count() === 0, 'report body appeared before delayed response completed');
    await page.getByTestId('report-body').waitFor({ state: 'visible', timeout });
  });

  await execute(page, 'B03', 'completed run shows retrieval, answer, cost and latency together', async () => {
    await openReport(page, ids.completed);
    for (const testId of ['retrieval-quality', 'answer-quality', 'cost-summary', 'latency-summary']) {
      await assert(await page.getByTestId(testId).isVisible(), `${testId} is not visible`);
    }
    const text = await reportText(page);
    await assert(text.includes('0.9200') && text.includes('0.7200'), 'known retrieval/answer fixture values not rendered');
  });

  await execute(page, 'B04', 'running run polls and reaches COMPLETED without manual refresh', async () => {
    await openReport(page, ids.polling);
    await page.getByTestId('run-status').filter({ hasText: /RUNNING|Running/i }).waitFor({ timeout: 2_000 });
    await page.getByTestId('run-status').filter({ hasText: /COMPLETED|Completed/i }).waitFor({ timeout: 7_000 });
    await assert(await page.getByTestId('retrieval-quality').isVisible(), 'quality did not appear after terminal transition');
  });

  await execute(page, 'B05', 'failed run remains FAILED and quality is explicitly unknown', async () => {
    await openReport(page, ids.failed);
    const text = await reportText(page);
    await assert((await page.getByTestId('run-status').innerText()).match(/FAILED|Failed/i), 'FAILED status missing');
    await assert(text.includes('METRICS_INVALID') && /UNKNOWN|Unknown/i.test(text), 'failed quality boundary missing');
  });

  await execute(page, 'B06', 'interrupted run shows PROCESS_LOST and does not invent quality', async () => {
    await openReport(page, ids.interrupted);
    const text = await reportText(page);
    await assert((await page.getByTestId('run-status').innerText()).match(/INTERRUPTED|Interrupted/i), 'INTERRUPTED status missing');
    await assert(text.includes('PROCESS_LOST') && text.includes('METRICS_INVALID'), 'interruption reason or quality boundary missing');
  });

  await execute(page, 'B07', 'all-unknown cost is UNKNOWN and never displayed as currency zero', async () => {
    await openReport(page, ids.unknownCost);
    const text = await page.getByTestId('cost-summary').innerText();
    await assert(text.includes('ALL_COST_UNKNOWN') && /UNKNOWN|Unknown/i.test(text), 'unknown cost state missing');
    await assert(!/[¥$€]\s*0(?:\D|$)/.test(text) && !/0\s+(USD|CNY|EUR)/.test(text), 'unknown cost rendered as zero');
  });

  await execute(page, 'B08', 'partially known cost shows known subtotal and unknown-call count', async () => {
    await openReport(page, ids.partial);
    const text = await page.getByTestId('cost-summary').innerText();
    await assert(text.includes('SOME_COST_UNKNOWN') && text.includes('0.500000 USD'), 'known subtotal or PARTIAL reason missing');
    await assert(/Unknown(?:-price)? calls\s*1/i.test(text), 'unknown cost call count is not one');
  });

  await execute(page, 'B09', 'a measured zero cost remains an available zero in its real currency', async () => {
    await openReport(page, ids.zeroCost);
    const text = await page.getByTestId('cost-summary').innerText();
    await assert(/AVAILABLE|Available/i.test(text) && /0 CNY/.test(text), 'true zero CNY was not preserved as AVAILABLE');
  });

  await execute(page, 'B10', 'mixed currencies are not summed into a false total', async () => {
    await openReport(page, ids.mixedCost);
    const text = await page.getByTestId('cost-summary').innerText();
    await assert(text.includes('MIXED_CURRENCY') && /UNKNOWN|Unknown/i.test(text), 'mixed-currency state missing');
    await assert(!/8(?:\.0+)?\s+(USD|CNY)/.test(text), 'mixed currencies were falsely summed');
  });

  await execute(page, 'B11', 'run measurement status and tenant-window health are visibly distinct', async () => {
    await openReport(page, ids.partial);
    const text = await page.getByTestId('trust-summary').innerText();
    await assert(text.includes('PARTIAL'), 'PARTIAL measurement status missing');
    await assert(/not.*run|does not.*run|不是.*运行/i.test(text), 'tenant-window scope disclaimer missing');
  });

  await execute(page, 'B12', 'provider cache unsupported is distinct from a zero-percent miss', async () => {
    await openReport(page, ids.unsupported);
    const text = await page.getByTestId('cache-summary').innerText();
    await assert(/Unsupported\s*1/i.test(text), 'unsupported call count missing');
    await assert(!/0\s*%/.test(text), 'unsupported provider cache rendered as 0%');
  });

  await execute(page, 'B13', 'hard refresh restores the same run from stable URL identity', async () => {
    await openReport(page, ids.completed);
    await page.reload({ waitUntil: 'domcontentloaded', timeout });
    await page.getByTestId('report-body').waitFor({ timeout });
    await assert((await page.getByTestId('run-metadata').innerText()).includes(ids.completed), 'run identity changed after refresh');
  });

  await execute(page, 'B14', 'rapid SPA run switch cannot be overwritten by stale response', async () => {
    await openReport(page, ids.completed);
    await page.evaluate((url) => {
      const app = document.querySelector('#app').__vue_app__;
      void app.config.globalProperties.$router.push(url);
    }, `/platform/evaluations/${ids.staleSlow}`);
    await page.waitForTimeout(80);
    await page.evaluate(async (url) => {
      const app = document.querySelector('#app').__vue_app__;
      await app.config.globalProperties.$router.push(url);
    }, `/platform/evaluations/${ids.staleFast}`);
    await page.getByTestId('report-body').waitFor({ timeout });
    await page.waitForTimeout(1_000);
    const text = await page.getByTestId('run-metadata').innerText();
    await assert(text.includes(ids.staleFast) && !text.includes(ids.staleSlow), 'stale slow response overwrote selected run');
  });

  await execute(page, 'B15', 'viewer can read report but the report surface has no mutation action', async () => {
    await openReport(page, ids.completed);
    const root = page.getByTestId('evaluation-run-detail');
    await assert(await root.getByTestId('report-body').isVisible(), 'viewer cannot read report');
    const labels = (await root.getByRole('button').allInnerTexts()).join(' ');
    await assert(!/delete|remove|execute|run now|promote|删除|执行|提升/i.test(labels), `mutation control exposed to viewer: ${labels}`);
  });

  await execute(page, 'B16', 'cross-tenant 404 clears the previous report and leaks no identity', async () => {
    await openReport(page, ids.completed);
    await page.evaluate(async (url) => {
      const app = document.querySelector('#app').__vue_app__;
      await app.config.globalProperties.$router.push(url);
    }, `/platform/evaluations/${ids.otherTenant}`);
    await page.getByTestId('report-error').waitFor({ timeout });
    await assert(await page.getByTestId('report-body').count() === 0, 'previous tenant report remained visible after 404');
    const text = await page.getByTestId('evaluation-run-detail').innerText();
    await assert(!text.includes(ids.completed) && !text.includes(ids.otherTenant), 'run identity leaked in cross-tenant error body');
  });

  await execute(page, 'B17', 'keyboard focus and text/ARIA expose status, errors and warnings without color', async () => {
    await openReport(page, ids.completed);
    await page.keyboard.press('Tab');
    for (let i = 0; i < 20 && !(await page.getByTestId('refresh-report').evaluate((el) => el === document.activeElement)); i++) {
      await page.keyboard.press('Tab');
    }
    await assert(await page.getByTestId('refresh-report').evaluate((el) => el === document.activeElement), 'refresh control is not keyboard reachable');
    await assert((await page.getByTestId('run-status').innerText()).trim().length > 0, 'status is color-only');
    await assert(await page.getByTestId('report-warnings').getByText(/No warnings|无警告/i).count() > 0, 'warning state has no text alternative');
  });

  await execute(page, 'B18', '375px viewport keeps identity and four primary sections reachable without page overflow', async () => {
    await page.setViewportSize({ width: 375, height: 760 });
    await openReport(page, ids.completed);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
    await assert(!overflow, 'document has horizontal overflow at 375px');
    for (const testId of ['run-metadata', 'retrieval-quality', 'answer-quality', 'cost-summary', 'latency-summary']) {
      await assert(await page.getByTestId(testId).isVisible(), `${testId} is unreachable at mobile width`);
    }
  });

  await browser.close();

  const exactIds = results.map((r) => r.test_id);
  const idsValid = exactIds.length === 18 && new Set(exactIds).size === 18 && expectedIds.every((id) => exactIds.includes(id));
  const failures = results.filter((r) => r.status !== 'PASS');
  const applicationConsoleErrors = consoleErrors.filter((message) => !message.startsWith('Failed to load resource:'));
  const summary = {
    schema_version: 'task010-browser-matrix/v2',
    generated_at: new Date().toISOString(),
    environment: { browser: 'chromium', headless, frontend: 'vite-source', backend: 'real-report-stack-sqlite-fixture' },
    results,
    browser_diagnostics: {
      console_error_count: consoleErrors.length,
      application_console_error_count: applicationConsoleErrors.length,
      page_error_count: pageErrors.length,
    },
    verdict: idsValid && failures.length === 0 && applicationConsoleErrors.length === 0 && pageErrors.length === 0 ? 'PASS' : 'FAIL',
  };
  fs.writeFileSync(path.join(evidenceDir, 'browser_test_summary.json'), `${JSON.stringify(summary, null, 2)}\n`);
  const rows = ['test_id\tstatus\tdetail\tevidence'];
  for (const row of results) rows.push([row.test_id, row.status, safeDetail(row.detail), row.evidence].join('\t'));
  fs.writeFileSync(path.join(evidenceDir, 'browser_matrix.tsv'), `${rows.join('\n')}\n`);
  fs.writeFileSync(path.join(evidenceDir, 'browser_diagnostics.json'), `${JSON.stringify({ consoleErrors, pageErrors }, null, 2)}\n`);

  if (!idsValid || failures.length > 0 || applicationConsoleErrors.length > 0 || pageErrors.length > 0) {
    console.error(`Browser matrix FAIL: idsValid=${idsValid} failures=${failures.length} applicationConsoleErrors=${applicationConsoleErrors.length} pageErrors=${pageErrors.length}`);
    process.exit(1);
  }
  console.log('Browser matrix PASS: 18/18, no SKIP/WARN/pageerror');
}

main().catch((error) => {
  console.error(error && error.stack ? error.stack : String(error));
  process.exit(2);
});
