'use strict';
// Read-only external acceptance. Run against the real public Balda reader.
const assert=require('node:assert/strict');
const {keyboardEnter,wikiPageNode}=require('./assertions.cjs');
const fs=require('node:fs/promises');const path=require('node:path');
const {createHash}=require('node:crypto');const {execFileSync}=require('node:child_process');
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
const auto='concepts/auto-mode', conversations='catalogs/balda/conversations/index';
const balda='catalogs/balda/index', releases='catalogs/balda/releases/index';
const viewports=[{width:1440,height:1000},{width:1024,height:900},{width:390,height:844},{width:320,height:844}];
const report={startedAt:new Date().toISOString(),scenarios:[],screenshots:[],failedReads:[],unavailable:[],errors:[]};
let metadata,base,directory,browser;
const navigationIntents=new WeakSet();
async function withNavigation(page,action){navigationIntents.add(page);try{return await action();}finally{navigationIntents.delete(page);}}
const sha=bytes=>createHash('sha256').update(bytes).digest('hex');
function kubectl(...args){return execFileSync('kubectl',['--kubeconfig',metadata.cluster.kubeconfig,'--namespace',metadata.cluster.namespace,...args],{encoding:'utf8',timeout:30000,maxBuffer:4*1024*1024});}
function runtime(){
 const deployment=JSON.parse(kubectl('get','deployment',metadata.cluster.deployment,'-o','json'));
 const desired=deployment.spec.replicas;
 assert.ok(desired>0 && deployment.status.observedGeneration>=deployment.metadata.generation && deployment.status.updatedReplicas===desired && deployment.status.availableReplicas===desired,'deployment is not fully updated and available');
 const selector=Object.entries(deployment.spec.selector.matchLabels).map(([k,v])=>k+'='+v).join(',');
 const pods=JSON.parse(kubectl('get','pods','-l',selector,'-o','json')).items.filter(p=>!p.metadata.deletionTimestamp && p.status.phase==='Running');
 const selected=pods.map(p=>({pod:p,status:p.status.containerStatuses?.find(c=>c.name===metadata.cluster.container),spec:p.spec.containers.find(c=>c.name===metadata.cluster.container)})).filter(p=>p.status?.ready);
 assert.equal(selected.length,desired,'ready runtime replica count');
 const observations=selected.map(({pod,status,spec})=>{
  const source=kubectl('exec',pod.metadata.name,'--container',metadata.cluster.container,'--','cat','/usr/share/knowl/revision').trim();
  const annotations=pod.metadata.annotations||{};
  const observed={pod:pod.metadata.name,image:spec.image,imageID:status.imageID,sourceRevision:source,configurationRevision:annotations['knowl.baldaworks.dev/configuration-revision'],configurationChecksum:annotations['checksum/config']};
  assert.equal(observed.image,metadata.image,'running pod image does not match expected immutable image');
  assert.ok(observed.imageID?.endsWith(metadata.image.split('@')[1]),'running imageID does not match immutable image digest');
  assert.equal(source,metadata.sourceRevision,'revision file does not match expected source');
  assert.equal(observed.configurationRevision,metadata.configurationRevision,'observed configuration revision mismatch');
  return observed;
 });
 return {observedAt:new Date().toISOString(),deploymentGeneration:deployment.metadata.generation,replicas:observations};
}
const target=(screen='knowledge',query='')=>new URL(screen+query,base).href;
async function picture(page,name){
 const filename=name+'.png',bytes=await page.screenshot({path:path.join(directory,filename),fullPage:false});
 report.screenshots.push({filename,sha256:sha(bytes),url:page.url(),viewport:page.viewportSize()});
}
async function check(page,name,fn){
 const entry={name,viewport:page.viewportSize(),startedAt:new Date().toISOString()};report.scenarios.push(entry);
 try{await fn();entry.outcome='pass';}
 catch(error){entry.outcome='fail';entry.error=error.message;entry.stack=error.stack;}
 entry.url=page.url();entry.finishedAt=new Date().toISOString();
 await picture(page,name+'-'+entry.viewport.width+'-'+entry.outcome);
 console.log(JSON.stringify({scenario:name,width:entry.viewport.width,outcome:entry.outcome}));
 return entry.outcome==='pass';
}
async function settledArticle(page){
 await page.locator('#article').waitFor();
 await page.waitForFunction(()=>document.body.dataset.connectionState==='connected' && !document.querySelector('#screen[aria-busy="true"],#screen.htmx-settling,#screen.htmx-swapping,#screen .htmx-settling,#screen .htmx-swapping'));
 assert.equal(await page.locator('#screen [data-error-code]').count(),0,'article read failed');
}
async function settledTree(page){
 await page.locator('#wiki-tree [data-wiki-pages=""]').waitFor({state:'attached'});
 await page.waitForFunction(()=>!document.querySelector('#wiki-tree [aria-busy="true"]'));
 assert.equal(await page.locator('#wiki-tree [data-error-code],[data-wiki-stage]').count(),0,'tree contains failed or unfinished reads');
 assert.equal(await page.locator('#wiki-tree-status:not([hidden])').count(),0,'tree limit or read status');
}
async function open(page,query=''){await withNavigation(page,async()=>{await page.goto(target('knowledge',query));await settledArticle(page);await settledTree(page);});assert.equal(await page.locator('.app-sidebar .brand-logo').evaluate(e=>e.complete&&e.naturalWidth>0),true,'owl artwork did not load');}
async function noInventory(page){assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count()+await page.getByRole('button',{name:'All pages',exact:true}).count(),0,'removed All pages control returned');}
async function navigation(page,open){
 if(page.viewportSize().width>=992)return;
 const current=await page.locator('#workspace-navigation').getAttribute('aria-hidden')==='false';
 if(current!==open){await page.locator('#navigation-toggle').click();await page.waitForFunction(expected=>document.getElementById('workspace-navigation').getAttribute('aria-hidden')===String(!expected),open);}
}
async function context(page,tab){
 if(page.viewportSize().width<1200){
  const panel=page.locator('#page-context');
  if(!(await panel.evaluate(e=>e.classList.contains('show')))){await page.locator('#context-toggle').click();}
  await page.waitForFunction(()=>{const e=document.getElementById('page-context');return e.classList.contains('show')&&!e.classList.contains('showing');});
 }
 await page.locator('#'+tab+'-tab').click();
 await page.waitForFunction(id=>document.getElementById(id+'-tab').getAttribute('aria-selected')==='true',tab);
}
async function closeContext(page){
 if(page.viewportSize().width<1200 && await page.locator('#page-context').evaluate(e=>e.classList.contains('show'))){await page.keyboard.press('Escape');await page.waitForFunction(()=>!document.getElementById('page-context').classList.contains('show'));}
}
async function pageNode(page,id){return wikiPageNode(page,id,settledTree);}
async function crumbLink(page,id){
 const links=page.locator('[aria-label="Breadcrumb"] a');
 const hrefs=await links.evaluateAll(nodes=>nodes.map(n=>n.href));
 const i=hrefs.findIndex(href=>{const q=new URL(href).searchParams;return (q.get('page_id')||q.get('parent_id'))===id;});
 assert.ok(i>=0,'verified breadcrumb context is missing: '+id);return links.nth(i);
}
async function chrome(page){return page.evaluate(()=>{
 const styles=selector=>{const e=document.querySelector(selector);assertElement(e,selector);const s=getComputedStyle(e);return Object.fromEntries(['fontFamily','fontSize','fontWeight','color','backgroundColor','borderRadius'].map(k=>[k,s[k]]));};
 function assertElement(e,selector){if(!e)throw new Error('missing component '+selector);}
 return {body:styles('body'),sidebar:styles('.app-sidebar'),brand:styles('.brand-text'),header:styles('.app-header'),nav:styles('[data-screen].active p'),card:styles('#screen .card'),width:getComputedStyle(document.querySelector('.app-sidebar')).width};
 });}
