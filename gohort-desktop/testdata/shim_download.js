// Runs popup_shim.js against a minimal fake page and clicks two download links:
// a same-origin URL that succeeds and one the server refuses. Invoked by
// TestPopupShimSavesURLDownloads with the shim's path as the only argument.
const fs=require('fs');
const src=fs.readFileSync(process.argv[2],'utf8');
const clickHandlers=[]; const posted=[]; const toasts=[];
function el(){const o={style:{},setAttribute(){},appendChild(){},remove(){},addEventListener(){},querySelectorAll(){return []},classList:{add(){},remove(){}}};Object.defineProperty(o,'textContent',{set(v){toasts.push(String(v));},get(){return ''}});return o;}
global.window=global; global.location={href:'http://127.0.0.1:9999/knowledge/c/x',origin:'http://127.0.0.1:9999',reload(){}};
global.navigator={clipboard:null,userAgent:'x'};
global.requestAnimationFrame=f=>f(); 
global.MutationObserver=function(){this.observe=()=>{};};
global.document={readyState:'complete',body:el(),documentElement:el(),head:el(),createElement:el,getElementById(){return null},querySelector(){return null},querySelectorAll(){return []},
  addEventListener(t,f){if(t==='click')clickHandlers.push(f);},removeEventListener(){}};
global.FileReader=function(){const self=this;this.readAsDataURL=function(b){self.result='data:'+b.type+';base64,'+Buffer.from(b.text).toString('base64');setTimeout(()=>self.onload(),0);};};
global.fetch=function(url,opts){
  if(url==='/__desktop/save'){posted.push(JSON.parse(opts.body));return Promise.resolve({json:()=>Promise.resolve({path:'/tmp/x'})});}
  if(url==='/orchestrate/api/collections/c1/export'){return Promise.resolve({ok:true,headers:{get:h=>h==='Content-Disposition'?'attachment; filename="Runbooks.gohort.json"':null},blob:()=>Promise.resolve({type:'application/json',text:'{"bundle":"gohort.bundle/v1"}'})});}
  if(url==='/bad'){return Promise.resolve({ok:false,status:403,text:()=>Promise.resolve('only the owner can export')})}
  return Promise.resolve({ok:true,json:()=>Promise.resolve({}),text:()=>Promise.resolve('')});
};
global.setInterval=()=>0;
const origSetTimeout=setTimeout;
global.console={log(){},warn(){},error:console.error};
try{ eval(src); }catch(e){ console.error('SHIM THREW', e); process.exit(1); }
function click(href,dl){const a={getAttribute:n=>n==='href'?href:(n==='download'?dl:null)};let prevented=false;
  const ev={target:{closest:sel=>sel.indexOf('download')>=0?a:null},preventDefault(){prevented=true},stopPropagation(){}};
  clickHandlers.forEach(h=>{try{h(ev)}catch(e){}}); return prevented;}
const p1=click('/orchestrate/api/collections/c1/export','');
const p2=click('/bad','');
origSetTimeout(()=>{
  const out={sameOriginIntercepted:p1, badIntercepted:p2, posted, toasts};
  process.stdout.write(JSON.stringify(out,null,1)+'\n');
  const ok=p1&&posted.length===1&&posted[0].name==='Runbooks.gohort.json'&&Buffer.from(posted[0].b64,'base64').toString().includes('gohort.bundle')&&toasts.some(t=>t.includes('only the owner can export'));
  console.error(ok?'PASS':'FAIL'); process.exit(ok?0:1);
},50);
