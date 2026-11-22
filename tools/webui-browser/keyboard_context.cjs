'use strict';
const assert=require('node:assert/strict');
const {chromium}=require(process.env.KNOWL_PLAYWRIGHT_MODULE||'playwright');
const {keyboardEnter}=require('./assertions.cjs');

(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 try {
  for(const width of [1024,390,320]) {
   const page=await browser.newPage({viewport:{width,height:900},reducedMotion:'reduce'});
   page.setDefaultTimeout(5000);
   try {
    await page.goto(process.env.KNOWL_BROWSER_URL+'/ui/knowledge?page_id=AI%2FPrism');
    await page.locator('#operator-token').fill('browser-secret-token');
    await page.locator('#connect-form button').click();
    await page.waitForFunction(()=>document.body.dataset.connectionState==='connected'&&!document.querySelector('#screen.htmx-settling,#screen .htmx-settling'));
    await page.locator('#outline a').first().waitFor({state:'hidden'});
    const timeline=await page.context().newCDPSession(page);
    await timeline.send('Animation.enable');
    await timeline.send('Animation.setPlaybackRate',{playbackRate:0});
    await page.locator('#context-toggle').click();
    await page.waitForFunction(()=>{const panel=document.getElementById('page-context');return panel.classList.contains('show')&&!panel.classList.contains('showing');});
    await page.evaluate(()=>{
     const anchor=document.querySelector('#outline a');
     for(const node of document.querySelectorAll('#page-context *'))getComputedStyle(node).visibility;
     // Complete sibling transitions so the real tab button is actionable while
     // the actual outline transition remains on its hidden first frame.
     for(const node of document.querySelectorAll('#page-context *'))if(node!==anchor)for(const animation of node.getAnimations())animation.finish();
    });
    await page.locator('#contents-tab').click();
    await page.waitForFunction(()=>document.getElementById('contents-tab').getAttribute('aria-selected')==='true');
    const heading=page.locator('#outline a').first(),href=await heading.getAttribute('href');
    // Hold the browser's real inherited visibility transition at its first
    // frame. The native drawer is already shown; no CSS or app event is mocked.
    const activation=keyboardEnter(page,heading);
    await page.waitForFunction(()=>document.querySelector('#outline a').getAnimations().some(animation=>animation.transitionProperty==='visibility'&&animation.currentTime===0));
    const held=await heading.evaluate(node=>({
     visibility:getComputedStyle(node).visibility,
     active:document.activeElement.id,
     panel:document.getElementById('page-context').classList.contains('show'),
     transitions:node.getAnimations().filter(animation=>animation.transitionProperty==='visibility').map(animation=>animation.effect.getKeyframes().map(frame=>frame.visibility)),
    }));
    assert.deepEqual(held,{visibility:'hidden',active:'contents-tab',panel:true,transitions:[['hidden','visible']]});
    await timeline.send('Animation.setPlaybackRate',{playbackRate:1});
    await activation;
    assert.equal(await page.evaluate(id=>document.activeElement.id===id,decodeURIComponent(href.slice(1))),true,'Enter must focus the actual rendered heading');
    assert.equal(new URL(page.url()).hash,href);
    await page.locator('#page-context').waitFor({state:'hidden'});
    await page.locator('#context-toggle').click();
    await page.locator('#sources-tab').click();
    await page.getByRole('button',{name:'Read saved source',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('#raw-source pre')&&!document.getElementById('raw-source').hasAttribute('aria-busy'));
    assert.equal(await page.locator('#raw-source pre').textContent(),'Original accepted Prism source.\n');
    await page.getByRole('button',{name:'Close saved source',exact:true}).click();
    assert.equal(await page.locator('#raw-source').textContent(),'');
   } finally {await page.close();}
  }
  console.log('PASS: native reduced-motion visibility readiness, keyboard heading focus/hash/drawer closure and immutable source at 1024/390/320');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