async function runViewport(viewport){
 const page=await browser.newPage({viewport,reducedMotion:'reduce'});page.setDefaultTimeout(10000);
 page.on('pageerror',error=>report.errors.push({viewport,error:error.message,url:page.url()}));
 page.on('requestfailed',request=>{const error=request.failure()?.errorText;report.failedReads.push({viewport,url:request.url(),error,classification:error==='net::ERR_ABORTED'&&navigationIntents.has(page)?'intentional-navigation-abort':'unexpected'});});
 page.on('response',response=>{if(response.status()>=400)report.failedReads.push({viewport,url:response.url(),status:response.status(),classification:'unexpected'});});
 const exact='?page_id='+encodeURIComponent(auto)+'&parent_id='+encodeURIComponent(conversations);
 try{
  await check(page,'published-structure',async()=>{
   await open(page);
   const rows=await page.locator('#wiki-tree > [data-page-branch] > [data-wiki-pages=""] > [data-page-node]').evaluateAll(nodes=>nodes.map(node=>({key:node.dataset.pageNode,path:node.dataset.pagePath,pageID:node.querySelector(':scope > [data-wiki-page]')?.dataset.wikiPage||null,group:node.querySelector(':scope > [data-wiki-group]')?.textContent||null,links:node.querySelectorAll(':scope > a').length,caret:!!node.querySelector(':scope > [data-page-toggle]')})));
   assert.ok(rows.length>0,'real published root has no navigation rows');
   for(const row of rows){
    const parts=row.path.split('/');
    assert.ok(parts.length===1 || (parts.length===2 && parts[1]==='index.md'),'root preserves actual immediate structure');
    if(row.group!==null){assert.equal(row.key,'group:'+row.path);assert.equal(row.group,parts[0]);assert.equal(row.pageID,null);assert.equal(row.links,0,'structural group has no article action');}
    else {assert.ok(row.pageID);assert.equal(row.key,'page:'+row.pageID);assert.equal(row.links,1);}
   }
   (report.treeObservations||=[]).push({viewport,directory:'',rows});
   if(!rows.some(row=>row.group!==null))report.unavailable.push({viewport,scenario:'indexless-group',reason:'actual published root has no indexless groups; deterministic filesystem fixture covers them'});
  });
  await check(page,'exact-context',async()=>{
   await open(page,exact);assert.equal(await page.locator('#article').getAttribute('data-page-id'),auto);await crumbLink(page,conversations);await noInventory(page);
   const before=await page.locator('.document').textContent();await withNavigation(page,async()=>{await page.reload();await settledArticle(page);await settledTree(page);});await crumbLink(page,conversations);assert.equal(await page.locator('.document').textContent(),before);
   const parent=await crumbLink(page,conversations);await withNavigation(page,async()=>{await parent.click();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),conversations);await noInventory(page);
   const authored=page.locator('.document a').filter({hasText:/Auto mode/i});assert.ok(await authored.count()>0,'real Conversations authored Auto mode link is missing');await withNavigation(page,async()=>{await authored.first().click();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),auto);await crumbLink(page,conversations);
   await withNavigation(page,async()=>{await page.goBack();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),conversations);
   await withNavigation(page,async()=>{await page.goForward();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),auto);await crumbLink(page,conversations);
  });
  await check(page,'root-and-parent',async()=>{
   await open(page);assert.equal(await page.locator('#article').getAttribute('data-page-id'),'index');await navigation(page,true);
   const parent=await pageNode(page,balda),caret=parent.locator(':scope > [data-page-toggle]');assert.equal(await caret.count(),1,'actual parent must disclose children');
   if(await caret.getAttribute('aria-expanded')==='true')await caret.click();
   const id=await page.locator('#article').getAttribute('data-page-id');await caret.focus();await page.keyboard.press('Enter');await settledTree(page);assert.equal(await page.locator('#article').getAttribute('data-page-id'),id,'caret navigated the article');
   assert.ok(await parent.locator('[data-page-branch] [data-page-node]').count()>0,'actual parent expanded to no pages');
   await withNavigation(page,async()=>{await parent.locator(':scope > a').click();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),balda);await noInventory(page);
  });
  await check(page,'releases-leaf',async()=>{
   await navigation(page,true);const leaf=await pageNode(page,releases);assert.equal(await leaf.locator(':scope > [data-page-toggle]').count(),0,'Balda releases has a dead disclosure');assert.equal(await leaf.locator('[data-page-branch]').count(),0);
   await withNavigation(page,async()=>{await leaf.locator(':scope > a').click();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),releases);await noInventory(page);
  });
  await check(page,'native-five-screens',async()=>{
   await open(page,exact);const reference=await chrome(page);let failures=0;
   for(const [screen,ready] of Object.entries({knowledge:'#article',search:'#search-form',operations:'.operation-history',sources:'.source-summary-grid',statistics:'[data-statistic="pages"]'})){
    const passed=await check(page,'native-'+screen,async()=>{
     await navigation(page,true);await withNavigation(page,async()=>{await page.locator('[data-screen="'+screen+'"]').click();await page.locator(ready).waitFor();});assert.equal(await page.locator('#screen [data-error-code]').count(),0,screen+' read failed');
     assert.deepEqual(await chrome(page),reference,'native visual system changes on '+screen);await noInventory(page);
     assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,screen+' widens document');
    });if(!passed)failures++;
   }
   assert.equal(failures,0,'one or more native screens failed');
  });
  await check(page,'same-article-two-contexts',async()=>{
   await open(page,exact);const body=await page.locator('.document').textContent();await navigation(page,true);
   const item=await pageNode(page,auto);await withNavigation(page,async()=>{await item.locator(':scope > a').click();await settledArticle(page);});
   assert.equal(await page.locator('#article').getAttribute('data-page-id'),auto);assert.equal(await page.locator('.document').textContent(),body);
   const ids=await page.locator('[aria-label="Breadcrumb"] a').evaluateAll(nodes=>nodes.map(n=>{const q=new URL(n.href).searchParams;return q.get('page_id')||q.get('parent_id');}));assert.equal(ids.includes(conversations),false,'page tree was replaced with authored context');
   await open(page,exact);await crumbLink(page,conversations);assert.equal(await page.locator('.document').textContent(),body);
  });
  await check(page,'keyboard-context-source',async()=>{
   await open(page,exact);await context(page,'contents');
   const heading=page.locator('#outline a').first();if(await heading.count()){
    const href=await heading.getAttribute('href');await keyboardEnter(page,heading);await page.waitForFunction(id=>document.activeElement?.id===id,decodeURIComponent(href.slice(1)));assert.equal(new URL(page.url()).hash,href);if(viewport.width<1200)await page.locator('#page-context').waitFor({state:'hidden'});
   } else report.unavailable.push({viewport,scenario:'outline',reason:'real article has no section headings'});
   await context(page,'details');assert.ok((await page.locator('#details').textContent()).trim().length>0);await context(page,'sources');
   const read=page.getByRole('button',{name:'Read saved source',exact:true}).first();
   if(await read.count()){
    const sourceURL=new URL(await read.getAttribute('hx-get'),page.url()).href;await read.click();await page.waitForFunction(()=>{const e=document.getElementById('raw-source');return !e.hasAttribute('aria-busy')&&!!e.querySelector('pre,[data-error-code]');});
    const error=page.locator('#raw-source [data-error-code]');
    if(await error.count()){
     const reason=await error.getAttribute('data-error-code');assert.ok(['source_revision_not_found','unsupported_format'].includes(reason),'unexpected saved-source failure '+reason);
     report.unavailable.push({viewport,scenario:'saved-source',reason});
     for(const read of report.failedReads)if(read.url===sourceURL&&read.viewport.width===viewport.width){read.classification='confirmed-unavailable-source';read.reason=reason;}
    }
    else assert.ok((await page.locator('#raw-source pre').textContent()).length>0,'saved source text is empty');
    await page.getByRole('button',{name:'Close saved source',exact:true}).click();assert.equal(await page.locator('#raw-source').textContent(),'');
   } else report.unavailable.push({viewport,scenario:'saved-source',reason:'real article records no saved source reference'});
   await closeContext(page);await navigation(page,true);
   if(viewport.width<992){assert.equal(await page.locator('#workspace-navigation').getAttribute('aria-modal'),'true');await page.keyboard.press('Escape');await page.waitForFunction(()=>document.getElementById('workspace-navigation').getAttribute('aria-hidden')==='true');assert.equal(await page.locator('#navigation-toggle').evaluate(e=>document.activeElement===e),true);}
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  });
  if(viewport.width===1440)await check(page,'search-and-history',async()=>{
   await page.locator('[data-screen="search"]').click();await page.locator('#search-form').waitFor();await page.locator('#query').fill('auto mode');await page.locator('#search-form button').click();
   await page.locator('#search-results #response-json-data').waitFor({state:'attached'});await page.waitForFunction(()=>!document.querySelector('#search-results.htmx-settling,#search-results.htmx-swapping'));
   const snapshot=await page.evaluate(()=>{
    const html=document.getElementById('screen').innerHTML,query=document.getElementById('query').value,encoder=new TextEncoder(),limit=256*1024;
    const sizes={htmlCharacters:html.length,queryCharacters:query.length,htmlUTF8Bytes:encoder.encode(html).length,queryUTF8Bytes:encoder.encode(query).length,limit};
    return {...sizes,eligible:sizes.htmlCharacters+sizes.queryCharacters<=limit&&sizes.htmlUTF8Bytes+sizes.queryUTF8Bytes<=limit};
   });
   (report.searchSnapshots||=[]).push({viewport,...snapshot});
   const evidence=page.locator('.evidence-title').first();assert.ok(await evidence.count(),'actual search returned no evidence');await withNavigation(page,async()=>{await evidence.click();await settledArticle(page);});const selected=await page.locator('#article').getAttribute('data-page-id');assert.ok(selected);
   await withNavigation(page,async()=>{await page.goBack();await page.locator('#search-form').waitFor();});
   if(snapshot.eligible){await page.locator('.evidence-title').first().waitFor();assert.equal(await page.locator('#query').inputValue(),'auto mode');}
   else {
    report.unavailable.push({viewport,scenario:'bounded-search-restoration',reason:'actual Search view exceeds the existing 256 KiB snapshot budget',...snapshot});
    assert.equal(await page.locator('#query').inputValue(),'','oversized view must return to fresh Search');assert.equal(await page.locator('.evidence-title,#response-json-data').count(),0,'oversized evidence must not be partially restored');
   }
   await withNavigation(page,async()=>{await page.goForward();await settledArticle(page);});assert.equal(await page.locator('#article').getAttribute('data-page-id'),selected);
  });
  await check(page,'legacy-url-and-overflow',async()=>{
   await open(page,'?view=all');assert.equal(await page.locator('#article').getAttribute('data-page-id'),'index');await noInventory(page);
   const available=await page.locator('#article-body .table-responsive,#article-body pre').count();if(!available)report.unavailable.push({viewport,scenario:'wide-content',reason:'this real root has no table/code; deterministic long-content regression supplements it'});
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  });
 }finally{await withNavigation(page,()=>page.close());}
}
(async()=>{
 assert.ok(process.env.KNOWL_E2E_METADATA,'KNOWL_E2E_METADATA is required');metadata=JSON.parse(await fs.readFile(process.env.KNOWL_E2E_METADATA,'utf8'));report.runner={filename:path.basename(__filename),sha256:sha(await fs.readFile(__filename)),helpers:{'assertions.cjs':sha(await fs.readFile(path.join(__dirname,'assertions.cjs')))}};
 assert.ok(['baseline','candidate'].includes(metadata.kind));assert.match(metadata.sourceRevision,/^[a-f0-9]{40}$/);assert.match(metadata.image,/@sha256:[a-f0-9]{64}$/);assert.ok(metadata.configurationRevision && metadata.adaptation && metadata.cluster?.kubeconfig);
 base=new URL(metadata.targetBase);assert.ok(['http:','https:'].includes(base.protocol)&&!base.username&&!base.password&&base.pathname.endsWith('/'));
 directory=process.env.KNOWL_BROWSER_ARTIFACT_DIR||path.resolve('balda-e2e-artifacts');await fs.mkdir(directory,{recursive:true});report.declared=metadata;report.targetBase=base.href;report.kind=metadata.kind;report.observedBefore=runtime();
 browser=await chromium.launch({headless:true,args:['--no-sandbox']});for(const viewport of viewports)await runViewport(viewport);report.observedAfter=runtime();
})().catch(error=>{report.errors.push({error:error.message,stage:'setup/runtime'});}).finally(async()=>{
 if(browser)await browser.close();report.finishedAt=new Date().toISOString();report.unexpectedReadCount=report.failedReads.filter(r=>r.classification==='unexpected').length;report.outcome=report.errors.length||report.unexpectedReadCount||report.scenarios.some(s=>s.outcome==='fail')?'fail':'pass';report.candidateAccepted=metadata?.kind==='candidate'&&report.outcome==='pass';
 if(directory)await fs.writeFile(path.join(directory,'report.json'),JSON.stringify(report,null,2)+'\n');
 console.log(JSON.stringify({kind:metadata?.kind,outcome:report.outcome,candidateAccepted:report.candidateAccepted,scenarios:report.scenarios.length,failures:report.scenarios.filter(s=>s.outcome==='fail').map(s=>({name:s.name,viewport:s.viewport,error:s.error})),errors:report.errors,report:directory&&path.join(directory,'report.json')},null,2));
 if(report.outcome!=='pass')process.exitCode=1;
});
