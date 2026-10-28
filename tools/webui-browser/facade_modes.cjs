'use strict';
const assert = require('node:assert/strict');
const {chromium} = require(process.env.KNOWL_PLAYWRIGHT_MODULE || 'playwright');

(async () => {
  const browser = await chromium.launch({headless:true,args:['--no-sandbox']});
  try {
    for (const [mode, origin] of [['public',process.env.KNOWL_BROWSER_PUBLIC_URL],['proxy',process.env.KNOWL_BROWSER_PROXY_URL]]) {
      const page = await browser.newPage();
      const requests=[];
      page.on('request',request=>requests.push({url:request.url(),headers:request.headers()}));
      await page.goto(origin+'/ui/knowledge');
      await page.locator('#mode-content').waitFor();
      assert.equal(await page.locator('body').getAttribute('data-auth-mode'),mode);
      assert.equal(await page.locator('body').getAttribute('data-connection-state'),'connected');
      assert.equal(await page.locator('#connection-card').isVisible(),false);
      assert.ok(requests.every(request=>!request.headers.authorization));
      if(mode==='proxy') {
        const reconnect=async()=>{
          const navigation=page.waitForRequest(request=>request.isNavigationRequest() && request.frame()===page.mainFrame());
          await page.locator('#connect-form button').click();
          const target=new URL((await navigation).url());
          assert.equal(target.origin,origin);
          assert.equal(target.pathname,'/ui/knowledge');
          await page.locator('#mode-content').waitFor();
        };
        const rejected=async(path,selector)=>{
          await page.evaluate(path=>htmx.ajax('GET',path,{target:'#screen',swap:'innerHTML'}).catch(()=>{}),path);
          await page.waitForFunction(()=>document.body.dataset.connectionState==='disconnected');
          assert.equal(await page.locator(selector).count(),0);
          assert.match(await page.locator('#connection-feedback').textContent(),/identity provider/);
          await reconnect();
        };
        await page.route('**/ui/fragments/knowledge',route=>route.fulfill({status:200,contentType:'text/html',body:'<script>window.proxyPwned=true</script><p id="untrusted-login">login</p>'}),{times:1});
        await rejected('/ui/fragments/knowledge','#untrusted-login,#screen script');
        assert.equal(await page.evaluate(()=>window.proxyPwned),undefined);
        await rejected('/ui/fragments/knowledge?case=location','#redirected-login');
        await rejected('/ui/fragments/knowledge?case=same-origin-redirect','#redirected-login');
        await rejected('/ui/fragments/knowledge?case=cross-origin-redirect','#cross-origin-login');
      }
      await page.close();
    }
    console.log('PASS: public and proxy modes auto-connect without browser credentials; proxy failures use fixed-origin reconnect');
  } finally {
    await browser.close();
  }
})().catch(error=>{console.error(error);process.exitCode=1;});
