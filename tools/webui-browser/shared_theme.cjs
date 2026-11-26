'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');const path=require('node:path');const os=require('node:os');
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
const artifacts=process.env.KNOWL_BROWSER_ARTIFACTS||path.join(os.tmpdir(),'knowl-shared-theme');fs.mkdirSync(artifacts,{recursive:true});
const screens={knowledge:'#article-body',search:'#search-form',operations:'.operation-history',sources:'.source-summary',statistics:'[data-statistic="pages"]'};
const settle=page=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
async function chrome(page){return page.evaluate(()=>{
 const fields=['fontFamily','fontSize','fontWeight','color','backgroundColor','borderRadius','paddingTop','paddingLeft'];
 const style=selector=>{const e=document.querySelector(selector),s=getComputedStyle(e);return Object.fromEntries(fields.map(k=>[k,s[k]]));};
 return {body:style('body'),sidebar:style('.app-sidebar'),brand:style('.brand-text'),header:style('.app-header'),nav:style('[data-screen].active p'),button:style('#disconnect'),card:style(document.body.dataset.connectionState==='connected'?'#screen .card':'#connection-card'),width:getComputedStyle(document.querySelector('.app-sidebar')).width,headerHeight:document.querySelector('.app-header').getBoundingClientRect().height};
 });}
(async()=>{const browser=await chromium.launch({headless:true});try{
for(const width of [1440,1024,390,320]){
 const page=await browser.newPage({viewport:{width,height:width===1440?1000:width===1024?900:844},reducedMotion:'reduce'});const violations=[];page.on('console',m=>{if(m.type()==='error')violations.push(m.text());});
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge');await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator(screens.knowledge).waitFor();await settle(page);const baseline=await chrome(page);
 // Compare against the same bundled vendor styles in a minimal native shell.
 // Keep production UI CSS enabled so its CSP-safe close/select replacements stay active.
 const stock=await page.evaluate(async()=>{
  const frame=document.createElement('iframe');frame.hidden=true;document.body.append(frame);
  try {
   const doc=frame.contentDocument;doc.documentElement.setAttribute('data-bs-theme','light');
   await Promise.all(['bootstrap.min.css','adminlte.min.css'].map(name=>new Promise((resolve,reject)=>{const link=doc.createElement('link');link.rel='stylesheet';link.href=new URL('/ui/assets/vendor/'+name,location.href).href;link.onload=resolve;link.onerror=reject;doc.head.append(link);})));
   doc.body.className='layout-fixed sidebar-expand-lg bg-body-tertiary';
   doc.body.innerHTML='<div class="app-wrapper"><nav class="app-header navbar navbar-expand bg-body"><div class="container-fluid"><button class="btn btn-outline-secondary btn-sm" id="native-button">Disconnect</button></div></nav><aside class="app-sidebar bg-dark" data-bs-theme="dark"><div class="sidebar-brand"><span class="brand-text">Knowl</span></div><div class="sidebar-wrapper"><nav><ul class="nav sidebar-menu flex-column"><li class="nav-item"><a class="nav-link active"><p>Knowledge</p></a></li></ul></nav></div></aside><main class="app-main"><section class="card"></section></main></div>';
   const fields=['fontFamily','fontSize','fontWeight','color','backgroundColor','borderRadius'];
   const style=selector=>{const computed=frame.contentWindow.getComputedStyle(doc.querySelector(selector));return Object.fromEntries(fields.map(k=>[k,computed[k]]));};
   return {body:style('body'),sidebar:style('.app-sidebar'),brand:style('.brand-text'),header:style('.app-header'),nav:style('.nav-link p'),button:style('#native-button'),card:style('.card')};
  } finally {frame.remove();}
 });
 for(const [key,value] of Object.entries(stock)) for(const [property,expected] of Object.entries(value)) assert.equal(baseline[key][property],expected,'stock vendor '+key+'.'+property+' at '+width);
 for(const [screen,selector] of Object.entries(screens)){
  if(width<992){await page.locator('#navigation-toggle').click();await page.waitForFunction(()=>document.body.classList.contains('sidebar-open'));}
  await page.locator('[data-screen="'+screen+'"]').click();await page.locator(selector).waitFor();await settle(page);
  assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0,'redundant All pages control must be absent');
  const current=await chrome(page);
  for(const key of ['body','sidebar','brand','header','nav','button','width','headerHeight']) assert.deepEqual(current[key],baseline[key],'native '+key+' must remain identical on '+screen+' at '+width);
  for(const [property,expected] of Object.entries(stock.card)) assert.equal(current.card[property],expected,'stock card '+property+' on '+screen+' at '+width);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,screen+' horizontal overflow at '+width);
  if(screen==='search'){await page.locator('#query').fill('knowledge');await page.locator('#search-form button').click();await page.locator('.evidence-card').waitFor();}
  await page.screenshot({path:path.join(artifacts,screen+'-'+width+'.png'),fullPage:true});
  if(width<992){await page.locator('#navigation-toggle').click();await page.waitForFunction(()=>document.body.classList.contains('sidebar-open'));await settle(page);await page.screenshot({path:path.join(artifacts,screen+'-navigation-'+width+'.png')});await page.locator('#navigation-close').click();await page.waitForFunction(()=>!document.body.classList.contains('sidebar-open'));}
 }
 // Root, parent and article stay ordinary page views even through historical URLs.
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge?view=all');
 await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('#article-title').waitFor();
 assert.equal(await page.locator('[aria-label="All wiki pages"]').count(),0);
 await page.locator('.document').getByRole('link',{name:'Team',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Team');
 assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0);
 await page.locator('.document').getByRole('link',{name:'Distant leaf',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Distant leaf');
 assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0);
 await page.goBack();await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Team');
 await page.goForward();await page.waitForFunction(()=>document.querySelector('#article-title')?.textContent==='Distant leaf');
 await page.reload();await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('#article-title').waitFor();
 assert.equal(await page.locator('#article-title').textContent(),'Distant leaf');
 assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0);
 assert.equal(await page.locator('[aria-label="All wiki pages"]').count(),0);
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge?view=all&page_id=sources%2Fdistant%2Fleaf');
 await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('#article-title').waitFor();
 assert.equal(await page.locator('#article-title').textContent(),'Distant leaf');
 assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0);
 await page.locator('#disconnect').click();await page.locator('#connection-card').waitFor({state:'visible'});await settle(page);await page.screenshot({path:path.join(artifacts,'disconnected-'+width+'.png'),fullPage:true});
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'disconnected overflow');
 const disconnected=await chrome(page);
 for(const screen of Object.keys(screens)) {
  await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/'+screen);await page.locator('#connection-card').waitFor({state:'visible'});await settle(page);
  assert.deepEqual(await chrome(page),disconnected,'disconnected native shell '+screen+' at '+width);
 }
 assert.deepEqual(violations,[]);await page.close();
}
console.log('All five production screens share native AdminLTE chrome across desktop, tablet and mobile; navigation, Search, disconnection and overflow checks passed.');
}finally{await browser.close();}})().catch(e=>{console.error(e);process.exit(1)});
