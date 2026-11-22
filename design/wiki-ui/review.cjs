'use strict';

// Uses the repository's existing development tooling; no new dependencies.
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');
const {chromium} = require('../../tools/webui-browser/node_modules/playwright');
const url = process.env.WIKI_REFERENCE_URL || 'http://127.0.0.1:8093/wiki-ui/';
const output = path.join(__dirname, 'review');
fs.mkdirSync(output, {recursive: true});

(async () => {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({viewport: {width: 1440, height: 1000}});
    const errors = [], requests = [], failedResponses = [];
    page.on('pageerror', (error) => errors.push(error.message));
    page.on('request', (request) => requests.push(request.url()));
    page.on('response', (response) => { if (response.status() >= 400) failedResponses.push(response.status()); });
    const settle = () => page.waitForTimeout(500); // Finite native CSS transitions.
    const shot = async (name) => {
      // Consistent handoff framing: include the header/context opener after navigation.
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.mouse.move(1000, 20);
      await settle();
      await page.screenshot({path: path.join(output, `${name}.png`)});
    };
    const load = async (width, height) => {
      await page.setViewportSize({width, height});
      await page.goto(url);
      await settle();
      await geometry(width);
    };
    const selected = () => page.locator('#article').getAttribute('data-page-id');
    const select = (key) => page.locator(`#wiki-tree [data-page="${key}"]`).click();
    const tab = async (name) => { await page.getByRole('tab', {name, exact: true}).click(); await settle(); };
    const focus = () => page.evaluate(() => document.activeElement.id);
    const openNavigation = async () => { await page.getByRole('button', {name: 'Открыть навигацию'}).click(); await settle(); };
    const openContext = async () => { await page.getByRole('button', {name: 'О странице', exact: true}).click(); await settle(); };
    async function geometry(width) {
      const state = await page.evaluate(() => {
        const ids = [...document.querySelectorAll('[id]')].map((element) => element.id);
        return {
          width: document.documentElement.scrollWidth, viewport: innerWidth,
          unique: new Set(ids).size === ids.length,
          sidebarX: document.querySelector('#wiki-navigation').getBoundingClientRect().x,
          context: getComputedStyle(document.querySelector('#page-context')).position,
          tabs: [...document.querySelectorAll('[role="tab"]')].map((element) => element.getBoundingClientRect().y),
        };
      });
      assert(state.width <= state.viewport, `page overflow at ${width}px`);
      assert(state.unique, 'duplicate DOM IDs');
      assert.equal(state.context, width >= 1200 ? 'sticky' : 'fixed');
      if (width >= 992) assert.equal(state.sidebarX, 0);
      if (width >= 1200) assert.equal(new Set(state.tabs).size, 1, 'wide tabs wrap');
    }

    const toggle = (trail) => page.locator(`[data-toggle-category="${trail}"]`);
    const choose = (trail) => page.locator(`#wiki-tree a[data-trail="${trail}"]`).click();
    const scenario = async (value) => {
      const disclosure = page.locator('main details').first();
      if (!(await disclosure.evaluate((element) => element.open))) await disclosure.locator('summary').click();
      await page.locator('#branch-scenario').selectOption(value);
    };
    const breadcrumbs = () => page.locator('#page-path li').allTextContents();
    const categoryTitles = ['Architecture and Workspace Semantics', 'Agent Maintainers and Maintenance Execution', 'Releases, Publishing, and Evaluation Harnesses', 'Retrieval, Service Transports, and Operator UI'];
    await load(1440, 1000);
    assert.equal(await selected(), 'index');
    assert.deepEqual(await page.locator('#article-body [data-members="catalog"] a').allTextContents(), categoryTitles);
    assert.equal(await page.locator('#wiki-tree a').evaluateAll((links) => links.some((link) => /\.md(?:$|\s)/.test(link.textContent))), false);
    assert.equal(await page.locator('#wiki-tree [data-page="architecture"]').count(), 1);
    await shot('desktop-category-root');
    await page.locator('.sidebar-brand').screenshot({path: path.join(output, 'logo-desktop.png')});
    await toggle('root/architecture').focus();
    await page.keyboard.press('Space');
    assert.equal(await selected(), 'index');
    assert.equal(await toggle('root/architecture').getAttribute('aria-expanded'), 'false');
    await page.locator('#wiki-tree a[data-trail="root/architecture"]').focus();
    await page.keyboard.press('Enter');
    assert.equal(await selected(), 'catalogs/architecture-and-workspace/index');
    assert.equal(await toggle('root/architecture').getAttribute('aria-expanded'), 'false');
    assert.deepEqual(await page.locator('#article-body [data-members="page"] a').allTextContents(), ['Knowl Overview and Operating Modes', 'Workspace Semantics and Source Lifecycle']);
    await page.locator('#article-body [data-page="workspace"]').click();
    assert.equal(await selected(), 'concepts/workspace-and-source-lifecycle');
    assert.deepEqual(await breadcrumbs(), ['Knowl Index', categoryTitles[0], 'Workspace Semantics and Source Lifecycle']);
    await toggle('root/architecture').click();
    await toggle('root/service').click();
    await select('transports');
    assert.equal(await toggle('root/architecture').getAttribute('aria-expanded'), 'true');
    assert.equal(await toggle('root/service').getAttribute('aria-expanded'), 'true');
    assert.equal(await page.locator('[data-occurrence="root/service/transports"] [data-toggle-category]').count(), 0);
    await shot('desktop-article');
    await tab('Источники');
    assert.equal(await page.locator('#sources [data-revision-state="saved"]').count(), 1);
    assert.equal(await page.locator('#sources [data-revision-state="unavailable"]').count(), 1);
    await page.locator('#source-records summary').click();
    assert(await page.locator('#source-records pre').isVisible());
    await shot('desktop-sources');
    await tab('Сведения');
    assert.equal(await page.locator('#page-details dd').first().textContent(), 'concepts/service-configuration-and-transports');
    await tab('Содержание');
    await page.locator('#outline a').nth(2).click();
    assert(await page.locator('#article-body h2, #article-body h3').nth(2).isVisible());
    await scenario('nested');
    await choose('root/team');
    assert.equal(await selected(), 'demo:team');
    assert.equal(await page.locator('#article-body [data-members="catalog"] a').count(), 1);
    assert.equal(await page.locator('#article-body [data-members="page"] a').count(), 1);
    await page.locator('#article-body [data-page="other"]').click();
    assert.equal(await selected(), 'demo:other');
    await page.locator('#page-path [data-page="team"]').click();
    assert.deepEqual(await breadcrumbs(), ['Knowl Index', 'Команда · пример']);
    await toggle('root/team/other').click();
    assert.equal(await selected(), 'demo:team');
    await choose('root/team/other');
    assert.equal(await selected(), 'demo:other');
    await shot('desktop-nested-category');
    await page.locator('#article-body [data-page="distant"]').click();
    assert.deepEqual(await breadcrumbs(), ['Knowl Index', 'Команда · пример', 'Процессы · пример', 'Рабочие договорённости · пример']);
    assert.equal(await page.locator('#wiki-tree [aria-current="page"]').getAttribute('data-trail'), 'root/team/other/distant');
    await choose('root/team/distant');
    assert.equal(await selected(), 'demo:distant');
    assert.deepEqual(await breadcrumbs(), ['Knowl Index', 'Команда · пример', 'Рабочие договорённости · пример']);
    assert.equal(await page.locator('#wiki-tree [aria-current="page"]').getAttribute('data-trail'), 'root/team/distant');
    assert.equal(await toggle('root/team/other').getAttribute('aria-expanded'), 'true');
    await page.locator('#article-body [data-page="unavailable"]').click();
    assert.equal(await selected(), 'demo:distant');
    assert.equal(await page.locator('#demo-feedback').getAttribute('data-code'), 'demo_page_unavailable');
    assert(await page.locator('#demo-feedback').isVisible());
    await tab('Источники');
    assert.equal(await page.locator('#sources [data-source-state="empty"]').count(), 1);
    await tab('Содержание');
    for (const [value, image] of [['empty', 'branch-empty'], ['loading', 'branch-loading'], ['failed', 'branch-error'], ['more', 'branch-more']]) {
      await scenario(value);
      assert.equal(await page.locator('#demo-branch').getAttribute('data-state'), value);
      assert.equal(await selected(), 'demo:distant');
      await select('overview'); await select('workspace');
      assert.equal(await page.locator('#demo-branch').getAttribute('data-state'), value);
      await page.locator('#tree-scroll').evaluate((element) => { element.scrollTop = element.scrollHeight; });
      await shot(image);
      if (value === 'empty') {
        assert.equal(await page.locator('#demo-branch a').count(), 0);
        await choose('root/demo');
        assert.equal(await selected(), 'demo:demo');
        assert.equal(await page.locator('#article-body [data-member-state="empty"]').count(), 1);
        assert.equal(await page.locator('#article-body [data-members]').count(), 0);
      }
      if (value === 'loading') {
        assert.equal(await page.locator('#demo-branch').getAttribute('aria-busy'), 'true');
        await page.getByRole('button', {name: 'Завершить демо-загрузку'}).click();
        assert.equal(await page.locator('#demo-branch').getAttribute('data-state'), 'normal');
      }
      if (value === 'failed') {
        assert.equal(await page.locator('#demo-branch [data-error-code]').getAttribute('data-error-code'), 'demo_branch_failed');
        await page.getByRole('button', {name: 'Retry', exact: true}).click();
        assert.equal(await page.locator('#demo-branch').getAttribute('data-state'), 'normal');
      }
      if (value === 'more') {
        const siblings = await page.locator('[data-occurrence="root/architecture"] a').count();
        assert.equal(await page.locator('#demo-branch a').count(), 2);
        await page.getByRole('button', {name: 'More pages'}).click();
        assert.equal(await page.locator('#demo-branch a').count(), 4);
        assert.equal(await page.locator('[data-occurrence="root/architecture"] a').count(), siblings);
        assert.equal(await page.getByRole('button', {name: 'More pages'}).count(), 0);
        await select('sample4'); await select('overview');
        assert.equal(await page.locator('#demo-branch a').count(), 4);
      }
      // Keep the next controlled branch check independent from page selection.
      await renderDistant();
    }
    async function renderDistant() {
      await scenario('nested'); await choose('root/team/distant');
    }
    await page.locator('#tree-scroll').evaluate((element) => { element.scrollTop = 35; });
    const scrollBefore = await page.locator('#tree-scroll').evaluate((element) => element.scrollTop);
    await page.locator('#wiki-tree [data-page="overview"]').evaluate((element) => element.click());
    assert.equal(await page.locator('#tree-scroll').evaluate((element) => element.scrollTop), scrollBefore);
    await select('transports');
    await page.locator('#article-body [data-page="overview"]').first().click();
    assert.deepEqual(await breadcrumbs(), ['Knowl Index', 'Knowl Overview and Operating Modes']);
    await page.locator('#page-path [data-page="root"]').click();
    assert.equal(await selected(), 'index');
    await page.getByRole('button', {name: 'Сбросить', exact: true}).click();
    assert.equal(await selected(), 'index');
    assert.equal(await page.locator('#wiki-tree [data-page="team"]').count(), 0);

    await load(1024, 900);
    await toggle('root/service').click(); await select('transports'); await shot('tablet-article');
    await openContext(); await tab('Сведения');
    await page.keyboard.press('Escape'); await settle();
    assert.equal(await focus(), 'context-toggle');

    await load(390, 844);
    await shot('mobile-article');
    await openNavigation();
    assert.equal(await focus(), 'navigation-close');
    assert(await page.locator('main').evaluate((element) => element.inert));
    await shot('mobile-tree');
    const first = page.locator('#wiki-navigation .brand-link');
    await first.focus(); await page.keyboard.press('Shift+Tab');
    assert(await page.locator('#wiki-navigation').evaluate((element) => element.contains(document.activeElement)));
    await page.keyboard.press('Tab');
    assert(await first.evaluate((element) => element === document.activeElement));
    await page.keyboard.press('Escape');
    assert.equal(await focus(), 'navigation-toggle');
    await openNavigation();
    await page.getByRole('button', {name: 'Закрыть навигацию'}).click();
    assert.equal(await focus(), 'navigation-toggle');
    await openNavigation();
    await toggle('root/architecture').click();
    assert.equal(await selected(), 'index');
    await choose('root/architecture');
    assert.equal(await focus(), 'article-title');
    assert(!(await page.locator('body').evaluate((element) => element.classList.contains('sidebar-open'))));
    assert(!(await page.locator('main').evaluate((element) => element.inert)));
    await page.locator('#article-body [data-page="workspace"]').click();
    assert.equal(await selected(), 'concepts/workspace-and-source-lifecycle');
    await openNavigation(); await toggle('root/service').click(); await select('transports');
    await geometry(390);
    await openContext(); await tab('Источники'); await shot('mobile-context');
    await page.keyboard.press('Escape'); await settle();
    assert.equal(await focus(), 'context-toggle');
    await openContext(); await tab('Содержание');
    const headingID = (await page.locator('#outline a').nth(2).getAttribute('href')).slice(1);
    await page.locator('#outline a').nth(2).click(); await settle();
    assert.equal(await focus(), headingID);
    await openContext();
    await page.locator('#navigation-toggle').evaluate((element) => element.click()); await settle();
    assert.equal(await page.locator('#page-context.show').count(), 0);
    assert(await page.locator('body').evaluate((element) => element.classList.contains('sidebar-open')));
    await page.locator('#context-toggle').evaluate((element) => element.click()); await settle();
    assert.equal(await page.locator('#page-context.show').count(), 1);
    assert(!(await page.locator('body').evaluate((element) => element.classList.contains('sidebar-open'))));
    await page.keyboard.press('Escape'); await settle();
    await load(320, 844);
    await page.locator('#article-body [data-page="service"]').click();
    await page.locator('#article-body [data-page="transports"]').click();
    await geometry(320); await shot('mobile-320');
    const table = page.locator('#article-body .table-responsive').first();
    assert(await table.evaluate((element) => element.scrollWidth > element.clientWidth));
    await table.evaluate((element) => { element.scrollLeft = element.scrollWidth; });
    assert(await table.evaluate((element) => element.scrollLeft > 0));
    await openNavigation(); await geometry(320); await page.keyboard.press('Escape');

    assert.deepEqual(errors, [], 'browser JavaScript errors');
    assert.deepEqual(failedResponses, [], 'failed local asset responses');
    assert(requests.every((request) => new URL(request).origin === new URL(url).origin), 'external request');
    assert(requests.every((request) => new URL(request).pathname.startsWith('/wiki-ui/') || new URL(request).pathname.startsWith('/web-ui/')), 'API or out-of-reference request');
    console.log('PASS: 1440×1000, 1024×900, 390×844, 320×844; category label/caret, nested/repeated-page context, branch states/Retry/More, source states, details/outline, focus/keyboard/drawers, no overflow/duplicate IDs/JS errors/failed assets/API or external requests.');
    console.log(`Saved 14 review screenshots to ${output}`);
  } finally {
    await browser.close();
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });
