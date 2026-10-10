const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const crypto = require('node:crypto');

async function main() {
  const base = process.env.ATLAS_HEALTH_UI_URL || 'http://127.0.0.1:5173';
  if (!['127.0.0.1', 'localhost'].includes(new URL(base).hostname)) throw new Error('UI checks are local-only');
  const bundlePath = process.env.ATLAS_HEALTH_TEST_BUNDLE;
  if (!bundlePath) throw new Error('ATLAS_HEALTH_TEST_BUNDLE is required');
  const output = process.env.ATLAS_HEALTH_UI_OUTPUT || '/tmp/atlas-health-ui-check';
  await fs.mkdir(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
  const page = await context.newPage();
  await context.addInitScript(() => { localStorage.setItem('atlas_lang', 'zh'); });
  const errors = [], uploads = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (['POST', 'PUT', 'PATCH', 'DELETE'].includes(request.method())) uploads.push(request.url()); });
  await page.route('**/api/**', route => route.fulfill({ status: 503, contentType: 'application/json', body: '{}' }));
  const checkWidth = async () => assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'page overflows viewport');
  const choose = async file => page.locator('input[type=file]').setInputFiles(file);
  try {
    await page.goto(`${base}/#/hardware`);
    await page.getByRole('heading', { name: '硬盘与内存健康', exact: true }).waitFor();
    await page.getByRole('heading', { name: '尚无硬盘健康证据' }).waitFor();
    await checkWidth();
    await page.screenshot({ path: path.join(output, 'empty-desktop.png'), fullPage: true });
    await choose(bundlePath);
    await page.getByText('文件完整性已校验').waitFor();
    assert.equal(await page.locator('.hh-table tbody tr').count(), 3);
    await page.getByRole('button', { name: '查看证据 public-nvme-historical-errors', exact: true }).click();
    await page.getByRole('heading', { name: 'Samsung SSD 970 EVO 500GB', exact: true }).waitFor();
    assert(await page.locator('.hh-evidence-table').innerText().then(text => text.includes('historical_media_errors')));
    await page.getByRole('tab', { name: '数据缺口' }).click();
    assert((await page.locator('.hh-gaps').innerText()).includes('unavailable'));
    await page.getByRole('tab', { name: '来源与身份' }).click();
    const expectedBundle = JSON.parse(await fs.readFile(bundlePath, 'utf8'));
    const expectedReport = JSON.parse(expectedBundle.report_json);
    const expectedTool = expectedReport.rows.find(row => row.reference === 'public-nvme-historical-errors').tool_version;
    assert((await page.locator('.hh-source').innerText()).includes(expectedTool));
    await page.getByRole('tab', { name: '规则与字段' }).click();
    await checkWidth();
    await page.screenshot({ path: path.join(output, 'report-desktop.png'), fullPage: true });
    await page.getByRole('textbox', { name: '搜索健康记录' }).fill('not-present');
    await page.getByRole('heading', { name: '没有匹配记录' }).waitFor();
    await page.getByRole('button', { name: '重置筛选' }).click();
    await page.getByLabel('数据状态', { exact: true }).selectOption('insufficient_data');
    assert.equal(await page.locator('.hh-table tbody tr').count(), 1);
    await page.getByLabel('数据状态', { exact: true }).selectOption('');
    await page.getByRole('tab', { name: /^内存/ }).click();
    await page.getByRole('heading', { name: '当前报告无内存原始证据' }).waitFor();
    await page.getByRole('tab', { name: /^RAID/ }).click();
    await page.getByRole('heading', { name: 'RAID 原始证据尚未接入' }).waitFor();
    await page.getByRole('tab', { name: /^硬盘/ }).click();
    await page.getByRole('tab', { name: /^硬盘/ }).focus();
    await page.keyboard.press('ArrowRight');
    await page.getByRole('heading', { name: '当前报告无内存原始证据' }).waitFor();
    await page.keyboard.press('Home');
    assert.equal(await page.locator('.hh-table tbody tr').count(), 3);

    const bad = path.join(output, 'tampered.json');
    await fs.writeFile(bad, JSON.stringify({ schema: 'hardware-health-bundle-v1', report_json: '{}', report_sha256: '0'.repeat(64) }));
    await choose(bad);
    await page.getByRole('alert').waitFor();
    assert.equal(await page.locator('.hh-table tbody tr').count(), 3, 'bad import replaced valid report');
    await page.getByRole('button', { name: '关闭错误' }).click();
    const downloadEvent = page.waitForEvent('download');
    await page.getByRole('button', { name: '导出原始报告' }).click();
    const saved = await downloadEvent;
    const exported = await fs.readFile(await saved.path());
    assert.equal(crypto.createHash('sha256').update(exported).digest('hex'), crypto.createHash('sha256').update(await fs.readFile(bundlePath)).digest('hex'));

    const paginated = structuredClone(expectedReport);
    const assessableRow = expectedReport.rows.find(row => row.assessment_status === 'assessed');
    assert(assessableRow);
    paginated.rows = Array.from({ length: 60 }, (_, i) => ({ ...assessableRow, reference: `synthetic-pagination-${i}`, entity_id: crypto.createHash('sha256').update(`synthetic-${i}`).digest('hex') }));
    paginated.summary = { total: 60, assessed: 60, unknown: 0, by_level: { [paginated.rows[0].level]: 60 } };
    const paginationJSON = JSON.stringify(paginated);
    const paginationFile = path.join(output, 'synthetic-pagination.json');
    await fs.writeFile(paginationFile, JSON.stringify({ schema: 'hardware-health-bundle-v1', report_json: paginationJSON, report_sha256: crypto.createHash('sha256').update(paginationJSON).digest('hex') }));
    await choose(paginationFile);
    await page.waitForFunction(() => document.querySelector('.hh-pagination')?.textContent.includes('1–25 / 60'));
    assert.equal(await page.locator('.hh-table tbody tr').count(), 25);
    await page.getByRole('button', { name: '下一页', exact: true }).click();
    assert((await page.locator('.hh-pagination').innerText()).includes('26–50 / 60'));
    await page.getByLabel('每页条数', { exact: true }).selectOption('100');
    assert.equal(await page.locator('.hh-table tbody tr').count(), 60);
    await choose(bundlePath);
    await page.waitForFunction(() => document.querySelector('.hh-pagination')?.textContent.includes('/ 3'));
    await page.getByRole('button', { name: '查看证据 public-nvme-historical-errors', exact: true }).click();

    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      await page.waitForFunction(() => document.querySelector('.sidebar').getBoundingClientRect().right <= 1);
      await checkWidth();
      await page.screenshot({ path: path.join(output, `report-mobile-${width}.png`), fullPage: true, animations: 'disabled' });
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.getByRole('button', { name: '外观' }).click();
    await page.getByRole('button', { name: '暗色', exact: true }).click();
    await page.screenshot({ path: path.join(output, 'report-dark.png'), fullPage: true });
    if (process.env.ATLAS_MEMORY_TEST_BUNDLE) {
      await choose(process.env.ATLAS_MEMORY_TEST_BUNDLE);
      await page.waitForFunction(() => document.querySelector('#hh-tab-memory')?.getAttribute('aria-selected') === 'true');
      assert.equal(await page.locator('.hh-table tbody tr').count(), 3);
      assert((await page.locator('.hh-table').innerText()).includes('DIMM 未定位'));
      await page.getByRole('button', { name: '查看证据 synthetic-memory-new-ue', exact: true }).click();
      assert((await page.locator('.hh-evidence-table').innerText()).includes('new_ue_errors'));
      await page.getByRole('tab', { name: /^数据缺口/ }).click();
      assert((await page.locator('.hh-gaps').innerText()).includes('UE 新增量'));
      await page.getByRole('tab', { name: '来源与身份', exact: true }).click();
      assert((await page.locator('.hh-source').innerText()).includes('synthetic-edac-driver'));
      await page.keyboard.press('ArrowLeft');
      assert.equal(await page.getByRole('tab', { name: /^数据缺口/ }).getAttribute('aria-selected'), 'true');
      await page.keyboard.press('Home');
      assert.equal(await page.getByRole('tab', { name: '规则与字段', exact: true }).getAttribute('aria-selected'), 'true');
      await page.screenshot({ path: path.join(output, 'memory-dark.png'), fullPage: true });
      await page.getByRole('button', { name: '外观' }).click();
      await page.getByRole('button', { name: '亮色', exact: true }).click();
      await page.screenshot({ path: path.join(output, 'memory-desktop.png'), fullPage: true });
      for (const width of [390, 320]) {
        await page.setViewportSize({ width, height: 844 });
        await page.waitForFunction(() => document.querySelector('.sidebar').getBoundingClientRect().right <= 1);
        await checkWidth();
        await page.screenshot({ path: path.join(output, `memory-mobile-${width}.png`), fullPage: true, animations: 'disabled' });
      }
      await page.setViewportSize({ width: 1440, height: 1000 });
      await page.getByLabel('风险等级', { exact: true }).selectOption('critical');
      assert.equal(await page.locator('.hh-table tbody tr').count(), 1);
      await page.getByRole('tab', { name: /^硬盘/ }).click();
      await page.getByRole('heading', { name: '尚无硬盘健康证据' }).waitFor();
      await page.getByRole('tab', { name: /^内存/ }).click();
      assert.equal(await page.locator('.hh-table tbody tr').count(), 3);
      await page.getByRole('button', { name: '查看证据 public-edac-incomplete', exact: true }).click();
      await page.getByRole('tab', { name: /^数据缺口/ }).click();
      assert((await page.locator('.hh-gaps').innerText()).includes('edac_counter_consistency'));
    }
    await page.getByTitle('English', { exact: true }).click();
    await page.getByRole('heading', { name: 'Disk & Memory Health', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Clear local report' }).click();
    await page.getByRole('heading', { name: 'No disk health evidence' }).waitFor();
    assert.deepEqual(errors, []);
    assert.deepEqual(uploads, [], 'report upload or mutation occurred');
    console.log(JSON.stringify({ status: 'passed', checks: ['empty', 'import', 'evidence', 'source', 'unknown', 'search', 'filters', 'memory_raid_gaps', 'keyboard_tabs', 'pagination_60_rows', 'tamper_preserves_report', 'export_identical', '320_390_1440_viewports', 'dark_theme', 'language', 'clear', 'no_mutations'], memory_checks: process.env.ATLAS_MEMORY_TEST_BUNDLE ? ['raw_memory_import', 'controller_precision', 'new_ue', 'ce_ue_deltas', 'driver_kernel_source', 'memory_filter', 'tab_filter_reset', 'public_missing_conflicting_fields', 'memory_desktop_mobile_dark'] : 'skipped', screenshots: output }));
  } catch (error) {
    await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true });
    const overflow = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth, scrollX, elements: Array.from(document.querySelectorAll('main *,aside')).filter(element => element.getBoundingClientRect().right > innerWidth || ['hh-table-scroll', 'hh-sr-only'].includes(element.className)).slice(0, 30).map(element => ({ tag: element.tagName, class: element.className, width: element.getBoundingClientRect().width, right: element.getBoundingClientRect().right, position: getComputedStyle(element).position, overflow: getComputedStyle(element).overflow })) }));
    console.error(JSON.stringify({ url: page.url(), errors, overflow, body: (await page.locator('body').innerText()).slice(0, 4000) }));
    throw error;
  } finally { await browser.close(); }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
