'use strict';
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs/promises');
const {readable}=require('./assertions.cjs');
const { chromium } = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');

(async () => {
  const browser = await chromium.launch({headless:true,args:['--no-sandbox']});
  try {
    const page = await browser.newPage({viewport:{width:1440,height:1000}});
    const errors = [], requests = [];
    page.on('request',request=>requests.push(request.url()));
    page.on('pageerror', error => errors.push(error.message));
    const artifact = async name => {
      const directory = process.env.KNOWL_BROWSER_ARTIFACT_DIR;
      if (!directory) return;
      await fs.mkdir(directory,{recursive:true});
      await page.screenshot({path:path.join(directory,'wiki-integration-'+name+'.png'),fullPage:false});
    };
    const open = async query => {
      await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge'+query);
      assert.equal(await page.locator('#wiki-tree').evaluate(node=>node.childElementCount),0,'public shell contains no protected categories');
      await page.locator('#operator-token').fill('browser-secret-token');
      await page.locator('#connect-form button').click();
      await page.locator('#article-title').waitFor();
      await page.waitForFunction(()=>document.body.dataset.connectionState==='connected');
      assert.equal(await page.locator('.knowledge-article h1').count(),1);
      assert.equal(await page.locator('[data-error-code]').count(),0);
    };
    await open('');
    assert.equal(await page.locator('#article-title').textContent(),'Knowl Index');
    assert.equal(await page.locator('.document a').count(),4,'all real authored root links remain');
    assert.equal(await page.locator('#article-body > .document').count(),1,'authored body renders once');
    assert.equal(await page.locator('#article-body').getByRole('heading',{name:'Category navigation',exact:true}).count(),0,'sidebar navigation adds no generated body members');
    assert.equal(await page.locator('#article-body').getByRole('heading',{name:'Pages',exact:true}).count(),0,'no empty page group on the real root');
    assert.equal(await page.locator('#page-context').isVisible(),true);
    assert.equal(await page.locator('#contents-tab').getAttribute('aria-selected'),'true');
    await artifact('root-1440');
    await page.locator('#wiki-tree [data-page-node="AI/index"]').waitFor();
    const leaf=page.locator('#wiki-tree [data-page-node="releases/index"]');
    assert.equal(await leaf.count(),1,'index leaf remains a published page');
    assert.equal(await leaf.locator(':scope > [data-page-toggle]').count(),0,'known empty index has no disclosure');
    await leaf.locator(':scope > a').click();
    await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Balda releases');
    assert.equal(await page.locator('.document').innerText(),'Published release information.');
    assert.equal(await leaf.locator(':scope > [data-page-branch]').count(),0,'leaf offers no dead empty expansion');
    await artifact('leaf-index-1440');
    await page.goBack();await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Knowl Index');
    const ai=page.locator('#wiki-tree [data-page-node="AI/index"]');
    await ai.locator(':scope > [data-page-toggle]').click();
    await page.locator('#wiki-tree [data-page-node="AI/Prism"]').waitFor();
    assert.equal(await page.locator('#article-title').textContent(),'Knowl Index','caret keeps current root page');
    assert.equal(await page.locator('#wiki-tree [data-page-node="AI/OpenSpec"]').count(),1);
    assert.equal(await page.locator('#wiki-tree [data-page-node="concepts"]').count(),0,'technical directory never becomes a page row');
    const adrs=page.locator('#wiki-tree [data-page-node="concepts/ADRs/index"]');
    await adrs.locator(':scope > [data-page-toggle]').click();
    await page.locator('#wiki-tree [data-page-node="concepts/ADRs/first"]').waitFor();
    await readable(page,'#wiki-navigation strong,#wiki-tree [data-wiki-page],#wiki-tree [data-page-toggle]');
    await artifact('tree-root-1440');
    await ai.locator(':scope > a').click();
    await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='AI');
    assert.equal(await page.locator('.document').innerText(),'AI tools and techniques.\n\nAuthored reading\n\nUnrelated article\n\nKeep this additional information.');
    assert.equal(await ai.locator(':scope > [data-page-toggle]').getAttribute('aria-expanded'),'true','expanded physical tree persists across article swap');
    const treeTextFits=()=>{
      const scroller=document.getElementById('tree-scroll'),viewport=scroller.getBoundingClientRect();
      const labels=[...scroller.querySelectorAll('[data-wiki-page]')].filter(node=>node.getClientRects().length);
      return scroller.scrollLeft===0 && labels.every(node=>{const range=document.createRange();range.selectNodeContents(node);const text=range.getBoundingClientRect();return text.left>=viewport.left && text.right<=viewport.right;});
    };
    assert.equal(await page.evaluate(treeTextFits),true,'complete page labels stay inside independent tree viewport');
    await artifact('tree-ai-1440');
    for(const viewport of [{width:1024,height:900},{width:390,height:844},{width:320,height:844}]) {
      await page.setViewportSize(viewport);
      if(viewport.width<992) {
        await page.locator('#navigation-toggle').click();
        await page.waitForFunction(()=>document.getElementById('workspace-navigation').getAttribute('aria-modal')==='true');
      }
      assert.equal(await page.evaluate(treeTextFits),true,'tree labels stay readable at '+viewport.width);
      if(viewport.width<992)await readable(page,'#navigation-close');
      await artifact('tree-ai-'+viewport.width);
      if(viewport.width<992) {
        await page.locator('#navigation-close').click();
        await page.waitForFunction(()=>document.getElementById('workspace-navigation').getAttribute('aria-hidden')==='true');
      }
    }
    await page.setViewportSize({width:1440,height:1000});

    await page.locator('#wiki-tree [data-page-node="AI/Prism"] > a').click();
    await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Prism');
    assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Knowl Index','AI','Prism']);
    assert.equal(await page.locator('#sources-tab span').textContent(),'1','real accepted manifest resolves the published citation');
    await page.locator('#sources-tab').click();
    await page.waitForFunction(()=>getComputedStyle(document.getElementById('page-sources')).opacity==='1');
    await page.getByRole('button',{name:'Read saved source',exact:true}).click();
    await page.locator('.raw-view pre').waitFor();
    assert.equal(await page.locator('.raw-view pre').textContent(),'Original accepted Prism source.\n','v1 citation never substitutes newer accepted v2');
    assert.equal(await page.locator('.source-original').getAttribute('href'),'https://example.test/prism','original excludes query and fragment');
    await artifact('saved-source-1440');

    await page.goBack();
    await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='AI');
    await adrs.locator(':scope > a').click();
    await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='ADRs');
    assert.equal(await page.locator('.document').innerText(),'','empty index stays empty with sidebar children');
    assert.equal(await page.locator('#wiki-tree [data-page-node="concepts/ADRs/first"]').count(),1);
    await artifact('tree-empty-parent-1440');

    await open('?parent_id=catalogs%2Farchitecture-and-workspace%2Findex');
    assert.equal(await page.locator('#article-title').textContent(),'Architecture and Workspace Semantics');
    assert.equal(await page.locator('.document a').count(),2,'all real authored nested links remain');
    assert.equal(await page.locator('#article-body').getByRole('heading',{name:'Subcategories',exact:true}).count(),0,'no empty subcategory group on the real nested category');
    await artifact('category-1440');
    await open('?page_id=concepts%2Fservice-configuration-and-transports');
    assert.equal(await page.locator('.document table').count(),2,'real article pipe tables render semantically');
    assert.ok(await page.locator('#outline a').count()>3,'real displayed headings appear in Contents');
    const ids=await page.locator('.document [id]').evaluateAll(nodes=>nodes.map(node=>node.id));
    assert.equal(new Set(ids).size,ids.length);
    await artifact('article-1440');
    const articleURL=page.url();
    await page.locator('#sources-tab').click();
    assert.equal(await page.locator('#page-sources').isVisible(),true);
    await page.waitForFunction(()=>getComputedStyle(document.getElementById('page-sources')).opacity==='1');
    assert.equal(page.url(),articleURL,'context tabs keep the current article URL');
    assert.equal(await page.locator('#article-title').textContent(),'Service Configuration, HTTP/MCP Transports, and Operator API');
    assert.equal(await page.getByText('No saved source references are recorded for this page.',{exact:true}).isVisible(),true);
    await artifact('sources-1440');
    await page.locator('#contents-tab').click();
    for (const viewport of [{width:1024,height:900},{width:390,height:844},{width:320,height:844}]) {
      await page.setViewportSize(viewport);
      await page.evaluate(()=>scrollTo(0,0));
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,'article stays inside viewport '+viewport.width);
      assert.equal(await page.evaluate(()=>{const a=document.getElementById('header-view').getBoundingClientRect(),b=document.getElementById('connection-status').getBoundingClientRect();return a.right<=b.left || b.right<=a.left || a.bottom<=b.top || b.bottom<=a.top;}),true,'header controls never overlap '+viewport.width);
      assert.equal(await page.locator('#page-context').isVisible(),false,'compact context is a native drawer');
      await artifact('article-'+viewport.width);
      const table=page.locator('.document .table-responsive').first();
      await table.scrollIntoViewIfNeeded();
      if(viewport.width===320) {
        assert.equal(await table.evaluate(node=>node.scrollWidth>node.clientWidth),true,'wide table scrolls locally');
        await artifact('table-320');
      }
    }
    assert.deepEqual(errors,[]);
    assert.equal(requests.every(value=>new URL(value).origin===new URL(process.env.KNOWL_BROWSER_URL).origin),true,'ordinary page/source reading never fetches upstream');
    console.log('PASS: production templates and filesystem wiki, full indexes, native context, unique anchors, semantic tables and four-width containment');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
