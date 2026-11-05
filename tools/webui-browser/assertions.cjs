'use strict';
const assert=require('node:assert/strict');
// Evaluate the rendered foreground against the composited ancestor surfaces, including native translucent active rows.
async function readable(page,selector){
 const pairs=await page.locator(selector).evaluateAll(elements=>{
  const rgba=color=>{const values=color.match(/[\d.]+/g)?.map(Number)||[0,0,0,0];return [...values.slice(0,3),values[3]??1];};
  const over=(front,back)=>front.slice(0,3).map((value,i)=>value*front[3]+back[i]*(1-front[3]));
  const luminance=color=>{const c=color.map(v=>{v/=255;return v<=0.04045?v/12.92:((v+0.055)/1.055)**2.4;});return c[0]*0.2126+c[1]*0.7152+c[2]*0.0722;};
  return elements.filter(e=>e.getClientRects().length && e.textContent.trim()).map(e=>{
   const layers=[];
   for(let ancestor=e;ancestor;ancestor=ancestor.parentElement)layers.push(rgba(getComputedStyle(ancestor).backgroundColor));
   let background=[255,255,255];
   for(const layer of layers.reverse())background=over(layer,background);
   const foreground=getComputedStyle(e).color,light=luminance(over(rgba(foreground),background)),dark=luminance(background);
   return {element:e.className||e.id||e.tagName,foreground,background,ratio:(Math.max(light,dark)+0.05)/(Math.min(light,dark)+0.05)};
  });
 });
 assert.ok(pairs.length>0,'contrast check must inspect visible rendered copy');
 for(const pair of pairs)assert.ok(pair.ratio>=4.5,JSON.stringify(pair));
}
async function bounded(promise,label){
 let timer;
 try{return await Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error(label)),5000);})]);}
 finally{clearTimeout(timer);}
}
// Native drawer classes can settle before inherited visibility transitions on a
// keyboard target. Focus does not wait for actionability, so observe it first.
async function keyboardEnter(page,target){
 await target.waitFor({state:'visible'});
 await target.focus();
 assert.equal(await target.evaluate(node=>document.activeElement===node),true,'keyboard target must own focus before Enter');
 await page.keyboard.press('Enter');
}
// Follow only the requested page's known structural path through loaded rows.
// Groups and real index pages are both navigation ancestors; neither is flattened.
async function wikiPageNode(page,id,settled){
 await page.locator('#wiki-tree [data-wiki-pages=""]').waitFor({state:'attached'});
 const node=page.locator('#wiki-tree [data-page-node='+JSON.stringify('page:'+id)+']');
 const parts=id.split('/');
 for(let i=1;i<parts.length && !(await node.isVisible());i++){
  const directory=parts.slice(0,i).join('/');
  const ancestor=page.locator('#wiki-tree [data-page-node='+JSON.stringify('group:'+directory)+'],#wiki-tree [data-page-node='+JSON.stringify('page:'+directory+'/index')+']');
  assert.equal(await ancestor.count(),1,'known structural ancestor must be present: '+directory);
  const caret=ancestor.locator(':scope > [data-page-toggle]');
  assert.equal(await caret.count(),1,'known structural ancestor must have actual children: '+directory);
  if(await caret.getAttribute('aria-expanded')==='false'){
   await caret.click();
   if(settled)await settled(page);
   else await page.waitForFunction(()=>!document.querySelector('#wiki-tree [aria-busy="true"],[data-wiki-stage]'));
  }
 }
 await node.waitFor();return node;
}
module.exports={readable,bounded,keyboardEnter,wikiPageNode};
