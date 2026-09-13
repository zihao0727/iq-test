const { chromium } = require('playwright');
const fs = require('node:fs');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  try {
    const page = await browser.newPage();
    await page.route('**/app.js', route => route.fulfill({
      contentType: 'application/javascript', body: fs.readFileSync('web/app.js', 'utf8'),
    }));
    await page.goto('http://127.0.0.1:8090');
    await page.locator('#gallery iframe').first().waitFor();
    for (const width of [1440, 1071, 390, 360, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await page.waitForTimeout(400);
      const frames = await page.locator('#gallery iframe').elementHandles();
      for (const frame of frames) {
        const content = await frame.contentFrame();
        const result = await content.evaluate(() => {
          const root = document.documentElement;
          const box = root.getBoundingClientRect();
          return {
            left: box.left, top: box.top, right: box.right, bottom: box.bottom,
            width: innerWidth, height: innerHeight,
            styles: document.querySelectorAll('#preview-fit-style').length,
            overflow: getComputedStyle(root).overflow,
          };
        });
        assert.equal(result.styles, 1);
        assert.equal(result.overflow, 'hidden');
        assert.ok(result.left >= -1 && result.top >= -1);
        assert.ok(result.right <= result.width + 1 && result.bottom <= result.height + 1, JSON.stringify(result));
      }
      await page.locator('#gallery').screenshot({ path: `artifacts/qa/preview-fit-${width}.png` });
    }
    console.log('PASS: real animation previews fully contained at desktop/mobile widths and after repeated resizing, with no internal scrollbars.');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
