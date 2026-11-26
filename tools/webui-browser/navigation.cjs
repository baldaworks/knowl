'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');const path=require('node:path');
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
const auto='concepts/auto-mode', conversations='catalogs/balda/conversations/index';
const exact='?page_id=concepts%2Fauto-mode&parent_id=catalogs%2Fbalda%2Fconversations%2Findex';
(async()=>{const browser=await chromium.launch({headless:true,args:['--no-sandbox']});try{
 for(const viewport of [{width:1440,height:1000},{width:1024,height:900},{width:390,height:844},{width:320,height:844}]){
  const page=await browser.newPage({viewport,reducedMotion:'reduce'}),errors=[];page.on('pageerror',e=>errors.push(e.message));
  async function connected(){await page.locator('#operator-token').fill('browser-secret-token');await page.locator('#connect-form button').click();await page.locator('#article-title').waitFor();}
  async function article(title){await page.waitForFunction(t=>document.querySelector('#article-title')?.textContent===t,title);}
  const crumbs=()=>page.locator('[aria-label="Breadcrumb"] li').allTextContents();
  async function open(query){await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge'+query);await connected();}
  await open(exact);await article('Balda auto mode');assert.deepEqual(await crumbs(),['Root','Conversations','Balda auto mode']);
  const body=await page.locator('.document').textContent();
  await page.reload();await connected();assert.deepEqual(await crumbs(),['Root','Conversations','Balda auto mode']);
  await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Conversations',exact:true}).click();await article('Conversations');assert.deepEqual(await crumbs(),['Root','Conversations']);
  await page.locator('.document').getByRole('link',{name:'Auto mode',exact:true}).click();await article('Balda auto mode');
  assert.deepEqual(await crumbs(),['Root','Conversations','Balda auto mode']);
  assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('trail')),[conversations]);
  await page.goBack();await article('Conversations');await page.goForward();await article('Balda auto mode');assert.deepEqual(await crumbs(),['Root','Conversations','Balda auto mode']);
  await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Root',exact:true}).click();await article('Root');
  await page.locator('.document').getByRole('link',{name:'Balda',exact:true}).click();await article('Balda');
  await page.locator('.document').getByRole('link',{name:'Conversations',exact:true}).click();await article('Conversations');
  await page.locator('.document').getByRole('link',{name:'Auto mode',exact:true}).click();await article('Balda auto mode');
  assert.deepEqual(await crumbs(),['Root','Balda','Conversations','Balda auto mode']);
  assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('trail')),['index','catalogs/balda/index',conversations]);
  await page.reload();await connected();assert.deepEqual(await crumbs(),['Root','Balda','Conversations','Balda auto mode']);
  const conversationNode=page.locator('#wiki-tree [data-page-node="page:catalogs/balda/conversations/index"]');
  await conversationNode.waitFor();
  assert.equal(await conversationNode.locator(':scope > [data-page-toggle]').count(),0,'context from authored links never invents physical children');
  assert.equal(await conversationNode.locator('[data-page-branch]').count(),0,'leaf offers no dead empty expansion');
  if(process.env.KNOWL_BROWSER_ARTIFACT_DIR){await fs.mkdir(process.env.KNOWL_BROWSER_ARTIFACT_DIR,{recursive:true});await page.screenshot({path:path.join(process.env.KNOWL_BROWSER_ARTIFACT_DIR,'navigation-context-'+viewport.width+'.png')});}
  await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Balda',exact:true}).click();await article('Balda');assert.deepEqual(await crumbs(),['Root','Balda']);
  assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('trail')),['index']);
  await page.goBack();await article('Balda auto mode');assert.deepEqual(await crumbs(),['Root','Balda','Conversations','Balda auto mode']);
  await page.getByRole('navigation',{name:'Breadcrumb'}).getByRole('link',{name:'Root',exact:true}).click();await article('Root');
  await page.locator('.document').getByRole('link',{name:'Other context',exact:true}).click();await article('Other context');
  await page.locator('.document').getByRole('link',{name:'Auto mode',exact:true}).click();await article('Balda auto mode');assert.deepEqual(await crumbs(),['Root','Other context','Balda auto mode']);assert.equal(await page.locator('.document').textContent(),body,'article is independent of context');
  await open('?page_id='+encodeURIComponent(auto));assert.deepEqual(await crumbs(),['Root','Balda auto mode'],'direct URL without context invents no physical ancestors');
  await open('?page_id='+encodeURIComponent(auto)+'&trail='+encodeURIComponent(JSON.stringify([conversations,conversations])));assert.deepEqual(await crumbs(),['Root','Balda auto mode'],'cyclic context keeps article');
  assert.equal(await page.getByRole('link',{name:'All pages',exact:true}).count(),0);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  assert.deepEqual(errors,[]);await page.close();
 }
 console.log('PASS: exact explicit parent, two contexts, visited trails, ancestor trim, direct/reload/Back/Forward and invalid fallback at four viewports. Local production-template fixture only.');
}finally{await browser.close();}})().catch(error=>{console.error(error);process.exitCode=1});
