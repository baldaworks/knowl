'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const {readable}=require('./assertions.cjs');
const { chromium } = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');
async function visibleDetail(page,id) {
 const state=await page.locator('#'+id).evaluate(e=>{const start=e.querySelector('.raw-heading strong,h2,.empty-state') || e;const range=document.createRange();range.selectNodeContents(start);let r=range.getBoundingClientRect();const error=e.querySelector('[data-error-code] p');if(error){range.selectNodeContents(error);const message=range.getBoundingClientRect();r={top:Math.min(r.top,message.top),bottom:Math.max(r.bottom,message.bottom)};}return {top:r.top,bottom:r.bottom,height:innerHeight,headerBottom:Math.max(0,document.querySelector('.app-header').getBoundingClientRect().bottom),focused:document.activeElement===e};});
 assert.ok(state.top>=state.headerBottom && state.bottom<=state.height,'detail heading outside viewport: '+JSON.stringify(state));
 assert.equal(state.focused,true,'explicit detail read must focus '+id);
}
async function savedSourceOutcomes(page) {
 const cases=[
  {width:320,index:1,code:'source_revision_not_found',status:404},
  {width:390,index:2,code:'unsupported_format',status:415},
  {width:640,index:3,code:'read_limit_exceeded',status:413},
  {width:1024,index:4,network:true},
  {width:1366,index:4},
  {width:1366,index:6,nearBottom:true}
 ];
 for(const test of cases) {
  await page.setViewportSize({width:test.width,height:844});
  const control=page.locator('[hx-target="#raw-source"]').nth(test.index);
  const expected=new URL(await control.getAttribute('hx-get'),page.url()).searchParams.get('source_ref');
  const match=url=>url.pathname==='/ui/fragments/source-revision'&&url.searchParams.get('source_ref')===expected;
  let ready,release,delivered;
  const held=new Promise(resolve=>{ready=resolve;});const gate=new Promise(resolve=>{release=resolve;});const delivery=new Promise(resolve=>{delivered=resolve;});
  await page.route(match,async route=>{const response=await route.fetch();if(test.status)assert.equal(response.status(),test.status);ready();await gate;try{if(test.network)await route.abort('failed');else await route.fulfill({response});}finally{delivered();}},{times:1});
  if(test.nearBottom) {
   // Position the actual opener near the lower edge through ordinary user
   // scrolling before activation. The result is never scrolled by the test.
   const delta=await control.evaluate(e=>{const record=e.closest('.source-record');return record.getBoundingClientRect().bottom+parseFloat(getComputedStyle(record).marginBottom)-(innerHeight-69);});
   await page.mouse.wheel(0,delta);await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
   const predicted=await control.evaluate(e=>{const record=e.closest('.source-record');return record.getBoundingClientRect().bottom+parseFloat(getComputedStyle(record).marginBottom);});
   assert.ok(Math.abs(predicted-(844-69))<3,'near-bottom ordinary opener fixture must reach the measured boundary');
  }
  await control.focus();await page.keyboard.press('Enter');await held;
  assert.equal(await page.locator('#raw-source').getAttribute('aria-busy'),'true');
  assert.equal(await page.locator('#raw-source .empty-state').textContent(),'Loading saved source…');
  assert.equal(await page.locator('#raw-source').count(),1);
  await visibleDetail(page,'raw-source');
  release();await delivery;
  await page.waitForFunction(()=>!document.getElementById('raw-source').hasAttribute('aria-busy'),null,{timeout:5000});
  if(test.code) {
   assert.equal(await page.locator('#raw-source [data-error-code]').getAttribute('data-error-code'),test.code);
   assert.equal(await page.locator('#raw-source pre').count(),0);
  } else if(test.network) {
   assert.equal(await page.locator('#raw-source .empty-state').textContent(),'Request failed. Select again or refresh this view to retry.');
   assert.equal(await page.locator('#raw-source pre,[data-error-code]').count(),0);
  } else {
   assert.equal(await page.locator('.raw-view .source-meta').textContent(),expected,'opaque source_ref round-trips exactly');
   assert.equal(await page.locator('.raw-view pre').textContent(),'<script>immutable accepted text</script>');
   assert.equal(await page.locator('#raw-source script').count(),0);
  }
  await visibleDetail(page,'raw-source');
  await page.getByRole('button',{name:'Close saved source',exact:true}).click();
  assert.equal(await page.locator('#raw-source').textContent(),'');
  assert.equal(await control.evaluate(e=>e===document.activeElement),true);
  const position=await control.evaluate(e=>({top:e.getBoundingClientRect().top,bottom:e.getBoundingClientRect().bottom,header:document.querySelector('.app-header').getBoundingClientRect().bottom,height:innerHeight}));
  assert.ok(position.top>=position.header && position.bottom<=position.height,'close must return to a visible source opener: '+JSON.stringify(position));
 }
 await page.setViewportSize({width:1366,height:900});
 let ready,release,delivered;
 const held=new Promise(resolve=>{ready=resolve;});const gate=new Promise(resolve=>{release=resolve;});const delivery=new Promise(resolve=>{delivered=resolve;});
 const first=page.locator('[hx-target="#raw-source"]').first();
 const match=url=>url.pathname==='/ui/fragments/source-revision'&&url.searchParams.get('source_ref')==='git:docs/guide@accepted';
 await page.route(match,async route=>{const response=await route.fetch();ready();await gate;try{await route.fulfill({response});}catch{}finally{delivered();}},{times:1});
 await first.click();await held;await visibleDetail(page,'raw-source');
 await page.getByRole('button',{name:'Close saved source',exact:true}).click();release();await delivery;
 await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 assert.equal(await page.locator('#raw-source').textContent(),'','closing a pending read rejects its late response');
 assert.equal(await first.evaluate(e=>e===document.activeElement),true);
}
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
 const newerHref=await page.locator('.catalog-panel').getByRole('link',{name:continuation?'Root':'All pages',exact:true}).getAttribute('href');
 await page.locator('.catalog-panel').getByRole('link',{name:continuation?'Next pages':'Root',exact:true}).click();
 await ready;
 // The old anchor replaces the screen with Loading. Drive the newer saved
 // real navigation URL through the same public history restoration interface.
 await page.evaluate(href=>{history.pushState(null,'',href);dispatchEvent(new PopStateEvent('popstate'));},newerHref);
 await page.waitForFunction(()=>window.knowledgeRace.newer);
 const expected=continuation?'Root':'ALL PAGES';
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
  const artifact=async name=>{if(process.env.KNOWL_BROWSER_ARTIFACT_DIR)await page.screenshot({path:require('node:path').join(process.env.KNOWL_BROWSER_ARTIFACT_DIR,'task-012-'+name+'.png'),fullPage:false});};
  const requests=[],violations=[];
  page.on('request',r=>requests.push(r.url()));
  await page.exposeFunction('recordCSP',v=>violations.push(v));
  await page.addInitScript(()=>document.addEventListener('securitypolicyviolation',e=>window.recordCSP(e.violatedDirective)));
  await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge');
  await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();
  await page.locator('.knowledge-article').waitFor();
  await page.waitForFunction(()=>document.activeElement===document.getElementById('screen'),null,{timeout:5000});
  assert.equal(await page.locator('#screen').getAttribute('aria-label'),'Knowledge');
  assert.equal(await page.locator('.document h1').textContent(),'Root','connect must read canonical root');
  assert.equal(await page.locator('.knowledge-article h2').count(),0);
  assert.equal(await page.locator('#page-sources,[data-toggle-sources]').count(),0);
  await page.locator('.catalog-panel').getByRole('link',{name:'Unicode catalog',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Unicode catalog');
  assert.deepEqual(await page.evaluate(()=>history.state),{catalogIds:['index','catalogs/архитектура/🦉/index']});
  await page.locator('.document').getByRole('link',{name:'Distant leaf',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Distant leaf');
  assert.deepEqual(await page.evaluate(()=>history.state),{catalogIds:['index','catalogs/архитектура/🦉/index']},'Unicode history IDs must survive transport unchanged');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Unicode catalog','Distant leaf']);
  assert.equal(await page.locator('[data-toggle-sources] .counter').textContent(),'0');
  assert.equal(await page.locator('.source-panel').isVisible(),false);
  await page.locator('[data-toggle-sources]').click();
  assert.equal(await page.getByText('No saved source references are recorded for this page.',{exact:true}).isVisible(),true,'zero-source leaf remains truthful and accessible');
  await page.locator('[data-close-sources]').click();
  assert.equal(await page.locator('[data-toggle-sources]').evaluate(e=>e===document.activeElement),true);
  assert.equal(await page.locator('.panel-description').filter({hasText:'Topic navigation is unavailable.'}).count(),0);
  await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Root',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Root');
  assert.equal(await page.getByText('Topic navigation is unavailable.',{exact:true}).count(),0,'returning to root needs no optional trail');
  const catalogLink=page.locator('.catalog-panel').getByRole('link',{name:'Team',exact:true});
  assert.equal(new URL(await catalogLink.getAttribute('href'),page.url()).searchParams.get('parent_id'),'catalogs/team/index');
  await catalogLink.click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Team');
  const bodyLeaf=page.locator('.document').getByRole('link',{name:'Distant leaf',exact:true});
  assert.equal(new URL(await bodyLeaf.getAttribute('href'),page.url()).searchParams.get('parent_id'),'catalogs/team/index');
  await bodyLeaf.click();await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Distant leaf');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Team','Distant leaf']);
  await page.goBack();await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Team');
  await page.locator('.catalog-panel').getByRole('link',{name:'Other catalog',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Other catalog');
  await page.locator('.catalog-panel').getByRole('link',{name:'Distant leaf',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Distant leaf');
  assert.equal(new URL(page.url()).searchParams.get('page_id'),'sources/distant/leaf');
  assert.equal(new URL(page.url()).searchParams.get('parent_id'),'catalogs/other/index');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Team','Other catalog','Distant leaf']);
  assert.deepEqual(await page.evaluate(()=>history.state),{catalogIds:['index','catalogs/team/index','catalogs/other/index']});
  await page.goBack();await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Other catalog');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Team','Other catalog']);
  await page.goForward();await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Distant leaf');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Team','Other catalog','Distant leaf']);
  const root=page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Root',exact:true});
  const popupPromise=page.context().waitForEvent('page');await root.click({modifiers:['Control']});
  const popup=await popupPromise;await popup.waitForLoadState();assert.equal(new URL(popup.url()).pathname,'/ui/knowledge');assert.equal(await popup.locator('#connection-status').textContent(),'Disconnected');await popup.close();
  assert.equal(new URL(page.url()).searchParams.get('page_id'),'sources/distant/leaf');
  await page.reload();await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Distant leaf');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Other catalog','Distant leaf'],'reload cannot reconstruct unverified ancestry');
  await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Root',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Root');
  await readable(page,'#header-view,#connection-status,.panel-description,.source-kind,.source-meta,.source-note,.technical-details dt,.catalog-label');
  await artifact('knowledge-desktop');
  await page.locator('.catalog-item').filter({hasText:'All pages'}).click();
  const stale=page.waitForResponse(r=>r.status()===409);
  await page.getByRole('link',{name:'Next pages'}).click();await stale;
  await page.locator('.knowledge-article').waitFor();
  assert.equal(await page.locator('[data-error-code]').count(),0,await page.locator('#screen').textContent());
  for(const width of [320,390,639,640,768,1024,1366,1920]) {
   await page.setViewportSize({width,height:900});
   assert.equal(await page.locator('.source-panel').isVisible(),false,'provenance starts closed at '+width);
   assert.equal(await page.locator('[data-toggle-sources]').getAttribute('aria-expanded'),'false');
   assert.equal(await page.locator('.page-details').evaluate(e=>e.open),false);
   assert.equal(await page.getByText('Published article metadata',{exact:true}).isVisible(),false);
  }
  await page.setViewportSize({width:1366,height:900});
  await page.locator('[data-toggle-sources]').click();
  assert.equal(await page.locator('.source-record').count(),7);
  await page.locator('[hx-target="#raw-source"]').first().click();await page.locator('.raw-view').waitFor();
  await visibleDetail(page,'raw-source');
  assert.equal(await page.locator('#raw-source').getAttribute('tabindex'),'-1');
  assert.equal(await page.locator('#raw-source').evaluate(e=>e.previousElementSibling?.querySelector('[hx-target="#raw-source"]')?.getAttribute('aria-current')),'true');
  assert.equal(await page.locator('a[href="https://example.test/guide"]').count(),1,'selected revision original is shown once');
  assert.equal(await page.locator('.raw-view pre').textContent(),'<script>immutable accepted text</script>');
  assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,0);
  let rawReady,rawRelease,rawDone;
  const rawHeld=new Promise(resolve=>{rawReady=resolve;});const rawGate=new Promise(resolve=>{rawRelease=resolve;});const rawDelivered=new Promise(resolve=>{rawDone=resolve;});
  const rawPath=url=>url.pathname==='/ui/fragments/source-revision'&&url.searchParams.get('source_ref')==='git:docs/guide@accepted';
  await page.route(rawPath,async route=>{const response=await route.fetch();rawReady();await rawGate;try{await route.fulfill({response});}catch{}finally{rawDone();}},{times:1});
  await page.locator('[hx-target="#raw-source"]').first().click();await rawHeld;
  assert.equal(await page.locator('#raw-source').getAttribute('aria-busy'),'true');assert.equal(await page.locator('.raw-view').count(),0);
  await visibleDetail(page,'raw-source');
  await page.locator('[hx-target="#raw-source"]').last().click();
  await page.waitForFunction(()=>document.querySelector('.raw-view .source-meta')?.textContent.includes('@second'));
  rawRelease();await rawDelivered;await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  assert.equal((await page.locator('.raw-view .source-meta').textContent()).endsWith('@second'),true,'old saved source must not replace latest selection');
  assert.equal(await page.locator('[hx-target="#raw-source"]').last().getAttribute('aria-current'),'true');
  await visibleDetail(page,'raw-source');
  await page.getByRole('button',{name:'Close saved source',exact:true}).click();
  assert.equal(await page.locator('#raw-source').textContent(),'');
  assert.equal(await page.locator('[hx-target="#raw-source"]').last().evaluate(e=>e===document.activeElement),true);
  await savedSourceOutcomes(page);
  await latestKnowledgeSelection(page,false);
  await latestKnowledgeSelection(page,true);
  await page.goBack();await page.waitForFunction(()=>document.querySelector('.catalog-label')?.textContent.trim()==='ALL PAGES');
  assert.equal(new URL(page.url()).searchParams.get('view'),'all');
  assert.equal(await page.locator('.knowledge-article h2').textContent(),'Article');
  await page.goForward();await page.waitForFunction(()=>document.querySelector('.document h1')?.textContent==='Root');
  await page.locator('[data-screen="search"]').click();await page.locator('#search-form').waitFor();
  await page.waitForFunction(()=>document.activeElement===document.getElementById('screen'),null,{timeout:5000});
  assert.equal(await page.locator('#screen').getAttribute('aria-label'),'Search');
  assert.equal(await page.locator('#query').evaluate(e=>e.placeholder),'');
  assert.equal(await page.locator('#query').inputValue(),'');
  assert.equal(await page.locator('#search-form select').count(),0);
  assert.deepEqual(await page.locator('#search-form').evaluate(form=>[...new FormData(form).keys()]),['query']);
  assert.match(await page.locator('.search-disclosure').textContent(),/Embeddings are enabled/);
  await readable(page,'.search-disclosure,.form-label');
  await page.waitForFunction(()=>getComputedStyle(document.getElementById('search-loading')).display==='none');
  await page.locator('#query').fill('typed but not submitted');
  assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,0);
  for (const mode of ['lexical','hybrid','degraded','unreported']) {
   await page.locator('#query').fill(mode);await page.locator('#search-form button').click();
   assert.equal(await page.locator('#search-loading').isVisible(),true);
   await page.waitForFunction(mode=>{const code=document.getElementById('response-json-data');return code && JSON.parse(code.textContent).query===mode},mode);
   await page.waitForFunction(()=>getComputedStyle(document.getElementById('search-loading')).display==='none');
   const sent=new URL(requests.filter(u=>new URL(u).searchParams.has('query')).at(-1));
   assert.deepEqual([...sent.searchParams.keys()],['query'],'UI search submits whole-wiki query only');
   const count=requests.filter(u=>new URL(u).searchParams.has('query')).length;
   await page.locator('[data-toggle-json]').click();assert.equal(await page.locator('[data-toggle-json]').getAttribute('aria-expanded'),'true');
   const result=JSON.parse(await page.locator('#response-json-data').textContent());
   assert.deepEqual(await page.locator('.evidence-title').allTextContents(),result.evidence.map(e=>e.title));
   assert.deepEqual(await page.locator('.evidence-snippet').allTextContents(),result.evidence.map(e=>e.snippet));
   if(mode==='unreported')assert.equal(await page.locator('[data-effective]').count(),0);else assert.equal(await page.locator('[data-effective]').getAttribute('data-effective'),mode);
   const downloadPromise=page.waitForEvent('download');await page.locator('[data-export-json]').click();
   const download=await downloadPromise;assert.deepEqual(JSON.parse(await fs.readFile(await download.path(),'utf8')),result);
   assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,count);
   assert.equal(new URL(page.url()).search,'');
   await readable(page,'.status-badge,.evidence-bottom,.evidence-citations,.subtle-note');
   if(mode==='lexical')await artifact('search-desktop');
  }
  await page.locator('[data-toggle-snippet]').first().click();assert.equal(await page.locator('[data-toggle-snippet]').first().getAttribute('aria-expanded'),'true');
  await page.setViewportSize({width:390,height:844});
  await page.locator('.evidence-source-refs summary').first().click();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  await page.locator('.evidence-title').first().click();await page.locator('.knowledge-article').waitFor();
  await page.waitForFunction(()=>document.activeElement===document.getElementById('screen'),null,{timeout:5000});
  assert.equal(await page.locator('#screen').getAttribute('aria-label'),'Knowledge');
  assert.equal(new URL(page.url()).searchParams.get('page_id'),'concepts/other');
  assert.deepEqual(await page.locator('[aria-label="Breadcrumb"] li').allTextContents(),['Root','Article']);
  assert.equal(await page.locator('.source-panel').isVisible(),false);
  const sourcesToggle=page.locator('[data-toggle-sources]').first();
  await page.evaluate(()=>scrollTo(0,200));
  await sourcesToggle.focus();await page.keyboard.press('Enter');assert.equal(await page.locator('.source-panel').isVisible(),true);
  assert.equal(await sourcesToggle.getAttribute('aria-expanded'),'true');
  assert.equal(await page.locator('[data-close-sources]').evaluate(e=>e===document.activeElement),true);
  await page.locator('.page-details summary').click();
  assert.equal(await page.getByText('Published article metadata',{exact:true}).isVisible(),true);
  assert.equal(await page.locator('.page-details dd').filter({hasText:'navigation provenance'}).isVisible(),true);
  await page.locator('.source-identity summary').last().click();
  await page.locator('[hx-target="#raw-source"]').first().click();await page.locator('.raw-view').waitFor();
  for(const width of [320,390,639,640,768,1024,1366]){
   await page.setViewportSize({width,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'expanded metadata overflow at '+width);
  }
  await page.setViewportSize({width:390,height:844});await artifact('knowledge-expanded-mobile');
  if(process.env.KNOWL_BROWSER_ARTIFACT_DIR)await page.locator('.source-panel').screenshot({path:require('node:path').join(process.env.KNOWL_BROWSER_ARTIFACT_DIR,'task-012-source-panel-expanded-mobile.png')});
  await page.locator('[data-close-sources]').click();assert.equal(await sourcesToggle.getAttribute('aria-expanded'),'false');
  assert.equal(await sourcesToggle.evaluate(e=>e===document.activeElement),true);
  const returned=await sourcesToggle.evaluate(e=>({top:e.getBoundingClientRect().top,bottom:e.getBoundingClientRect().bottom,header:document.querySelector('.app-header').getBoundingClientRect().bottom,height:innerHeight}));
  assert.ok(returned.top>=returned.header && returned.bottom<=returned.height,'Page sources close returns to visible opener below the pinned header');
  await page.locator('[data-toggle-catalog]').click();assert.equal(await page.locator('.catalog-panel').isVisible(),true);
  assert.equal(await page.locator('[data-toggle-catalog]').getAttribute('aria-expanded'),'true');
  await page.locator('[data-toggle-catalog]').focus();
  await page.setViewportSize({width:768,height:844});
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  assert.equal(await page.locator('[data-toggle-catalog]').isVisible(),false);
  assert.equal(await page.locator('#screen').evaluate(e=>e===document.activeElement),true,'hidden catalog opener returns focus to persistent screen');
  assert.equal(await page.locator('[data-toggle-catalog]').getAttribute('aria-expanded'),'true');
  await page.setViewportSize({width:390,height:844});
  await page.evaluate(()=>document.querySelector('[data-screen="search"]').click());await page.locator('#search-form').waitFor();
  assert.equal(await page.locator('#query').inputValue(),'');assert.equal(await page.locator('.evidence-card').count(),0);
  const stored=await page.evaluate(()=>({local:{...localStorage},session:{...sessionStorage},cookies:document.cookie}));
  assert.deepEqual(stored.local,{});assert.equal(stored.cookies,'');
  for(const value of Object.values(stored.session))for(const secret of ['browser-secret-token','typed but not submitted','lexical','hybrid','degraded','unreported'])assert.equal(value.includes(secret),false);
  assert.deepEqual(violations,[]);
  assert.equal(requests.filter(u=>new URL(u).searchParams.has('query')).length,4);
  await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge?parent_id=catalogs%2Flong%2Findex');
  await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();
  await page.locator('[aria-label="Breadcrumb"] [aria-current="page"]').waitFor();
  const breadcrumb=await page.locator('[aria-label="Breadcrumb"]').evaluate(n=>{const box=n.getBoundingClientRect();return {left:box.left,right:box.right,width:document.documentElement.scrollWidth,viewport:innerWidth};});
  assert.ok(breadcrumb.left>=0 && breadcrumb.right<=breadcrumb.viewport && breadcrumb.width<=breadcrumb.viewport,'long breadcrumb must remain inside mobile viewport: '+JSON.stringify(breadcrumb));
  console.log('Knowledge latest-selection races (catalog and stale continuation), raw navigation, mobile provenance, explicit search, ordered evidence/JSON/download, disclosure, CSP, and no stored queries passed.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
