'use strict';
const assert=require('node:assert/strict');
const crypto=require('node:crypto');
const fs=require('node:fs/promises');
const path=require('node:path');
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
const {wikiPageNode}=require('./assertions.cjs');
const base=process.env.KNOWL_BROWSER_URL;
const directory=process.env.KNOWL_BROWSER_ARTIFACT_DIR;
const hash=body=>crypto.createHash('sha256').update(body).digest('hex');
const report={outcome:'fail',failureCode:'browser_assertion',url:base,assets:[],scenarios:[],screenshots:[],runnerHashes:{}};
(async()=>{
 await fs.mkdir(directory,{recursive:true});
 for(const name of ['cache_upgrade.cjs','assertions.cjs'])report.runnerHashes[name]=hash(await fs.readFile(path.join(__dirname,name)));
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'});
  const page=await context.newPage(),responses=new Map(),pending=[],errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('response',response=>{
   const url=new URL(response.url());
   if(url.pathname.startsWith('/ui/assets/ui.'))pending.push((async()=>{
    responses.set(url.pathname,{url:response.url(),status:response.status(),headers:await response.allHeaders(),sha256:hash(await response.body())});
   })());
  });
  const stats=async()=>{const response=await context.request.get(base+'/__qa/cache');assert.equal(response.status(),200);return response.json();};
  const screenshot=async name=>{const filename=name+'.png';await page.screenshot({path:path.join(directory,filename)});report.screenshots.push({file:filename,sha256:hash(await fs.readFile(path.join(directory,filename)))});};
  const ownPaths=()=>page.evaluate(()=>[...document.querySelectorAll('script[src],link[rel="stylesheet"]')].map(n=>n.getAttribute('src')||n.getAttribute('href')).filter(value=>value.startsWith('/ui/assets/ui.')));
  await page.goto(base+'/ui/knowledge');
  await Promise.all(pending.splice(0));
  const legacyPaths=await ownPaths();
  assert.deepEqual(legacyPaths.sort(),['/ui/assets/ui.css','/ui/assets/ui.js']);
  report.warm=await stats();
  for(const pathname of legacyPaths){const record=responses.get(pathname);assert.equal(record.status,200);assert.equal(record.headers['cache-control'],'public, max-age=14400');assert.equal(record.sha256,report.warm.assets[pathname].sha256);assert.equal(report.warm.assets[pathname].misses,1);}
  assert.equal((await context.request.post(base+'/__qa/cache')).status(),200);
  responses.clear();
  const candidateDocument=await page.goto(base+'/ui/knowledge');
  await Promise.all(pending.splice(0));
  report.html={sha256:hash(await candidateDocument.body()),status:candidateDocument.status(),headers:await candidateDocument.allHeaders()};
  assert.equal(report.html.sha256,(await stats()).expected.html);
  const candidatePaths=await ownPaths(),expected=(await stats()).expected;
  for(const pathname of candidatePaths){
   const record=responses.get(pathname),extension=path.extname(pathname).slice(1);
   report.assets.push({...record,expectedSHA256:expected[extension]});
   if(!record||record.sha256!==expected[extension]||legacyPaths.includes(pathname)){
    report.failureCode='asset_identity_mismatch';await screenshot('stale-upgrade-rejected');throw new Error('Loaded own asset does not match candidate bytes: '+pathname);
   }
   assert.equal(record.status,200);assert.equal(record.headers['cache-control'],'public, max-age=31536000, immutable');assert.equal(record.headers['x-qa-cache'],'MISS');
   assert.equal(path.basename(pathname),'ui.'+expected[extension]+'.'+extension);
   assert.equal(record.headers['content-type'],extension==='js'?'text/javascript; charset=utf-8':'text/css; charset=utf-8');
  }
  assert.equal(candidatePaths.length,2);
  report.beforeReload=await stats();
  responses.clear();
  await page.reload();await Promise.all(pending.splice(0));
  report.afterReload=await stats();
  report.reloadAssets=candidatePaths.map(pathname=>{assert.ok(responses.has(pathname),'reload must produce a fresh observation: '+pathname);return responses.get(pathname);});
  for(const pathname of candidatePaths){assert.equal(responses.get(pathname).sha256,expected[path.extname(pathname).slice(1)]);assert.equal(report.afterReload.assets[pathname].misses,1);}
  for(const pathname of legacyPaths){
   const response=await context.request.get(base+pathname);assert.equal(response.headers()['x-qa-cache'],'HIT');assert.equal(hash(await response.body()),report.warm.assets[pathname].sha256,'old stable cache remains stale');
  }
  for(const pathname of candidatePaths){const response=await context.request.get(base+pathname);assert.equal(response.headers()['x-qa-cache'],'HIT');assert.equal(hash(await response.body()),expected[path.extname(pathname).slice(1)]);}
  report.cache=await stats();
  async function connect(){await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('#article-title').waitFor();await page.waitForFunction(()=>document.body.dataset.connectionState==='connected');}
  const article=title=>page.waitForFunction(value=>document.querySelector('#article-title')?.textContent===value,title);
  const crumbs=()=>page.locator('[aria-label="Breadcrumb"] li').allTextContents();
  async function navigation(){if(await page.evaluate(()=>matchMedia('(max-width:991.98px)').matches)){await page.waitForFunction(()=>{const node=document.getElementById('workspace-navigation');return !node.classList.contains('showing')&&!node.classList.contains('hiding');});if(await page.locator('#workspace-navigation').getAttribute('aria-modal')!=='true'){await page.locator('#navigation-toggle').click();await page.waitForFunction(()=>{const node=document.getElementById('workspace-navigation');return node.getAttribute('aria-modal')==='true'&&!node.classList.contains('showing');});}}}
  async function canonical(pageID,trail){const url=new URL(page.url());assert.equal(url.searchParams.get('page_id'),pageID);assert.deepEqual(JSON.parse(url.searchParams.get('trail')||'[]'),trail);if(trail.length)assert.equal(url.searchParams.get('parent_id'),trail.at(-1));}
  for(const viewport of [{width:1440,height:1000},{width:1024,height:900},{width:390,height:844},{width:320,height:844}]){
   await page.setViewportSize(viewport);await page.goto(base+'/ui/knowledge');await connect();await navigation();
   const balda=await wikiPageNode(page,'catalogs/balda/index');await balda.locator(':scope > a').click();await article('Balda');await canonical('catalogs/balda/index',['index']);
   await navigation();const releases=await wikiPageNode(page,'catalogs/balda/releases/index');assert.equal(await releases.locator(':scope > [data-page-toggle]').count(),0);
   // Expand arbitrary Unicode/nested/wrapped groups and a real indexed parent.
   for(const id of ['AI/Prism','concepts/ADRs/first','研究/renamed/item'])await wikiPageNode(page,id);
   await screenshot('upgraded-tree-nested-'+viewport.width);
   await page.locator('#tree-scroll').evaluate(node=>node.scrollTop=0);
   const geometry=await page.locator('#tree-scroll').evaluate(scroller=>{
    const viewport=scroller.getBoundingClientRect();
    return [...scroller.querySelectorAll('[data-wiki-page],[data-wiki-group]')].filter(node=>node.getClientRects().length).map(node=>{
     const range=document.createRange();range.selectNodeContents(node);const lines=[...range.getClientRects()].filter(r=>r.width>0).map(r=>({left:r.left,right:r.right,top:r.top,bottom:r.bottom}));
     const caret=node.parentElement.querySelector(':scope > [data-page-toggle]');let hit=null,ink=null;
     if(caret){const r=caret.getBoundingClientRect();hit={left:r.left,right:r.right,top:r.top,bottom:r.bottom};const glyph=document.createRange();glyph.selectNodeContents(caret.querySelector('span'));const g=glyph.getBoundingClientRect();ink={left:g.left,right:g.right,top:g.top,bottom:g.bottom};}
     return {identity:node.parentElement.dataset.pageNode,lines,hit,ink,viewport:{left:viewport.left,right:viewport.right},scrollLeft:scroller.scrollLeft};
    });
   });
   assert.ok(geometry.length>8);
   for(const row of geometry){assert.equal(row.scrollLeft,0);for(const line of row.lines){assert.ok(line.left>=row.viewport.left && line.right<=row.viewport.right,JSON.stringify(row));if(row.hit)assert.ok(line.left>=Math.max(row.hit.right,row.ink.right)+4,JSON.stringify(row));}}
   for(const group of ['catalogs','concepts']){
    const id='group:'+group,rootRow=geometry.find(row=>row.identity===id);assert.ok(rootRow,'required real root group');
    const label=page.locator('#wiki-tree [data-page-node='+JSON.stringify(id)+'] > [data-wiki-group]');
    await label.scrollIntoViewIfNeeded();
    assert.equal(await label.evaluate(node=>{const line=node.getBoundingClientRect(),viewport=document.getElementById('tree-scroll').getBoundingClientRect();return line.top>=viewport.top&&line.bottom<=viewport.bottom;}),true,'root label visible in captured tree: '+id);
    await screenshot('upgraded-tree-root-'+group+'-'+viewport.width);
   }
   await releases.locator(':scope > a').click();await article('Balda releases');await canonical('catalogs/balda/releases/index',['index','catalogs/balda/index']);assert.deepEqual(await crumbs(),['Knowl Index','Balda','Balda releases']);
   await page.goBack();await article('Balda');await canonical('catalogs/balda/index',['index']);
   await page.goForward();await article('Balda releases');await canonical('catalogs/balda/releases/index',['index','catalogs/balda/index']);
   await page.reload();await connect();await article('Balda releases');assert.deepEqual(await crumbs(),['Knowl Index','Balda','Balda releases']);
   await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Balda',exact:true}).click();await article('Balda');await canonical('catalogs/balda/index',['index']);
   await page.goto(base+'/ui/knowledge?page_id=concepts%2Fauto-mode&parent_id=catalogs%2Fbalda%2Fconversations%2Findex');await connect();await article('Auto mode');assert.deepEqual(await crumbs(),['Knowl Index','Conversations','Auto mode']);
   await page.reload();await connect();await article('Auto mode');assert.deepEqual(await crumbs(),['Knowl Index','Conversations','Auto mode']);
   await screenshot('upgraded-context-'+viewport.width);
   await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Conversations',exact:true}).click();await article('Conversations');
   await page.locator('.document').getByRole('link',{name:'Auto mode',exact:true}).click();await article('Auto mode');await canonical('concepts/auto-mode',['catalogs/balda/conversations/index']);
   await page.goBack();await article('Conversations');await page.goForward();await article('Auto mode');assert.deepEqual(await crumbs(),['Knowl Index','Conversations','Auto mode']);
   assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0);
   report.scenarios.push({viewport,url:page.url(),geometry,context:await crumbs(),outcome:'pass'});
  }
  assert.deepEqual(errors,[]);report.outcome='pass';delete report.failureCode;
  console.log('PASS: same-browser warmed-cache upgrade, exact delivered bytes, four-width canonical navigation and root/nested glyph geometry');
 }finally{await browser.close();}
})().catch(error=>{report.error=String(error);console.error(error);process.exitCode=1;}).finally(async()=>{await fs.writeFile(path.join(directory,'cache-upgrade.json'),JSON.stringify(report,null,2)+'\n');});
