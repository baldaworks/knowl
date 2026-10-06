'use strict';
const assert=require('node:assert/strict');
const {readable,bounded}=require('./assertions.cjs');
const fs=require('node:fs');const path=require('node:path');const os=require('node:os');
const artifacts=process.env.KNOWL_BROWSER_ARTIFACTS||path.join(os.tmpdir(),'knowl-webui-activity');fs.mkdirSync(artifacts,{recursive:true});
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 try {
 const page=await browser.newPage({viewport:{width:1366,height:900}});
 const violations=[];await page.exposeFunction('recordCSP',v=>violations.push(v));await page.addInitScript(()=>document.addEventListener('securitypolicyviolation',e=>window.recordCSP(e.violatedDirective)));
 await page.clock.install({time:new Date('2026-01-04T00:00:00Z')});await page.clock.pauseAt(new Date('2026-01-04T00:00:00Z'));
 let requests=0,active=0,maxActive=0,status=200,hold,release;
 await page.route('**/ui/fragments/operation?*',async route=>{
  requests++;active++;maxActive=Math.max(maxActive,active);
  const response=await route.fetch();
  if(hold){const ready=hold;hold=null;await new Promise(resolve=>{release=resolve;ready();});}
  try{if(status!==200)await route.fulfill({status,headers:{'Content-Type':'text/html','X-Knowl-Error':status===401?'unauthorized':'workspace_unavailable'},body:'<div data-error-code="workspace_unavailable">Unavailable</div>'});else await route.fulfill({response});}finally{active--;}
 });
 const settle=()=>page.evaluate(()=>Promise.resolve());
 const done=()=>page.waitForFunction(()=>document.querySelector('.operation-detail')&&!document.querySelector('.htmx-request'));
 const refresh=async delay=>{const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/ui/fragments/operation');await page.clock.runFor(delay);await response;await done();};
 const select=async()=>{await page.locator('.operation-select').first().click();await done();await settle();};
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/operations');await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('.operation-select').first().waitFor();
 await page.clock.runFor(10000);assert.equal(requests,0,'list rows must not poll');
 const initialDetailHeld=new Promise(resolve=>{hold=resolve;});
 await page.locator('.operation-select').first().click();await initialDetailHeld;
 assert.equal(await page.locator('#operation-detail').getAttribute('aria-busy'),'true');
 assert.equal(await page.locator('.operation-select').first().getAttribute('aria-current'),'true');
 assert.equal(await page.locator('.operation-detail').count(),0);release();await done();await settle();assert.equal(requests,1);
 await readable(page,'.subtle-note,.table-subtitle,.table th,.status-badge,.execution-facts span,.diagnostic-note,.section-label,.plan-facts dt');
 await page.clock.runFor(1999);assert.equal(requests,1);await refresh(1);assert.equal(requests,2,'selected active operation polls at 2 seconds');
 // Hold a real handler response: elapsed intervals must not start overlapping reads.
 let held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;const before=requests;await page.clock.runFor(20000);assert.equal(requests,before);release();await done();await settle();assert.equal(maxActive,1);
 // Transient errors retain selected facts and double delay, capped at 30 seconds.
 status=503;
 for(const delay of [2000,4000,8000,16000,30000,30000]){const prior=requests;await page.clock.runFor(delay-1);assert.equal(requests,prior);await refresh(1);await settle();assert.equal(requests,prior+1);assert.equal(await page.locator('.operation-detail').count(),1);}
 // Visibility cancellation and exactly one refresh on resumption.
 await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});let count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 status=200;await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});await refresh(1);await settle();assert.equal(requests,count+1);
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=completed');await refresh(2000);await settle();assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-status'),'completed');count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 // Both public terminal states stop, while queued work remains eligible.
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=failed');await select();count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=queued');await select();count=requests;await refresh(2000);assert.equal(requests,count+1);
 // Hiding during an in-flight refresh aborts it and discards its late response.
 held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;
 await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});count=requests;release();await settle();await page.clock.runFor(60000);assert.equal(requests,count);
 await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});await refresh(1);assert.equal(requests,count+1);
 // Selection cancels an in-flight older read and ignores its eventual response.
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=running');await select();held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;
 await page.locator('.operation-select').nth(1).click();await page.waitForFunction(()=>document.querySelector('[data-operation-id="op-new"]'));release();await settle();assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-id'),'op-new');
 // Screen exit clears the polling lifecycle.
 await page.locator('[data-screen="sources"]').click();await page.locator('.source-select').first().waitFor();count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 await page.locator('.source-select').first().click();await page.locator('.source-documents').waitFor();await page.clock.runFor(20);await readable(page,'.adapter-type,.source-card-facts dt,.source-card-facts dd,.processing-strip,.source-flow span,.source-flow strong,.subtle-note');assert.equal(await page.locator('[data-fact="last-success"]').last().textContent(),'2026-01-02 00:00 UTC');
 // Hold a second real source response while old documents are removed.
 let detailReady,detailRelease;const detailHeld=new Promise(resolve=>{detailReady=resolve;});const detailGate=new Promise(resolve=>{detailRelease=resolve;});
 await page.route(url=>url.pathname==='/ui/fragments/source'&&url.searchParams.get('source_id')==='other-docs',async route=>{const response=await route.fetch();detailReady();await detailGate;await route.fulfill({response});},{times:1});
 await page.locator('.source-select').last().click();await bounded(detailHeld,'second source gate not reached');
 assert.equal(await page.locator('#source-detail').getAttribute('aria-busy'),'true');assert.equal(await page.locator('.source-documents').count(),0);
 assert.equal(await page.locator('.source-select').last().getAttribute('aria-current'),'true');
 detailRelease();await page.locator('.source-documents .card-title').filter({hasText:'other-docs'}).waitFor();
 await page.locator('.source-select').first().click();await page.locator('.source-documents .card-title').filter({hasText:/^docs$/}).waitFor();await page.clock.runFor(20);
 // An older document continuation cannot overwrite a newly selected source.
 const oldSource=url=>url.pathname==='/ui/fragments/source'&&url.searchParams.has('cursor');
 let sourceReady,sourceRelease,sourceDelivered;
 const sourceHeld=new Promise(resolve=>{sourceReady=resolve;});const sourceGate=new Promise(resolve=>{sourceRelease=resolve;});const sourceDelivery=new Promise(resolve=>{sourceDelivered=resolve;});
 const sourceFinished=new Promise(resolve=>{const finish=request=>{if(!oldSource(new URL(request.url())))return;page.off('requestfinished',finish);page.off('requestfailed',finish);resolve();};page.on('requestfinished',finish);page.on('requestfailed',finish);});
 await page.route(oldSource,async route=>{const response=await route.fetch();sourceReady();await sourceGate;try{await route.fulfill({response});}finally{sourceDelivered();}});
 await page.getByRole('button',{name:'Next documents'}).click();await bounded(sourceHeld,'continuation gate not reached');
 await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname==='/ui/fragments/source'&&!new URL(r.url()).searchParams.has('cursor')),page.locator('.source-select').first().click()]);
 sourceRelease();await sourceDelivery;await sourceFinished;await page.clock.runFor(32);await settle();
 assert.equal(await page.locator('[data-fact="upstream-state"]').textContent(),'Present','late document continuation replaced the selected source');
 await page.unroute(oldSource);
 // First-page omission never becomes a tombstone; second page has an explicit saved deletion.
 assert.equal(await page.locator('[data-fact="upstream-state"]').textContent(),'Present');
 await page.getByRole('button',{name:'Next documents'}).click();await page.waitForFunction(()=>document.querySelector('[data-fact="upstream-state"]')?.textContent==='Confirmed deletion');await page.clock.runFor(20);
 assert.equal(await page.locator('.source-select').first().getAttribute('aria-current'),'true','document continuation retains selected source');
 await page.screenshot({path:path.join(artifacts,'task-009-fixture-tombstone-desktop.png'),fullPage:true});
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await page.screenshot({path:path.join(artifacts,'task-009-fixture-tombstone-mobile.png'),fullPage:true});
 await page.getByRole('button',{name:'View operation'}).click();await page.locator('.operation-detail').waitFor();assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-id'),'op-2');
 await page.screenshot({path:path.join(artifacts,'task-009-fixture-active-mobile.png'),fullPage:true});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 await page.setViewportSize({width:1366,height:900});await page.screenshot({path:path.join(artifacts,'task-009-fixture-active-desktop.png'),fullPage:true});
 status=401;await page.clock.runFor(2000);await page.locator('#connection-card').waitFor({state:'visible'});assert.equal(await page.locator('.operation-detail').count(),0);count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 status=200;await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('.operation-detail').waitFor();held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;await page.locator('#disconnect').click();release();await settle();count=requests;await page.clock.runFor(60000);assert.equal(requests,count);assert.equal(await page.locator('.operation-detail').count(),0);
 assert.deepEqual(violations,[]);console.log('Production fragments: selected-only nonoverlapping 2s polling, capped backoff, visibility, terminal, selection, screen exit, 401, logout, source continuation and mobile layout passed. Screenshots are labeled deterministic lifecycle fixtures.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
