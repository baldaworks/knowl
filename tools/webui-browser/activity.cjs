'use strict';
const assert=require('node:assert/strict');
const {readable,bounded}=require('./assertions.cjs');
const fs=require('node:fs');const path=require('node:path');const os=require('node:os');
const artifacts=process.env.KNOWL_BROWSER_ARTIFACTS||path.join(os.tmpdir(),'knowl-webui-activity');fs.mkdirSync(artifacts,{recursive:true});
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
async function visibleDetail(page,id) {
 const state=await page.locator('#'+id).evaluate(e=>{const start=e.querySelector('.raw-heading strong,h2,.empty-state') || e;const range=document.createRange();range.selectNodeContents(start);let r=range.getBoundingClientRect();const error=e.querySelector('[data-error-code] p');if(error){range.selectNodeContents(error);const message=range.getBoundingClientRect();r={top:Math.min(r.top,message.top),bottom:Math.max(r.bottom,message.bottom)};}return {top:r.top,bottom:r.bottom,height:innerHeight,headerBottom:Math.max(0,document.querySelector('.app-header').getBoundingClientRect().bottom),focused:document.activeElement===e};});
 assert.ok(state.top>=state.headerBottom && state.bottom<=state.height,'detail heading outside viewport: '+JSON.stringify(state));
 assert.equal(state.focused,true,'explicit detail read must focus '+id);
}
async function sourceCardSelection(page) {
 const observations=[];
 for(const viewport of [{width:1440,height:1000},{width:1024,height:900},{width:390,height:844},{width:320,height:844}]) {
  await page.setViewportSize(viewport);
  const controls=page.locator('.source-select');
  for(const control of [controls.first(),controls.last()]) {
   const id=(await control.textContent()).trim();
   const card=control.locator('xpath=ancestor::section[contains(@class,"source-summary")]');
   assert.equal(await card.getByRole('button').count(),1,'one native source action per card');
   assert.equal(await card.locator('a,input,select,textarea,[tabindex]').count(),0,'no second interactive source action');
   for(const area of ['blank','status','metadata','title']) {
    await card.scrollIntoViewIfNeeded();
    const point=await card.evaluate((e,area)=>{
     const box=e.getBoundingClientRect(),button=e.querySelector('.source-select').getBoundingClientRect();
     const target=area==='status'?e.querySelector('.status-badge'):area==='metadata'?e.querySelector('.adapter-type'):e.querySelector('.source-select');
     const r=target.getBoundingClientRect();
     const p=area==='blank'?{x:box.x+box.width/2,y:box.bottom-8}:{x:r.x+r.width/2,y:r.y+r.height/2};
     return {...p,inside:p.x>box.left&&p.x<box.right&&p.y>box.top&&p.y<box.bottom,outsideTitle:!(p.x>=button.left&&p.x<=button.right&&p.y>=button.top&&p.y<=button.bottom)};
    },area);
    assert.equal(point.inside,true,'source click must be inside card');
    if(area!=='title')assert.equal(point.outsideTitle,true,'regression click must not hit title');
    const response=page.waitForResponse(r=>{const u=new URL(r.url());return u.pathname==='/ui/fragments/source'&&u.searchParams.get('source_id')===id;},{timeout:2000});
    await page.mouse.click(point.x,point.y);const result=await response;
    assert.equal(result.status(),200);await page.locator('.source-documents .card-title').filter({hasText:new RegExp('^'+id+'$')}).waitFor();
    await page.clock.runFor(32);await visibleDetail(page,'source-detail');
    assert.equal(await page.locator('.source-select[aria-current="true"]').count(),1);
    assert.equal(await control.getAttribute('aria-current'),'true');
    observations.push({viewport,id,area,point,status:result.status()});
   }
  }
  const first=controls.first();await first.scrollIntoViewIfNeeded();
  await page.mouse.move(0,0);
  const resting=await first.evaluate(e=>getComputedStyle(e,'::after').backgroundColor);
  await first.hover();
  assert.notEqual(await first.evaluate(e=>getComputedStyle(e,'::after').backgroundColor),resting,'whole-card hover must be visible');
  await page.screenshot({path:path.join(artifacts,'source-card-hover-'+viewport.width+'.png')});
  await page.mouse.move(0,0);
  for(const index of [0,await controls.count()-1]) {
   const control=controls.nth(index),id=(await control.textContent()).trim();
   await page.getByRole('button',{name:'Refresh',exact:true}).focus();
   for(let step=0;step<=index;step++)await page.keyboard.press('Tab');
   assert.equal(await control.evaluate(e=>document.activeElement===e),true,'Tab reaches each native card control');
   const focus=await control.evaluate(e=>{const s=getComputedStyle(e,'::after'),card=e.closest('.source-summary'),r=card.getBoundingClientRect(),c=getComputedStyle(card);return {visible:e.matches(':focus-visible'),outline:s.outlineStyle,outlineWidth:parseFloat(s.outlineWidth),width:parseFloat(s.width),height:parseFloat(s.height),cardWidth:r.width-parseFloat(c.borderLeftWidth)-parseFloat(c.borderRightWidth),cardHeight:r.height-parseFloat(c.borderTopWidth)-parseFloat(c.borderBottomWidth)};});
   assert.equal(focus.visible,true);assert.notEqual(focus.outline,'none');assert.ok(focus.outlineWidth>=2);
   assert.ok(Math.abs(focus.width-focus.cardWidth)<.05&&Math.abs(focus.height-focus.cardHeight)<.05,'focus indicator covers whole card: '+JSON.stringify(focus));
   await page.screenshot({path:path.join(artifacts,'source-card-focus-'+id+'-'+viewport.width+'.png')});
   for(const key of ['Enter','Space']) {
    await page.getByRole('button',{name:'Refresh',exact:true}).focus();
    for(let step=0;step<=index;step++)await page.keyboard.press('Tab');
    const response=page.waitForResponse(r=>{const u=new URL(r.url());return u.pathname==='/ui/fragments/source'&&u.searchParams.get('source_id')===id;});
    await page.keyboard.press(key);assert.equal((await response).status(),200);
    await page.locator('.source-documents .card-title').filter({hasText:new RegExp('^'+id+'$')}).waitFor();await page.clock.runFor(32);
    assert.equal(await control.getAttribute('aria-current'),'true');assert.equal(await page.locator('.source-select[aria-current="true"]').count(),1);
    const selected=await control.evaluate(e=>({background:getComputedStyle(e,'::after').backgroundColor,shadow:getComputedStyle(e,'::after').boxShadow}));
    assert.notEqual(selected.shadow,'none','whole-card selected state must be visible');
    observations.push({viewport,id,key,focus,selected});
   }
  }
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  await controls.last().locator('xpath=ancestor::section[contains(@class,"source-summary")]').scrollIntoViewIfNeeded();
  const selectedCard=await controls.last().evaluate(e=>{const r=e.closest('.source-summary').getBoundingClientRect();return {top:r.top,bottom:r.bottom,height:innerHeight};});
  assert.ok(selectedCard.top>=0&&selectedCard.bottom<=selectedCard.height,'selected card must be visible in screenshot: '+JSON.stringify(selectedCard));
  await page.screenshot({path:path.join(artifacts,'source-card-selected-'+viewport.width+'.png')});
 }
 fs.writeFileSync(path.join(artifacts,'source-card-actions.json'),JSON.stringify(observations,null,2));
}
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
  try{if(status!==200)await route.fulfill({status,headers:{'Content-Type':'text/html','X-Knowl-Fragment':'1','X-Knowl-Error':status===401?'unauthorized':'workspace_unavailable'},body:'<div data-error-code="workspace_unavailable">Unavailable</div>'});else await route.fulfill({response});}finally{active--;}
 });
 const settle=()=>page.evaluate(()=>Promise.resolve());
 const done=()=>page.waitForFunction(()=>document.querySelector('.operation-detail')&&!document.querySelector('.htmx-request'));
 const pollView=()=>page.evaluate(()=>({scroll:scrollY,focused:document.activeElement.id,top:document.getElementById('operation-detail').getBoundingClientRect().top}));
 const refresh=async delay=>{const position=await pollView();const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/ui/fragments/operation');await page.clock.runFor(delay);await response;await done();assert.deepEqual(await pollView(),position,'background poll must not move reading position or focus');};
 const select=async()=>{await page.locator('.operation-select').first().click();await done();await settle();};
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/operations');await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('.operation-select').first().waitFor();
 assert.ok((await page.locator('.operation-select').nth(2).innerText()).includes('first'),'first opaque ID must remain visible');
 assert.ok((await page.locator('.operation-select').nth(3).innerText()).includes('second'),'second opaque ID must remain visible');
 assert.equal(await page.evaluate(()=>getComputedStyle(document.scrollingElement).scrollBehavior),'auto','ordinary focus scrolling must finish before explicit detail reveal');
 await page.clock.runFor(10000);assert.equal(requests,0,'list rows must not poll');
 const initialDetailHeld=new Promise(resolve=>{hold=resolve;});
 await page.setViewportSize({width:1024,height:844});
 await page.locator('.operation-select').first().click();await initialDetailHeld;
 await visibleDetail(page,'operation-detail');
 assert.equal(await page.locator('#operation-detail').getAttribute('aria-busy'),'true');
 assert.equal(await page.locator('.operation-select').first().getAttribute('aria-current'),'true');
 assert.equal(await page.locator('.operation-detail').count(),0);release();await done();await settle();assert.equal(requests,1);
 await visibleDetail(page,'operation-detail');
 await page.setViewportSize({width:1366,height:900});
 await readable(page,'.diagnostic-row>span:last-child');
 await readable(page,'.subtle-note,.table-subtitle,.table th,.status-badge,.execution-facts span,.diagnostic-note,.section-label,.plan-facts dt');
 await page.clock.runFor(1999);assert.equal(requests,1);await refresh(1);assert.equal(requests,2,'selected active operation polls at 2 seconds');
 // Hold a real handler response: elapsed intervals must not start overlapping reads.
 let held=new Promise(resolve=>{hold=resolve});const heldView=await pollView();await page.clock.runFor(2000);await held;assert.equal(await page.locator('.operation-detail').count(),1,'background polling retains saved facts while loading');assert.deepEqual(await pollView(),heldView);const before=requests;await page.clock.runFor(20000);assert.equal(requests,before);release();await done();await settle();assert.equal(maxActive,1);assert.deepEqual(await pollView(),heldView);
 // Transient errors retain selected facts and double delay, capped at 30 seconds.
 status=503;
 for(const delay of [2000,4000,8000,16000,30000,30000]){const prior=requests;await page.clock.runFor(delay-1);assert.equal(requests,prior);await refresh(1);await settle();assert.equal(requests,prior+1);assert.equal(await page.locator('.operation-detail').count(),1);}
 // Visibility cancellation and exactly one refresh on resumption.
 await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});let count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 status=200;await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});await refresh(1);await settle();assert.equal(requests,count+1);
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=completed');await refresh(2000);await settle();assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-status'),'completed');count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 // Both public terminal states stop, while queued work remains eligible.
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=failed');await select();count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 for(const width of [320,390,640,1024,1366]) {
  await page.setViewportSize({width,height:844});
  if(width===1366){await page.mouse.wheel(0,-10000);await page.clock.runFor(160);await page.waitForFunction(()=>scrollY===0,null,{timeout:5000});}
  await page.locator('.operation-select').first().scrollIntoViewIfNeeded();
  const openerScroll=await page.evaluate(()=>scrollY);
  const position=await page.evaluate(()=>({scroll:scrollY,header:Math.max(0,document.querySelector('.app-header').getBoundingClientRect().bottom),top:document.querySelector('.operation-detail h2').getBoundingClientRect().top,bottom:document.querySelector('.operation-detail h2').getBoundingClientRect().bottom}));
  if(width===1366)assert.ok(position.top>=position.header && position.bottom<=844,'desktop side-by-side heading starts visible');
  held=new Promise(resolve=>{hold=resolve});await page.locator('.operation-select').first().click();await held;
  assert.equal(await page.locator('#operation-detail').getAttribute('aria-busy'),'true');await visibleDetail(page,'operation-detail');
  if(width===1366)assert.equal(await page.evaluate(()=>scrollY),position.scroll,'visible desktop loading does not scroll');
  release();await done();await visibleDetail(page,'operation-detail');
  if(width===1366)assert.equal(await page.evaluate(()=>scrollY),position.scroll,'visible desktop result does not scroll');
  if([390,640,1024].includes(width)) {
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'operation ID created horizontal overflow');
   await page.screenshot({path:path.join(artifacts,'task-021-operation-'+width+'.png'),fullPage:true});
  }
  await page.getByRole('button',{name:'Back to operations'}).click();
  assert.equal(await page.evaluate(()=>document.activeElement===document.querySelector('.operation-select')),true,'return focuses the actual selected row');
  assert.equal(await page.evaluate(()=>scrollY),openerScroll,'return restores the row reading position');
 }
 await page.locator('select[name="status"]').selectOption('applying');await page.locator('input[name="source_id"]').fill('docs');
 await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname==='/ui/fragments/operations'),page.getByRole('button',{name:'Filter',exact:true}).click()]);
 await page.waitForFunction(()=>new URL(document.querySelector('.operation-history [hx-get]').getAttribute('hx-get'),location.href).searchParams.get('status')==='applying',null,{timeout:5000});
 await page.clock.runFor(20);
 await page.getByRole('button',{name:'Next operations',exact:true}).click();await page.getByRole('button',{name:'Next operations',exact:true}).waitFor({state:'detached'});await page.clock.runFor(20);
 const refreshRequest=page.waitForRequest(r=>new URL(r.url()).pathname==='/ui/fragments/operations');
 await page.getByRole('button',{name:'Refresh',exact:true}).click();const refreshURL=new URL((await refreshRequest).url());
 assert.deepEqual(Object.fromEntries(refreshURL.searchParams),{limit:'10',source_id:'docs',status:'applying'},'Refresh retains filters and limit, clears only cursor');
 await page.getByRole('button',{name:'Next operations',exact:true}).waitFor();await page.clock.runFor(20);
 assert.equal(await page.locator('select[name="status"]').inputValue(),'applying');assert.equal(await page.locator('input[name="source_id"]').inputValue(),'docs');
 await page.locator('.operation-select').last().click();await page.locator('#operation-detail [data-error-code="operation_not_found"]').waitFor();await page.clock.runFor(20);await visibleDetail(page,'operation-detail');
 await page.route(url=>url.pathname==='/ui/fragments/operation',async route=>{await route.fetch();await route.abort('failed');},{times:1});
 await page.locator('.operation-select').first().click();await page.getByText('Request failed. Select again or refresh this view to retry.',{exact:true}).waitFor();await visibleDetail(page,'operation-detail');
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=queued');await select();count=requests;await refresh(2000);assert.equal(requests,count+1);
 // Hiding during an in-flight refresh aborts it and discards its late response.
 held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;
 await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});count=requests;release();await settle();await page.clock.runFor(60000);assert.equal(requests,count);
 await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});await refresh(1);assert.equal(requests,count+1);
 // Selection cancels an in-flight older read and ignores its eventual response.
 await page.request.get(process.env.KNOWL_BROWSER_URL+'/fixture/status?value=running');await select();held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;
 await page.locator('.operation-select').nth(1).click();await page.waitForFunction(()=>document.querySelector('[data-operation-id="op-new"]'));release();await settle();assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-id'),'op-new');
 // Returning during a background poll must retain focus after the late response.
 held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;
 await page.getByRole('button',{name:'Back to operations'}).click();
 const selectedOpener=page.locator('.operation-select').nth(1);
 assert.equal(await selectedOpener.evaluate(e=>document.activeElement===e),true);
 release();await done();await settle();assert.equal(await selectedOpener.evaluate(e=>document.activeElement===e),true,'poll stole focus after return');
 // Screen exit clears the polling lifecycle.
 await page.locator('[data-screen="sources"]').click();await page.locator('.source-select').first().waitFor();count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 await sourceCardSelection(page);
 for(const width of [320,390,640,1024,1366]) {
  await page.setViewportSize({width,height:844});
  const match=url=>url.pathname==='/ui/fragments/source'&&url.searchParams.get('source_id')==='docs';
  let ready,finish;const loading=new Promise(resolve=>{ready=resolve;});const gate=new Promise(resolve=>{finish=resolve;});
  await page.route(match,async route=>{const response=await route.fetch();ready();await gate;await route.fulfill({response});},{times:1});
  await page.locator('.source-select').first().focus();await page.keyboard.press('Enter');await loading;
  assert.equal(await page.locator('#source-detail').getAttribute('aria-busy'),'true');await visibleDetail(page,'source-detail');
  finish();await page.locator('.source-documents').waitFor();await page.waitForFunction(()=>!document.getElementById('source-detail').hasAttribute('aria-busy'),null,{timeout:5000});await page.clock.runFor(32);await visibleDetail(page,'source-detail');
  const firstDocument=await page.locator('.source-documents tbody .table-name').first().evaluate(e=>{const r=e.getBoundingClientRect();return {top:r.top,bottom:r.bottom,header:document.querySelector('.app-header').getBoundingClientRect().bottom,height:innerHeight};});
  assert.ok(firstDocument.top>=firstDocument.header && firstDocument.bottom<=firstDocument.height,'selected source must reveal its first saved document: '+JSON.stringify({width,...firstDocument}));
 }
 await readable(page,'.adapter-type,.source-card-facts dt,.source-card-facts dd,.processing-strip,.subtle-note');assert.equal(await page.locator('[data-fact="last-success"]').last().textContent(),'2026-01-02 00:00 UTC');
 await readable(page,'[data-open-operation].link-button');
 await page.getByRole('button',{name:'docs-7',exact:true}).click();await page.locator('#source-detail [data-error-code="source_not_found"]').waitFor();await page.clock.runFor(20);await visibleDetail(page,'source-detail');
 await page.route(url=>url.pathname==='/ui/fragments/source',async route=>{await route.fetch();await route.abort('failed');},{times:1});
 await page.locator('.source-select').first().click();await page.getByText('Request failed. Select again or refresh this view to retry.',{exact:true}).waitFor();await visibleDetail(page,'source-detail');
 // Hold a second real source response while old documents are removed.
 let detailReady,detailRelease;const detailHeld=new Promise(resolve=>{detailReady=resolve;});const detailGate=new Promise(resolve=>{detailRelease=resolve;});
 await page.route(url=>url.pathname==='/ui/fragments/source'&&url.searchParams.get('source_id')==='other-docs',async route=>{const response=await route.fetch();detailReady();await detailGate;await route.fulfill({response});},{times:1});
 await page.locator('.source-select').last().click();await bounded(detailHeld,'second source gate not reached');
 assert.equal(await page.locator('#source-detail').getAttribute('aria-busy'),'true');assert.equal(await page.locator('.source-documents').count(),0);
 await visibleDetail(page,'source-detail');
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
 await visibleDetail(page,'source-detail');
 assert.equal(await page.locator('.source-select').first().getAttribute('aria-current'),'true','document continuation retains selected source');
 await page.screenshot({path:path.join(artifacts,'task-009-fixture-tombstone-desktop.png'),fullPage:true});
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await page.screenshot({path:path.join(artifacts,'task-009-fixture-tombstone-mobile.png'),fullPage:true});
 await page.getByRole('button',{name:'View operation'}).click();await page.locator('.operation-detail').waitFor();await page.clock.runFor(20);assert.equal(await page.locator('.operation-detail').getAttribute('data-operation-id'),'op-2');await visibleDetail(page,'operation-detail');
 await page.screenshot({path:path.join(artifacts,'task-009-fixture-active-mobile.png'),fullPage:true});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 await page.setViewportSize({width:1366,height:900});await page.screenshot({path:path.join(artifacts,'task-009-fixture-active-desktop.png'),fullPage:true});
 status=401;await page.clock.runFor(2000);await page.locator('#connection-card').waitFor({state:'visible'});assert.equal(await page.locator('.operation-detail').count(),0);count=requests;await page.clock.runFor(60000);assert.equal(requests,count);
 status=200;await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('.operation-detail').waitFor();held=new Promise(resolve=>{hold=resolve});await page.clock.runFor(2000);await held;await page.locator('#disconnect').click();release();await settle();count=requests;await page.clock.runFor(60000);assert.equal(requests,count);assert.equal(await page.locator('.operation-detail').count(),0);
 // A direct/reloaded detail has no opener in this document; return lands at list heading.
 const direct=await browser.newPage({viewport:{width:390,height:844}});
 await direct.goto(process.env.KNOWL_BROWSER_URL+'/ui/operations?operation_id=op-1');
 await direct.locator('#operator-token').fill('browser-secret-token');await direct.locator('#connect-form button').click();
 await direct.locator('.operation-detail').waitFor();
 await direct.reload();await direct.locator('#operator-token').fill('browser-secret-token');await direct.locator('#connect-form button').click();await direct.locator('.operation-detail').waitFor();
 await direct.getByRole('button',{name:'Back to operations'}).click();
 assert.equal(await direct.locator('.operation-history h2').evaluate(e=>document.activeElement===e),true,'direct detail return must fall back to list heading');
 await direct.close();
 assert.deepEqual(violations,[]);console.log('Production fragments: selected-only nonoverlapping 2s polling, capped backoff, visibility, terminal, selection, screen exit, 401, logout, source continuation and mobile layout passed. Screenshots are labeled deterministic lifecycle fixtures.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
