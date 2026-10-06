'use strict';
const assert = require('node:assert/strict');
const { chromium } = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');
const origin = process.env.KNOWL_BROWSER_URL;
(async () => {
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox']});
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    const requests = [], errors = [], violations = [];
    page.on('request', r => requests.push({url:r.url(),headers:r.headers()}));
    page.on('pageerror', error => errors.push(error.message));
    await page.exposeFunction('recordCSP', directive => violations.push(directive));
    await page.addInitScript(() => document.addEventListener('securitypolicyviolation', e => window.recordCSP(e.violatedDirective)));
    const connect = async (token = 'browser-secret-token') => {
      await page.locator('#operator-token').fill(token);
      await page.locator('#connect-form button').click();
    };
    await page.goto(origin + '/ui/knowledge');
    await connect();
    await page.locator('#protected-document').waitFor();
    assert.equal(await page.locator('#operator-token').inputValue(), '');
    assert.equal(await page.locator('#protected-document script, #protected-document img, #protected-document svg').count(), 0);
    assert.equal(await page.locator('#raw').textContent(), '<img src="https://evil.invalid/raw" onerror="window.pwned=true">');
    assert.equal(await page.locator('#json').textContent(), '{"content":"<script>window.pwned=true</script>"}');
    assert.equal(await page.locator('#protected-document a[href]').getAttribute('href'), 'https://evil.invalid/doc');
    assert.equal(await page.evaluate(() => window.pwned), undefined);
    // Trusted HTMX cannot send a token to other origins or unprotected/normalized paths.
    for (const path of ['https://evil.invalid/leak','/health','/ui/assets/ui.js','/ui/fragments/../../health']) {
      await page.evaluate(path => {htmx.ajax('GET',path,{target:'#screen'}).catch(()=>{});},path);
    }
    await page.locator('[data-screen="search"]').click();
    await page.locator('#protected-document').waitFor();
    assert.equal(new URL(page.url()).pathname, '/ui/search');
    await page.goBack();
    await page.locator('#protected-document').waitFor();
    assert.equal(new URL(page.url()).pathname, '/ui/knowledge');
    await page.locator('#disconnect').click();
    assert.equal(await page.locator('#protected-document').count(), 0);
    // A delayed response from the prior generation cannot repopulate the DOM.
    let release, started;
    const held = new Promise(resolve => {started=resolve;});
    await page.route('**/ui/fragments/knowledge', async route => {
      started(); await new Promise(resolve => {release=resolve;});
      await route.fulfill({status:200,contentType:'text/html',body:'<p id="stale">stale private response</p>'}).catch(()=>{});
    });
    await connect(); await held; await page.locator('#disconnect').click(); release();
    await page.unroute('**/ui/fragments/knowledge');
    await connect(); await page.locator('#protected-document').waitFor();
    assert.equal(await page.locator('#stale').count(), 0);
    await page.reload();
    assert.equal(await page.locator('#protected-document').count(), 0);
    assert.equal(await page.locator('#connection-status').textContent(), 'Disconnected');
    await connect('invalid-token');
    await page.waitForFunction(() => document.getElementById('connection-status').textContent === 'Disconnected');
    assert.equal(await page.locator('#protected-document').count(), 0);
    await connect(); await page.locator('#protected-document').waitFor();
    // Exercise the browser's persisted-pageshow cleanup contract explicitly.
    await page.evaluate(() => dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true})));
    assert.equal(await page.locator('#protected-document').count(), 0);
    assert.equal(await page.locator('#connection-status').textContent(), 'Disconnected');
    const storage = await page.evaluate(() => ({local:{...localStorage},session:{...sessionStorage}}));
    assert.deepEqual(storage.local, {});
    for(const [key,value] of Object.entries(storage.session)) {assert.equal(key,'htmx-current-path-for-history');assert.ok(value.startsWith('/ui/'));}
    assert.equal((await context.cookies()).length, 0);
    assert.equal(await page.evaluate(() => history.state), null);
    assert.deepEqual(errors, []); assert.deepEqual(violations, []);
    assert.ok(requests.some(r=>r.headers.authorization==='Bearer browser-secret-token'));
    for(const r of requests) {
      const u=new URL(r.url);assert.equal(u.origin,origin);
      assert.ok(!r.url.includes('browser-secret-token'));
      if(r.headers.authorization) assert.ok(u.pathname.startsWith('/ui/fragments/'));
    }
    await page.setViewportSize({width:390,height:844});
    await page.locator('[aria-label="Toggle navigation"]').click();
    await page.locator('[data-screen="sources"]').click();
    assert.equal(new URL(page.url()).pathname,'/ui/sources');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    console.log('PASS: sanitized content, CSP, no external requests, bounded token dispatch, logout, reload, 401, navigation, late response, persisted-pageshow, mobile');
  } finally {await browser.close();}
})().catch(error => {console.error(error);process.exitCode=1;});
