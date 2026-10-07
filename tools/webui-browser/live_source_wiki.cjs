'use strict';

const assert = require('node:assert/strict');
const { chromium } = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');

(async () => {
  const base = new URL(process.env.KNOWL_BROWSER_URL);
  const token = process.env.KNOWL_BROWSER_TOKEN;
  const operationID = process.env.KNOWL_BROWSER_OPERATION_ID;
  const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1366, height: 900 } });
    const errors = [];
    const requests = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => requests.push(request.url()));

    await page.goto(new URL('/ui/knowledge', base).href);
    assert.equal(await page.locator('#workspace-navigation').isVisible(), false);
    const guest = await page.request.get(new URL('/ui/fragments/knowledge', base).href);
    assert.equal(guest.status(), 401, 'guest cannot read the wiki fragment');
    await page.locator('#operator-token').fill(token);
    await page.locator('#connect-form button').click();
    await page.locator('.knowledge-article').waitFor();
    assert.equal(await page.locator('[data-error-code]').count(), 0, 'real root renders after authentication');

    await page.getByRole('link', { name: 'All pages', exact: true }).click();
    await page.locator('.wiki-tree').waitFor();
    assert.equal(await page.locator('.knowledge-article').count(), 0, 'tree does not open an article itself');
    await page.locator('[data-wiki-path="entities"] [data-tree-expand]').click();
    const article = page.locator('[data-wiki-path="entities/one.md"] a');
    await article.waitFor();
    await article.click();
    await page.locator('.knowledge-article').waitFor();
    assert.equal(await page.locator('.knowledge-article h2').textContent(), 'One');
    assert.equal(await page.locator('.page-details dd').first().textContent(), 'entities/one');

    await page.locator('[data-toggle-sources]').click();
    assert.equal(await page.locator('#page-sources .source-record').count(), 1);
    await page.getByRole('button', { name: 'Read saved source' }).click();
    await page.locator('.raw-view pre').waitFor();
    assert.equal(await page.locator('.raw-view pre').textContent(), 'source text');
    assert.equal(await page.locator('.raw-view .source-meta').textContent(), 'inline:source-1@1');

    await page.locator('[data-screen="search"]').click();
    await page.locator('#query').fill('One');
    await page.locator('#search-form button').click();
    await page.locator('.evidence-title').first().waitFor();
    assert.equal(await page.locator('.evidence-title').first().textContent(), 'One');
    assert.equal(await page.locator('.evidence-card').first().locator('.evidence-bottom').textContent(), 'entities/one');
    const searches = () => requests.filter(value => {
      const url = new URL(value);
      return url.pathname === '/ui/fragments/search' && url.searchParams.get('query') === 'One';
    }).length;
    assert.equal(searches(), 1);
    await page.locator('.evidence-title').first().click();
    await page.locator('.knowledge-article').waitFor();
    await page.goBack();
    await page.locator('.evidence-title').first().waitFor();
    assert.equal(await page.locator('#query').inputValue(), 'One');
    assert.equal(searches(), 1, 'Back restores results without another retrieval');

    await page.locator('[data-screen="operations"]').click();
    const operation = page.locator('.operation-select').filter({ hasText: operationID });
    await operation.waitFor();
    await operation.click();
    await page.locator('.operation-detail').waitFor();
    assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-id'), operationID);
    assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-status'), 'completed');

    assert.deepEqual(errors, [], 'browser has no uncaught JavaScript errors');
    assert.deepEqual(requests.filter(value => new URL(value).origin !== base.origin), [], 'browser issues no external requests');
    const privacy = await page.evaluate(() => ({
      html: document.documentElement.outerHTML,
      url: location.href,
      history: JSON.stringify(history.state),
      local: JSON.stringify(localStorage),
      session: JSON.stringify(sessionStorage),
    }));
    for (const [place, value] of Object.entries(privacy)) {
      assert.equal(value.includes(token), false, `operator token leaked into ${place}`);
    }
    console.log('Real source → SQLite → wiki → authenticated UI/browser flow passed.');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
