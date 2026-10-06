'use strict';
const assert=require('node:assert/strict');
const path=require('node:path');
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 const failures=[];
 const check=async(name,run)=>{try{await run();console.log('PASS: '+name);}catch(e){failures.push(name+': '+e.message);console.error('FAIL: '+name+': '+e.message);}};
 const context=await browser.newContext({viewport:{width:1366,height:900},colorScheme:'dark',reducedMotion:'reduce'});
 await context.addInitScript(()=>localStorage.setItem('lte-theme','dark'));
 const page=await context.newPage();const violations=[];
 await page.exposeFunction('recordCSP',v=>violations.push(v));
 await page.addInitScript(()=>document.addEventListener('securitypolicyviolation',e=>window.recordCSP(e.violatedDirective)));
 const connect=async(token='browser-secret-token')=>{await page.locator('#operator-token').fill(token);await page.locator('#connect-form button').click();};
 try{
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge');
 await check('guest navigation unavailable',async()=>{assert.equal(await page.locator('.app-sidebar').isVisible(),false);assert.equal(await page.locator('[data-lte-toggle="sidebar"]').isVisible(),false);assert.equal((await page.locator('.app-main').boundingBox()).x,0);});
 await check('pinned light and CSP safe reduced motion',async()=>{assert.equal(await page.locator('html').getAttribute('data-bs-theme'),'light');assert.equal(await page.locator('.form-control').evaluate(e=>getComputedStyle(e).backgroundColor),'rgb(255, 255, 255)');assert.deepEqual(violations,[]);});
 let release,ready;const held=new Promise(r=>{ready=r;});const gate=new Promise(r=>{release=r;});
 await page.route('**/ui/fragments/knowledge',async route=>{const response=await route.fetch();ready();await gate;await route.fulfill({response}).catch(()=>{});},{times:1});
 await connect();await held;
 await check('held first response remains connecting',async()=>{assert.equal(await page.locator('#connection-status').textContent(),'Connecting…');assert.equal(await page.locator('.app-sidebar').isVisible(),false);assert.equal(await page.locator('#connection-card').isVisible(),true);});
 release();await page.locator('#protected-document').waitFor();
 await check('desktop collapse reclaims width and semantics',async()=>{assert.ok(Math.abs((await page.locator('.app-main').boundingBox()).x-224)<2);await page.locator('[data-lte-toggle="sidebar"]').click();await page.waitForFunction(()=>document.body.classList.contains('sidebar-collapse'));assert.equal((await page.locator('.app-main').boundingBox()).x,0);assert.equal(await page.locator('.app-sidebar').evaluate(e=>e.inert),true);assert.equal(await page.locator('[data-lte-toggle="sidebar"]').getAttribute('aria-expanded'),'false');});
 await page.locator('[data-lte-toggle="sidebar"]').click();
 await check('same document brand preserves authorization',async()=>{await page.locator('.brand-link').click();await page.locator('#protected-document').waitFor({timeout:2000});assert.equal(await page.locator('#connection-status').textContent(),'Connected');});
 if(await page.locator('#connection-status').textContent()!=='Connected'){await connect();await page.locator('#protected-document').waitFor();}
 await page.setViewportSize({width:390,height:844});
 await check('mobile focus containment and Escape',async()=>{await page.locator('[data-lte-toggle="sidebar"]').click();assert.equal(await page.locator('.app-sidebar').evaluate(e=>e.contains(document.activeElement)),true);assert.equal(await page.locator('.app-main').evaluate(e=>e.inert),true);await page.keyboard.press('Escape');assert.equal(await page.locator('[data-lte-toggle="sidebar"]').evaluate(e=>e===document.activeElement),true);assert.equal(await page.locator('.app-main').evaluate(e=>e.inert),false);});
 await check('mobile Tab cycle, overlay, close and resize',async()=>{
  await page.locator('#navigation-toggle').click();
  await page.locator('.sidebar-menu a').last().focus();await page.keyboard.press('Tab');assert.equal(await page.locator('.brand-link').evaluate(e=>e===document.activeElement),true,'forward Tab wraps to brand');
  await page.keyboard.press('Shift+Tab');assert.equal(await page.locator('.sidebar-menu a').last().evaluate(e=>e===document.activeElement),true,'reverse Tab wraps to last navigation link');
  await page.locator('#navigation-close').click();assert.equal(await page.locator('#navigation-toggle').evaluate(e=>e===document.activeElement),true);
  await page.locator('#navigation-toggle').click();await page.mouse.click(375,400);assert.equal(await page.locator('.app-sidebar').evaluate(e=>e.inert),true);
  await page.setViewportSize({width:992,height:844});await page.waitForFunction(()=>!document.getElementById('workspace-navigation').inert);assert.equal(await page.locator('.app-sidebar').evaluate(e=>e.inert),false);assert.equal(await page.locator('.app-main').evaluate(e=>e.inert),false);
  await page.setViewportSize({width:991,height:844});await page.waitForFunction(()=>document.getElementById('workspace-navigation').inert);assert.equal(await page.locator('.app-sidebar').evaluate(e=>e.inert),true);
 });
 for(const width of [320,390,640,768,1024,1366,1920]){
  await page.setViewportSize({width,height:900});
  await check('header and layout at '+width,async()=>{
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
   const start=await page.locator('.header-start').boundingBox(),end=await page.locator('.header-end').boundingBox();
   assert.ok(start.x+start.width<=end.x || start.y+start.height<=end.y);
   assert.ok(end.x+end.width<=width);
  });
 }
 await page.setViewportSize({width:390,height:844});await page.locator('#disconnect').click();await connect('invalid-token');await page.waitForFunction(()=>document.getElementById('connection-status').textContent==='Disconnected');
 await check('rejected credentials actionable and focused',async()=>{assert.equal(await page.locator('#connection-feedback').isVisible(),true);assert.equal(await page.locator('#operator-token').getAttribute('aria-invalid'),'true');assert.equal(await page.locator('#operator-token').evaluate(e=>e===document.activeElement),true);});
 if(process.env.KNOWL_BROWSER_ARTIFACT_DIR)await page.screenshot({path:path.join(process.env.KNOWL_BROWSER_ARTIFACT_DIR,'task-012-rejected-mobile.png'),fullPage:true});
 await page.setViewportSize({width:1366,height:900});await page.reload();
 await page.route('**/ui/fragments/knowledge',route=>route.abort('failed'),{times:1});await connect();await page.waitForFunction(()=>document.body.dataset.connectionState==='disconnected');
 await check('network failure actionable without navigation',async()=>{assert.equal(await page.locator('#connection-feedback').isVisible(),true);assert.equal(await page.locator('.app-sidebar').isVisible(),false);});
 await connect();await page.locator('#protected-document').waitFor();await page.locator('#disconnect').click();

 if(process.env.KNOWL_BROWSER_ARTIFACT_DIR)await page.screenshot({path:path.join(process.env.KNOWL_BROWSER_ARTIFACT_DIR,'task-012-guest-desktop.png'),fullPage:true});
 await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge?page_id=missing');await connect();await page.locator('[data-error-code="capability_unavailable"]').waitFor();
 await check('trusted domain failure proves authorization',async()=>{assert.equal(await page.locator('#connection-status').textContent(),'Connected');assert.equal(await page.locator('.app-sidebar').isVisible(),true);});
 await page.locator('#disconnect').click();await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge');
 await page.route('**/ui/fragments/knowledge',route=>route.fulfill({status:503,headers:{'X-Knowl-Error':'unrecognized_error'},body:'<p id="unknown-error">Unsafe body</p>'}),{times:1});await connect();await page.waitForFunction(()=>document.body.dataset.connectionState==='disconnected');
 await check('unknown marked errors do not authorize',async()=>{assert.equal(await page.locator('#unknown-error').count(),0);assert.equal(await page.locator('.app-sidebar').isVisible(),false);});

 await check('runtime motion preferences keep CSP intact',async()=>{await page.emulateMedia({reducedMotion:'no-preference'});await page.emulateMedia({reducedMotion:'reduce'});assert.equal(await page.locator('.btn').first().evaluate(e=>getComputedStyle(e).transitionDuration),'1e-05s');assert.deepEqual(violations,[]);});
 }finally{await browser.close();}
 if(failures.length)throw new Error(failures.join('\n'));
})().catch(e=>{console.error(e);process.exitCode=1;});
