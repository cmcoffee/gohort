package customapps

import (
	"github.com/cmcoffee/gohort/core/apppage"
	"github.com/cmcoffee/gohort/core/editor"
)

// PageAPI is what window.app offers a page, one call a line: the reference
// an agent writing a page reads (Anvil's app projects, Builder's help). It
// lives in core/apppage so orchestrate can name it without importing this
// package; the helper it describes is here, and a test holds the two equal.
const PageAPI = apppage.API

// PageWork is how a page treats work that outlives it (apppage.Work).
const PageWork = apppage.Work

// The page side of an app: one small object, window.app, put at the top of
// every frame an app's page renders in.
//
// An app is a web page and a Python backend: the page draws and calls its
// endpoints; the endpoints (data sources and actions) do the work and reach
// gohort. Pages wrote that plumbing by hand, and the same bugs came back:
// absolute paths the frame refuses, endpoint names spelled differently from
// the saved ones, a polling loop written for every live page, a refresh that
// never fired. window.app is that plumbing written once; what it offers is
// PageAPI, above.
//
// localStorage in the page is the person's own and survives a reload: the
// frame's stand-in for it is kept on the server per person and per app
// (view_state.go), for where they were in the app, not its data.
//
// The three dialogs are gohort's own, shown by the page the app sits in, so
// a question an app asks looks like every other question gohort asks. The
// browser's confirm() and prompt() are not these: they block the page, look
// foreign, and a frame's are shown by the browser alone.
//
// Every call is relative, so it reaches only this app, and a failed one
// rejects with the server's own message.
//
// An error the page does not catch is told to the app's run record
// (problems.go), a few per load and each once, so whoever fixes the app
// reads what broke in someone's browser. One in the first seconds of the
// frame's load is also told to the page around it, which offers the person
// Reset view when they have one (viewTroubleJS).
const appPageHelper = `<script>(function(){if(window.app)return;
(function(){var n=0,seen={},t0=Date.now(),said=false;function loading(){if(said||Date.now()-t0>8000)return;said=true;try{parent.postMessage({gohortApp:"load-error"},"*");}catch(x){}}
function tell(m,w){m=String(m||"").slice(0,500);if(m)loading();if(!m||m==="Script error."||n>=5)return;var k=m+"|"+(w||"");if(seen[k])return;seen[k]=1;n++;try{fetch("problem",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({error:m,where:String(w||"").slice(0,200)})}).catch(function(){});}catch(x){}}
if(!window.addEventListener)return;
window.addEventListener("error",function(e){if(!e||(e.target&&e.target!==window))return;tell(e.message||(e.error&&e.error.message),e.lineno?"line "+e.lineno+(e.colno?":"+e.colno:""):"");});
window.addEventListener("unhandledrejection",function(e){var r=e&&e.reason;tell("unhandled promise rejection: "+((r&&r.message)||r),"");});})();
function q(p){var s=[];if(p)for(var k in p)if(p[k]!=null)s.push(encodeURIComponent(k)+"="+encodeURIComponent(p[k]));return s.length?"?"+s.join("&"):"";}
function json(r){return r.text().then(function(t){var d=null;try{d=t?JSON.parse(t):null;}catch(e){}if(!r.ok)throw new Error((d&&(d.error||d.message))||(t&&t.trim())||("HTTP "+r.status));return d;});}
function post(u,b){return fetch(u,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(b||{})}).then(json);}
function hear(o,p){if(p&&o&&o.onProgress){try{o.onProgress(p);}catch(x){console.error(x);}}}
function outcome(s,k){if(s.error)throw new Error(s.error);if(k==="ask")return s.result&&s.result.text;return {message:s.message,saved:s.saved,records:s.records,result:s.result};}
function follow(u,o,k){return new Promise(function(res,rej){(function poll(){setTimeout(function(){fetch(u).then(json).then(function(s){
if(s&&s.done){try{res(outcome(s,k));}catch(x){rej(x);}return;}
if(!s||!s.running){rej(new Error("the "+(k==="ask"?"ask":"action")+" is no longer running and left no outcome"));return;}
hear(o,s.progress);poll();}).catch(function(){poll();});},2000);})();});}
window.app={
data:function(n,p){return fetch("data/"+encodeURIComponent(n)+q(p)).then(json);},
action:function(n,b,o){o=o||{};var e=encodeURIComponent(n);
return post("action/"+e,b).then(function(d){if(!d||!d.running||!d.job)return d;hear(o,d.progress);return follow("action-progress/"+e+q({job:d.job}),o,"action");});},
work:function(){return fetch("work").then(json).then(function(d){return (d&&d.items)||[];});},
follow:function(job,o){o=o||{};return fetch("work/"+encodeURIComponent(job)).then(json).then(function(s){if(!s||(!s.running&&!s.done))throw new Error("no such work: it ended too long ago, or is not yours");
var k=s.kind==="ask"?"ask":"action";if(s.done)return outcome(s,k);hear(o,s.progress);return follow("work/"+encodeURIComponent(job),o,k);});},
cancelAction:function(n){return post("action-cancel/"+encodeURIComponent(n));},
records:{list:function(){return fetch("records").then(json);},save:function(r){return post("records",r);},remove:function(id){return fetch("record?id="+encodeURIComponent(id),{method:"DELETE"}).then(json);}},
shared:function(n){return fetch("shared/"+encodeURIComponent(n)).then(json);},
ask:function(p,o){o=o||{};return post("ask",{prompt:String(p),json:!!o.json}).then(function(d){if(d&&d.running&&d.job){hear(o,d.progress);return follow("work/"+encodeURIComponent(d.job),o,"ask");}return d&&d.text;});},
propose:function(d,p,o){o=o||{};return post("propose",{document:String(d==null?"":d),prompt:String(p),selection:o.selection||null,history:o.history||[]});},
asset:function(n){return "assets/"+n;},
files:{list:function(id){return fetch("files"+q({id:id})).then(json).then(function(d){return (d&&d.files)||[];});},url:function(id,n){return "file"+q({id:id,name:n});},remove:function(id,n){return fetch("file"+q({id:id,name:n}),{method:"DELETE"}).then(json);},add:function(id,f,t){return fetch("file"+q({id:id,name:f&&f.name,title:t}),{method:"POST",body:f}).then(json);}},
confirm:function(m){return window.uiConfirm(m);},alert:function(m){return window.uiAlert(m);},prompt:function(m,d){return window.uiPrompt(m,d);},
onChange:function(fn){var v=null,stop=false;
(function loop(){if(stop)return;var s=v||{shared:"",records:""};
fetch("changes"+q({shared:s.shared,records:s.records})).then(json).then(function(d){if(stop||!d)return;var moved=v&&(d.shared!==v.shared||d.records!==v.records);v={shared:d.shared||"",records:d.records||""};if(moved){try{fn(d);}catch(e){console.error(e);}}loop();}).catch(function(){if(!stop)setTimeout(loop,3000);});})();
return function(){stop=true;};}
};})();</script>`

