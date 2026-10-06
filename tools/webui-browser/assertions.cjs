'use strict';
const assert=require('node:assert/strict');
// Evaluate the rendered foreground against the first opaque ancestor surface.
async function readable(page,selector){
 const pairs=await page.locator(selector).evaluateAll(elements=>{
  const luminance=color=>{const c=color.match(/[\d.]+/g).slice(0,3).map(Number).map(v=>{v/=255;return v<=0.04045?v/12.92:((v+0.055)/1.055)**2.4;});return c[0]*0.2126+c[1]*0.7152+c[2]*0.0722;};
  return elements.filter(e=>e.getClientRects().length && e.textContent.trim()).map(e=>{
   let ancestor=e,background;
   while(ancestor){background=getComputedStyle(ancestor).backgroundColor;if(background!=='rgba(0, 0, 0, 0)'&&background!=='transparent')break;ancestor=ancestor.parentElement;}
   const foreground=getComputedStyle(e).color,light=luminance(foreground),dark=luminance(background||'rgb(255, 255, 255)');
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
module.exports={readable,bounded};
