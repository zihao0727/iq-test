const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const out = path.resolve('artifacts/qa');
  fs.mkdirSync(out, { recursive: true });
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1080 }, deviceScaleFactor: 1 });
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await page.goto('http://127.0.0.1:8092');
  await page.getByText('等待服务端配置 API_KEY', { exact: true }).waitFor();
  assert.equal(await page.locator('#timeline button').count(), 144);
  assert.equal(await page.locator('#run-button').isDisabled(), true);
  assert.equal(await page.locator('#visitor-submit').isDisabled(), false);
  assert.equal(await page.locator('#visitor-url').inputValue(), '');
  assert.equal(await page.locator('#visitor-key').inputValue(), '');
  await page.screenshot({ path: path.join(out, 'desktop.png'), fullPage: true });
  await page.getByRole('button', { name: '检测题目与判定规则' }).click();
  await page.locator('#method-dialog').waitFor({ state: 'visible' });
  await page.locator('#method-dialog .close-dialog').click();
  for (const width of [390, 360, 768, 1024]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `overflow at ${width}`);
    await page.screenshot({ path: path.join(out, `mobile-${width}.png`), fullPage: true });
  }
  const now = new Date();
  const records = [
    { id: 'candy-1', kind: 'candy', status: 'pass', started: now.toISOString(), durationMs: 5400, outputTokens: 99 },
    { id: 'pelican-1', kind: 'pelican', status: 'generated', started: now.toISOString(), durationMs: 41000, outputTokens: 3400 },
    { id: 'candy-2', kind: 'candy', status: 'fail', started: new Date(now - 600000).toISOString(), durationMs: 5000, outputTokens: 100 },
    { id: 'pelican-2', kind: 'pelican', status: 'error', started: new Date(now - 600000).toISOString(), durationMs: 20000, outputTokens: 0, error: '上游 HTTP 401' },
  ];
  await page.route('**/api/status', route => route.fulfill({
    json: { model: 'gpt-6-astra', effort: 'low', endpoint: 'https://www.sevnx.lol/v1/responses', configured: true, running: false, manualEnabled: true, records, serverTime: new Date().toISOString(), next: new Date(Date.now() + 600000).toISOString() },
  }));
  await page.route('**/api/records/*', route => {
    const id = route.request().url().split('/').pop();
    route.fulfill({ json: { ...records.find(r => r.id === id), output: id.startsWith('candy') ? '21' : '<html><svg></svg></html>' } });
  });
  await page.route('**/art/*', route => route.fulfill({
    contentType: 'text/html',
    body: '<html><body style="margin:0;background:#edf8f2"><svg width="100%" height="220" viewBox="0 0 300 220"><circle cx="150" cy="110" r="60" fill="#2b997c"><animate attributeName="r" values="45;65;45" dur="2s" repeatCount="indefinite"/></circle><text x="150" y="200" text-anchor="middle">MOCK PREVIEW</text></svg></body></html>',
  }));
  let requests = 0;
  await page.route('**/api/run', route => {
    assert.equal(route.request().headers().authorization, 'Bearer admin-test');
    requests++;
    route.fulfill({ status: 202, json: { status: 'started' } });
  });
  await page.setViewportSize({ width: 1440, height: 1080 });
  await page.locator('#refresh').click();
  await page.getByText('50%', { exact: true }).waitFor();
  assert.equal(await page.locator('#gallery iframe').count(), 1);
  await page.screenshot({ path: path.join(out, 'populated-mock.png'), fullPage: true });
  await page.getByRole('button', { name: '检测记录', exact: true }).click();
  assert.equal(await page.locator('#history-rows tr').count(), 4);
  await page.locator('[data-kind="pelican"]').click();
  assert.equal(await page.locator('#history-rows tr').count(), 2);
  await page.locator('#status-filter').selectOption('error');
  assert.equal(await page.locator('#history-rows tr').count(), 1);
  await page.locator('#history-rows button').click();
  await page.locator('#detail-body').getByText('上游 HTTP 401', { exact: true }).waitFor();
  await page.locator('#detail-dialog .close-dialog').click();
  await page.locator('#run-button').click();
  await page.locator('#admin-token').fill('admin-test');
  await page.getByRole('button', { name: '确认运行' }).click();
  await page.locator('#run-dialog').waitFor({ state: 'hidden' });
  assert.equal(requests, 1);
  assert.equal(await page.locator('#admin-token').inputValue(), '');
  assert.equal(await page.evaluate(() => localStorage.length), 0);
  let visitorRequests = 0, selectedKind = 'both';
  await page.route('**/api/visitor', async route => {
    const input = route.request().postDataJSON();
    assert.equal(input.endpoint, 'https://custom.example/v1');
    assert.equal(input.key, 'sk-visitor-test');
    assert.equal(input.kind, selectedKind);
    assert.equal(input.consent, true);
    assert.equal(input.model, undefined);
    visitorRequests++;
    await route.fulfill({ status: 202, json: { id: `private-${visitorRequests}` } });
  });
  await page.route('**/api/visitor/*', route => route.fulfill({
    json: { id: route.request().url().split('/').pop(), running: false, created: now.toISOString(), records: [
      ...(selectedKind !== 'pelican' ? [{ ...records[0], output: '21' }] : []),
      ...(selectedKind !== 'candy' ? [{ ...records[1], output: '<html><svg></svg></html>' }] : []),
    ] },
  }));
  await page.route('**/visitor-art/*/*', route => route.fulfill({ contentType: 'text/html', body: '<html><svg width="300" height="180"><circle cx="100" cy="90" r="60" fill="#578e7a"/></svg></html>' }));
  await page.locator('#visitor-url').fill('https://custom.example/v1');
  await page.locator('#visitor-key').fill('sk-visitor-test');
  await page.locator('#toggle-key').click();
  assert.equal(await page.locator('#visitor-key').getAttribute('type'), 'text');
  await page.locator('#toggle-key').click();
  await page.locator('#visitor-consent').check();
  await page.locator('#visitor-submit').click();
  await page.locator('.visitor-result .badge.generated').waitFor();
  assert.equal(await page.locator('.visitor-result').count(), 2);
  assert.equal(await page.locator('#visitor-key').inputValue(), '');
  assert.equal(await page.locator('#accuracy').textContent(), '50%');
  await page.locator('.visitor-result').nth(1).getByRole('button', { name: '查看结果' }).click();
  await page.locator('#detail-body iframe').waitFor();
  assert.equal(await page.locator('#detail-body iframe').getAttribute('sandbox'), 'allow-scripts');
  await page.locator('#detail-dialog .close-dialog').click();
  for (const testKind of ['candy', 'pelican']) {
    selectedKind = testKind;
    await page.locator(`[data-test="${testKind}"]`).click();
    await page.locator('#visitor-key').fill('sk-visitor-test');
    await page.locator('#visitor-submit').click();
    await page.waitForFunction(count => document.querySelectorAll('.visitor-result').length === count, testKind === 'candy' ? 3 : 4);
  }
  assert.equal(visitorRequests, 3);
  assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
  await page.route('**/api/visitor', route => route.fulfill({ status: 400, json: { error: '不允许访问本机或内网地址' } }));
  await page.locator('#visitor-key').fill('sk-visitor-test');
  await page.locator('#visitor-submit').click();
  await page.locator('#visitor-error').getByText('不允许访问本机或内网地址').waitFor();
  await page.locator('#visitor-key').fill('');
  await page.getByRole('button', { name: '监测总览', exact: true }).click();
  await page.screenshot({ path: path.join(out, 'visitor-results-mock.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: path.join(out, 'visitor-mobile-mock.png'), fullPage: true });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.getByRole('button', { name: '动画作品', exact: true }).click();
  assert.equal(await page.locator('#overview-view').isVisible(), false);
  assert.equal(await page.locator('#gallery iframe').getAttribute('sandbox'), 'allow-scripts');
  assert.deepEqual(errors, []);
  await browser.close();
  console.log('PASS: desktop/mobile layouts, monitor states, filters, admin trigger, custom URL/key, all three visitor modes, private preview, error state, no key storage and no browser errors.');
})().catch(error => { console.error(error); process.exit(1); });