// coauthor: a document the person writes with the app's agent, the same one
// the framework's own editors are (core/editor's coauthor.js and its diff,
// loaded into the frame below), so an app that designs its own page still
// gets the tested highlight, review and apply rather than writing them.
//
//	var doc = app.coauthor(textarea, {chip, onSelect, setValue})
//	  chip      an element (the page's ask box) the kit shows "Highlighted:
//	            ..." in, with a button that lets the passage go
//	  onSelect  fn(sel or null) when the highlighted passage changes
//	  setValue  fn(text) writing the document; default sets the textarea and
//	            fires input, so the page's own save sees an applied change
//	doc.ask(prompt, {history})  sends the document, the highlighted passage
//	            and the request to the agent; its change opens in the
//	            document's place to Apply or Reject. Resolves {reply,
//	            proposed, reason}
//	doc.selection()  the highlighted passage {text, start, end}, or null; it
//	            stays while the person types in the ask box
//	doc.propose({find, replace, summary})  review a change the page has
//	doc.clear(), doc.destroy()
//
// The kit is the page's own code with the page's own reach: everything it
// does that matters is checked on the server (handlePropose).
const appCoauthorJS = `(function(){var app=window.app;if(!app||app.coauthor||typeof coauthorTextDoc!=="function")return;
app.coauthor=function(ta,o){o=o||{};if(!ta||ta.tagName!=="TEXTAREA")throw new Error("app.coauthor takes a <textarea>");
var chip=null,d;
function showChip(sel){if(!o.chip)return;
if(!sel){if(chip&&chip.parentNode)chip.parentNode.removeChild(chip);chip=null;return;}
if(!chip){chip=coauthorEl("div",{"class":"app-coauthor-chip"});o.chip.insertBefore(chip,o.chip.firstChild);}
chip.innerHTML="";
chip.appendChild(coauthorEl("button",{type:"button","class":"app-coauthor-chip-x",title:"Forget the highlighted passage","aria-label":"Forget the highlighted passage",onclick:function(){d.unpin();}},["\u00d7"]));
chip.appendChild(coauthorEl("span",{"class":"app-coauthor-chip-label"},["Highlighted: "+coauthorLabel(sel.text)]));}
d=coauthorTextDoc(ta,{setValue:o.setValue,showDocument:o.showDocument,onSelect:function(sel){showChip(sel);if(o.onSelect){try{o.onSelect(sel);}catch(e){console.error(e);}}}});
function sel(){var s=d.selection();return s?{text:s.text,start:s.start,end:s.end}:null;}
return{selection:sel,clear:function(){d.unpin();},propose:d.propose,
ask:function(prompt,ao){ao=ao||{};return app.propose(ta.value,prompt,{selection:sel(),history:ao.history}).then(function(r){r=r||{};
var out={reply:r.reply||"",proposed:false,reason:r.reason||""};
if(r.edit){var res=d.propose(r.edit);out.proposed=!!(res&&res.ok);if(!out.proposed)out.reason=(res&&res.reason)||out.reason;}
return out;});},
destroy:function(){showChip(null);d.destroy();}};};})();`

