'use strict';
const assert = require('node:assert/strict');
const {chromium} = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');
const {bounded,readable} = require('./assertions.cjs');

(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1440,height:1000}});
 page.setDefaultTimeout(5000);
 const errors=[],requests=[];
 page.on('pageerror',error=>errors.push(error.message));
 page.on('request',request=>requests.push(request.url()));
 await page.route('**/*',route=>route.continue());
 const node=id=>page.locator('#wiki-tree [data-page-node='+JSON.stringify('page:'+id)+']');
 const branch=id=>node(id).locator(':scope > [data-page-branch]');
 const root=page.locator('#wiki-tree > [data-page-branch]');
 const title=()=>page.locator('#article-title').textContent();
 const countReads=()=>requests.filter(value=>['/ui/fragments/knowledge','/ui/fragments/page'].includes(new URL(value).pathname)).length;
 const matchBranch=(directory,cursor=false)=>url=>url.pathname==='/ui/fragments/wiki-pages' && (url.searchParams.get('directory')||'')===directory && url.searchParams.has('cursor')===cursor;
 const fault=(status,code)=>({status,headers:{'Content-Type':'text/html','X-Knowl-Fragment':'1','X-Knowl-Error':code},body:'<div role="alert" data-error-code="'+code+'">Branch unavailable</div>'});
 async function hold(match,options) {
  let ready,release,done;
  const held=new Promise(resolve=>{ready=resolve;}),gate=new Promise(resolve=>{release=resolve;}),delivered=new Promise(resolve=>{done=resolve;});
  await page.route(match,async route=>{const response=await route.fetch();ready();await gate;try{await route.fulfill(options || {response});}catch{}finally{done();}},{times:1});
  return {held,release,delivered};
 }
 async function open(query='') {
  await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge'+query);
  assert.equal(await page.locator('#wiki-tree').textContent(),'','public shell has no protected tree');
  await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();
  await page.locator('#article-title').waitFor();await node('AI/index').waitFor();
 }
 async function waitIdle(target) {await target.evaluate(element=>new Promise(resolve=>{const check=()=>element.getAttribute('aria-busy')==='true'?requestAnimationFrame(check):resolve();check();}));}
 try {
  await open();
  assert.equal(await root.locator(':scope > [data-wiki-pages] > [data-page-node]').count(),50);
  const emptyGroup=page.locator('#wiki-tree [data-page-node="group:A-empty"]');
  assert.equal(await emptyGroup.locator(':scope > [data-wiki-group]').textContent(),'A-empty');
  assert.equal(await emptyGroup.locator(':scope > a,:scope > [data-page-toggle]').count(),0,'empty group has no invented action');
  await readable(page,'#wiki-tree [data-page-more]');
  const stale=page.waitForResponse(response=>new URL(response.url()).pathname==='/ui/fragments/wiki-pages' && response.status()===409);
  await page.route(matchBranch('',true),route=>route.fulfill(fault(409,'snapshot_changed')),{times:1});
  await root.locator('[data-page-more]').click();await stale;await waitIdle(root);
  await root.locator(':scope > [data-wiki-pages] > [data-page-node]').first().waitFor();
  assert.equal(await root.locator(':scope > [data-wiki-pages] > [data-page-node]').count(),50,'stale root continuation restarts only root');
  assert.equal(await title(),'Home');
  await page.route(matchBranch('ADRs'),route=>route.fulfill(fault(503,'capability_unavailable')),{times:1});
  await node('ADRs/index').locator(':scope > [data-page-toggle]').click();
  await branch('ADRs/index').locator('[data-error-code="capability_unavailable"]').waitFor();
  assert.equal(await title(),'Home','branch failure preserves article');
  await branch('ADRs/index').locator('[data-page-retry]').click();
  await branch('ADRs/index').locator('[data-page-empty]').waitFor();
  const delayed=await hold(matchBranch('AI'));
  await node('AI/index').locator(':scope > [data-page-toggle]').click();await bounded(delayed.held,'AI read did not start');
  await page.locator('#wiki-tree [data-wiki-page="index"]').click();await page.locator('#article-title').waitFor();
  await branch('AI/index').locator('[data-page-retry]').waitFor();
  await readable(page,'#wiki-tree [data-page-node="page:AI/index"] > [data-page-branch] [data-page-retry]');
  delayed.release();await bounded(delayed.delivered,'aborted branch did not finish');
  assert.equal(await branch('AI/index').locator('[data-page-node]').count(),0,'late aborted branch cannot restore descendants');
  await branch('AI/index').locator('[data-page-retry]').click();await node('AI/Prism').waitFor();
  const aiCaret=node('AI/index').locator(':scope > [data-page-toggle]');
  assert.equal(await aiCaret.getAttribute('aria-expanded'),'true');
  await node('AI/index').locator(':scope > a').click();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='AI');
  assert.equal((await page.locator('.document').textContent()).trim(),'Optional AI information.','parent title opens own optional body');
  await node('AI/Prism').locator(':scope > a').click();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='Prism');
  assert.equal(await node('AI/Prism').locator(':scope > a').getAttribute('aria-current'),'page');
  assert.equal(await node('AI/Prism').locator(':scope > a').evaluate(element=>element.classList.contains('active')),true,'selected page uses native active row');
  await page.locator('#tree-scroll').evaluate(element=>{element.scrollTop=100;});
  const scroll=await page.locator('#tree-scroll').evaluate(element=>element.scrollTop);
  await node('AI/OpenSpec').locator(':scope > a').click();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='OpenSpec');
  assert.equal(await page.locator('#tree-scroll').evaluate(element=>element.scrollTop),scroll,'article swap preserves independent tree scroll');
  assert.equal(await aiCaret.getAttribute('aria-expanded'),'true');
  assert.equal(await page.locator('#tree-scroll').evaluate(element=>element.scrollLeft===0 && element.scrollWidth<=element.clientWidth),true,'native page rows never shift sideways');
  const staleNested=await hold(matchBranch('AI',true),fault(409,'snapshot_changed'));
  await branch('AI/index').locator('[data-page-more]').click();await bounded(staleNested.held,'nested stale read did not start');
  await aiCaret.click();staleNested.release();await bounded(staleNested.delivered,'nested stale response did not finish');
  await page.waitForFunction(()=>{const branch=document.querySelector('#wiki-tree [data-page-node="page:AI/index"] > [data-page-branch]');return branch?.getAttribute('aria-busy')!=='true' && branch?.querySelectorAll('[data-page-node]').length===50;});
  assert.equal(await aiCaret.getAttribute('aria-expanded'),'false','stale restart preserves latest collapse');
  assert.equal(await branch('AI/index').isVisible(),false);
  await aiCaret.click();
  const moreHeld=await hold(matchBranch('AI',true));
  await branch('AI/index').locator('[data-page-more]').click();await bounded(moreHeld.held,'AI More did not start');
  await aiCaret.click();assert.equal(await branch('AI/index').isVisible(),false);
  moreHeld.release();await bounded(moreHeld.delivered,'AI More did not finish');await waitIdle(branch('AI/index'));
  assert.equal(await aiCaret.getAttribute('aria-expanded'),'false','late continuation preserves user collapse');
  assert.equal(await branch('AI/index').isVisible(),false);
  await aiCaret.click();assert.equal(await branch('AI/index').locator('[data-page-node]').count(),60,'More appends exactly one child window');
  assert.equal(await title(),'OpenSpec');
  const older=await hold(url=>url.pathname==='/ui/fragments/page' && url.searchParams.get('page_id')==='AI/Prism');
  await node('AI/Prism').locator(':scope > a').click();await bounded(older.held,'Prism read did not start');
  await node('AI/OpenSpec').locator(':scope > a').click();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='OpenSpec');
  older.release();await bounded(older.delivered,'old article did not finish');
  assert.equal(await title(),'OpenSpec','old article reply cannot overwrite newer selection');
  await page.locator('[data-screen="statistics"]').click();await page.locator('#screen [data-error-code]').waitFor();
  assert.equal(await page.locator('#wiki-navigation').isVisible(),false);
  await page.goBack();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='OpenSpec');
  assert.equal(await aiCaret.getAttribute('aria-expanded'),'true','other screens retain completed branch DOM');
  await page.goForward();await page.locator('#screen [data-error-code]').waitFor();
  await page.goBack();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='OpenSpec');
  // Native focus moves out of a desktop context that becomes a hidden drawer.
  await page.waitForFunction(()=>!document.getElementById('screen').classList.contains('htmx-settling'));
  await page.locator('#contents-tab').focus();await page.setViewportSize({width:1024,height:900});
  await page.waitForFunction(()=>document.activeElement===document.getElementById('context-toggle'));
  await page.setViewportSize({width:320,height:844});
  await page.locator('#context-toggle').click();await page.locator('#page-context.show').waitFor();
  const reads=countReads();
  await page.locator('#outline a').first().click();
  await page.waitForFunction(()=>document.activeElement?.matches('.document h2'));
  assert.equal(countReads(),reads,'Contents never reloads the page');
  assert.equal(await page.locator('.offcanvas-backdrop').count(),0);
  assert.equal(await page.locator('body').evaluate(element=>element.style.overflow),'');
  assert.equal(await page.evaluate(()=>{const heading=document.activeElement.getBoundingClientRect(),header=document.querySelector('.app-header').getBoundingClientRect();return heading.top>=header.bottom && heading.bottom<=innerHeight;}),true,'focused heading is visible below the real wrapped header');
  await page.locator('#context-toggle').click();await page.locator('#page-context.show').waitFor();
  await page.locator('#page-context .btn-close').click();await page.waitForFunction(()=>!document.querySelector('#page-context').classList.contains('show'));
  await page.locator('#navigation-toggle').click();
  assert.equal(await page.locator('.offcanvas-backdrop').count(),0,'drawers are exclusive');
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#navigation-toggle').evaluate(element=>element===document.activeElement),true);
  await page.locator('#context-toggle').click();await page.locator('#page-context.show').waitFor();await page.keyboard.press('Escape');
  await page.waitForFunction(()=>!document.querySelector('#page-context').classList.contains('show'));
  assert.equal(await page.locator('#context-toggle').evaluate(element=>element===document.activeElement),true);
  await page.locator('#navigation-toggle').click();
  await node('AI/Prism').locator(':scope > a').click();
  await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='Prism' && document.activeElement===document.getElementById('article-title'));
  assert.equal(await page.locator('.app-main').evaluate(element=>element.inert),false,'mobile page selection releases navigation inert state');
  await page.setViewportSize({width:1440,height:1000});
  await node('AI/OpenSpec').locator(':scope > a').click();await page.waitForFunction(()=>document.getElementById('article-title')?.textContent==='OpenSpec');
  // Independent pending branches reserve their complete maximum windows.
  const heldGroups=[];
  for(let i=0;i<8;i++) {const name='B'+String(i).padStart(2,'0');const held=await hold(matchBranch(name));heldGroups.push(held);await node(name+'/index').locator(':scope > [data-page-toggle]').click();await bounded(held.held,'concurrent branch did not start');}
  const beforeCapacity=requests.filter(value=>new URL(value).pathname==='/ui/fragments/wiki-pages').length;
  await node('B08/index').locator(':scope > [data-page-toggle]').click();
  assert.equal(requests.filter(value=>new URL(value).pathname==='/ui/fragments/wiki-pages').length,beforeCapacity,'reserved windows block requests above1000nodes');
  assert.equal(await page.locator('#wiki-tree-status').isVisible(),true);
  for(const held of heldGroups)held.release();for(const held of heldGroups)await bounded(held.delivered,'concurrent branch did not finish');
  for(let i=0;i<8;i++)await waitIdle(branch('B'+String(i).padStart(2,'0')+'/index'));
  await node('B08/index').locator(':scope > [data-page-toggle]').click();await branch('B08/index').locator('[data-page-empty]').waitFor();
  assert.equal(await page.locator('#wiki-tree-status').isVisible(),false,'freed reservations clear capacity status');
  // Reconnect resets capacity while keeping URL-selected direct navigation.
  await page.locator('#disconnect').click();
  assert.equal(await page.locator('#wiki-tree').textContent(),'');
  await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();
  await node('AI/OpenSpec').waitFor();
  assert.equal(await node('AI/OpenSpec').locator(':scope > a').getAttribute('aria-current'),'page','direct page reveals only verified real ancestor branches');
  // Fill the bounded root with real cursor windows; capacity stays local.
  for(let i=0;i<25;i++) {
   if(await page.locator('#wiki-tree-status').isVisible())break;
   const more=root.locator(':scope > [data-wiki-pages] > [data-page-continuation] > [data-page-more]');
   if(!await more.count())break;
   await more.click();await waitIdle(root);
  }
  assert.equal(await page.locator('#wiki-tree-status').isVisible(),true);
  assert.equal(await page.locator('#wiki-tree [data-wiki-group]').count(),1,'groups remain present within the shared bounded capacity');
  assert.ok(await page.locator('#wiki-tree [data-page-node]').count()<=1000);
  assert.equal(await page.locator('#wiki-tree [data-page-node]').evaluateAll(nodes=>new Set(nodes.map(node=>node.dataset.pageNode)).size===nodes.length),true,'continued page hierarchy has no duplicate nodes');
  assert.equal(await title(),'OpenSpec','capacity never replaces current page');
  await page.route(matchBranch('ADRs'),route=>route.fulfill({status:401,headers:{'Content-Type':'text/html'},body:'Unauthorized'}),{times:1});
  // Capacity intentionally prevents another branch; reconnect frees it first.
  await page.locator('#disconnect').click();await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await node('ADRs/index').waitFor();
  await node('ADRs/index').locator(':scope > [data-page-toggle]').click();await page.locator('#operator-token').waitFor({state:'visible'});
  assert.equal(await page.locator('#wiki-tree').textContent(),'','401 clears protected tree/session');
  assert.equal(await page.locator('#screen').textContent(),'');
  assert.deepEqual(await page.evaluate(()=>({local:{...localStorage},session:{...sessionStorage},cookies:document.cookie})),{local:{},session:{'htmx-current-path-for-history':'/ui/knowledge'},cookies:''});
  assert.deepEqual(errors,[]);
  console.log('PASS: persistent page tree, scoped retry/stale/abort/More, independent reservations/capacity, direct/history selection, native drawer/focus/Contents and401 cleanup');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
