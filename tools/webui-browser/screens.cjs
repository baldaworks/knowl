'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const { chromium } = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');
// Hold an actual server-rendered response until the newer selection completes.
async function latestKnowledgeSelection(page, continuation) {
 let release, held;
 const gate=new Promise(resolve=>{release=resolve;});
 const ready=new Promise(resolve=>{held=resolve;});
 let delivered;
 const delivery=new Promise(resolve=>{delivered=resolve;});
 const isOld=url=>url.pathname==='/ui/fragments/knowledge' && (continuation?url.searchParams.has('cursor'):!url.searchParams.has('view')&&!url.searchParams.has('cursor'));
 const completed=new Promise(resolve=>{const done=request=>{if(!isOld(new URL(request.url())))return;page.off('requestfinished',done);page.off('requestfailed',done);resolve();};page.on('requestfinished',done);page.on('requestfailed',done);});
 await page.evaluate(continuation=>{
  window.knowledgeRace={newer:false};
  window.knowledgeRaceListener=event=>{
   const url=new URL(event.detail.requestConfig.path,location.href);
   if(url.pathname!=='/ui/fragments/knowledge')return;
   const older=continuation?url.searchParams.has('cursor'):!url.searchParams.has('view')&&!url.searchParams.has('cursor');
   if(!older)window.knowledgeRace.newer=true;
  };
  document.addEventListener('htmx:afterRequest',window.knowledgeRaceListener);
 },continuation);
 await page.route(isOld,async route=>{
  const response=await route.fetch();held();await gate;
  try {await route.fulfill({response});} finally {delivered();}
 });
 await page.getByRole('button',{name:continuation?'Next pages':'Topics',exact:true}).click();
 await ready;
 await page.getByRole('button',{name:continuation?'Topics':'All pages',exact:true}).click();
 await page.waitForFunction(()=>window.knowledgeRace.newer);
 const expected=continuation?'Topics':'ALL PAGES';
 assert.equal((await page.locator('.catalog-label').textContent()).trim(),expected);
 release();await delivery;
 await completed;
 await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 assert.equal((await page.locator('.catalog-label').textContent()).trim(),expected,'an older Knowledge response replaced the latest selection');
 assert.equal(await page.locator('[data-error-code]').count(),0);
 await page.unroute(isOld);
 await page.evaluate(()=>document.removeEventListener('htmx:afterRequest',window.knowledgeRaceListener));
}
(async () => {
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 try {
  const page=await browser.newPage({viewport:{width:1366,height:900}});
  const requests=[],violations=[];
  page.on('request',r=>requests.push(r.url()));
  await page.exposeFunction('recordCSP',v=>violations.push(v));
  await page.addInitScript(()=>document.addEventListener('securitypolicyviolation',e=>window.recordCSP(e.violatedDirective)));
  await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge');
  await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();
  await page.locator('.knowledge-article').waitFor();
  await page.locator('.catalog-item').filter({hasText:'All pages'}).click();
  const stale=page.waitForResponse(r=>r.status()===409);
  await page.getByRole('button',{name:'Next pages'}).click();await stale;
  await page.locator('.knowledge-article').waitFor();
  assert.equal(await page.locator('[data-error-code]').count(),0);
  await page.locator('[hx-target="#raw-source"]').click();await page.locator('.raw-view').waitFor();
  assert.equal(await page.locator('.raw-view pre').textContent(),'<script>immutable accepted text</script>');
  assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,0);
  await latestKnowledgeSelection(page,false);
  await latestKnowledgeSelection(page,true);
  await page.locator('[data-screen="search"]').click();await page.locator('#search-form').waitFor();
  assert.match(await page.locator('.search-disclosure').textContent(),/Embeddings are enabled/);
  await page.waitForFunction(()=>getComputedStyle(document.getElementById('search-loading')).display==='none');
  await page.locator('#query').fill('typed but not submitted');
  assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,0);
  for (const mode of ['lexical','hybrid','degraded','unreported']) {
   await page.locator('#query').fill(mode);await page.locator('#search-form button').click();
   assert.equal(await page.locator('#search-loading').isVisible(),true);
   await page.waitForFunction(mode=>{const code=document.getElementById('response-json-data');return code && JSON.parse(code.textContent).query===mode},mode);
   await page.waitForFunction(()=>getComputedStyle(document.getElementById('search-loading')).display==='none');
   const count=requests.filter(u=>new URL(u).searchParams.has('query')).length;
   await page.locator('[data-toggle-json]').click();
   const result=JSON.parse(await page.locator('#response-json-data').textContent());
   assert.deepEqual(await page.locator('.evidence-title').allTextContents(),result.evidence.map(e=>e.title));
   assert.deepEqual(await page.locator('.evidence-snippet').allTextContents(),result.evidence.map(e=>e.snippet));
   if(mode==='unreported')assert.equal(await page.locator('[data-effective]').count(),0);else assert.equal(await page.locator('[data-effective]').getAttribute('data-effective'),mode);
   const downloadPromise=page.waitForEvent('download');await page.locator('[data-export-json]').click();
   const download=await downloadPromise;assert.deepEqual(JSON.parse(await fs.readFile(await download.path(),'utf8')),result);
   assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,count);
   assert.equal(new URL(page.url()).search,'');
  }
  await page.locator('[data-toggle-snippet]').first().click();assert.equal(await page.locator('[data-toggle-snippet]').first().getAttribute('aria-expanded'),'true');
  await page.setViewportSize({width:390,height:844});
  await page.locator('.evidence-source-refs summary').first().click();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  await page.locator('.evidence-bottom a').first().click();await page.locator('.knowledge-article').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('page_id'),'concepts/other');
  assert.equal(await page.locator('.source-panel').isVisible(),false);
  await page.locator('[data-toggle-sources]').first().click();assert.equal(await page.locator('.source-panel').isVisible(),true);
  await page.locator('[data-toggle-catalog]').click();assert.equal(await page.locator('.catalog-panel').isVisible(),true);
  await page.evaluate(()=>document.querySelector('[data-screen="search"]').click());await page.locator('#search-form').waitFor();
  assert.equal(await page.locator('#query').inputValue(),'');assert.equal(await page.locator('.evidence-card').count(),0);
  const stored=await page.evaluate(()=>({local:{...localStorage},session:{...sessionStorage},cookies:document.cookie}));
  assert.deepEqual(stored.local,{});assert.equal(stored.cookies,'');
  for(const value of Object.values(stored.session))for(const secret of ['browser-secret-token','typed but not submitted','lexical','hybrid','degraded','unreported'])assert.equal(value.includes(secret),false);
  assert.deepEqual(violations,[]);
  assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,4);
  console.log('Knowledge latest-selection races (catalog and stale continuation), raw navigation, mobile provenance, explicit search, ordered evidence/JSON/download, disclosure, CSP, and no stored queries passed.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