// appCoauthorCSS is the frame's own look for the kit: the runtime's buttons
// are not there, and the chip is the kit's.
const appCoauthorCSS = `.ui-coauthor-review .ui-row-btn,.ui-coauthor-undo .ui-row-btn{font:inherit;font-size:0.85em;padding:0.25rem 0.7rem;border-radius:6px;border:1px solid #30363d;background:#21262d;color:#c9d1d9;cursor:pointer}
.ui-coauthor-review .ui-coauthor-apply{background:#7c83ff;border-color:#7c83ff;color:#fff}
.ui-coauthor-review .ui-row-btn:disabled{opacity:0.5;cursor:default}
.app-coauthor-chip{display:flex;align-items:center;gap:0.4rem;min-width:0;margin:0 0 0.3rem;padding:0.25rem 0.45rem;font-size:0.85em;border-left:3px solid #7c83ff;border-radius:6px;background:rgba(124,131,255,0.12)}
.app-coauthor-chip-label{flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:clip;animation:ui-cut-fade-x linear both;animation-timeline:scroll(self inline);white-space:nowrap}
.app-coauthor-chip-x{flex:0 0 auto;padding:0 0.2rem;font:inherit;color:inherit;background:none;border:0;cursor:pointer}`

// appPagePrelude is the markup at the top of every frame an app's page
// renders in: window.app, and the co-authored document with its diff.
var appPagePrelude = appPageHelper +
	"<style>" + editor.DiffCSS() + editor.CoauthorCSS() + appCoauthorCSS + "</style>" +
	"<script>" + editor.DiffJS() + "\n" + editor.CoauthorJS() + "\n" + appCoauthorJS + "</script>"
