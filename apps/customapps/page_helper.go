package customapps

// The page side of an app: one small object, window.app, put at the top of
// every frame an app's page renders in.
//
// An app is a web page and a Python backend: the page draws and calls its
// endpoints; the endpoints (data sources and actions) do the work and reach
// oddjob. Pages wrote that plumbing by hand, and the same bugs came back:
// absolute paths the frame refuses, endpoint names spelled differently from
// the saved ones, a polling loop written for every live page, a refresh that
// never fired. window.app is that plumbing written once:
//
//	app.data(name, params)        GET a data source, its JSON
//	app.action(name, body)        POST an action, {message, saved, records, result}
//	app.records.list()            this person's records, oldest first
//	app.records.save(record)      create, or update by the record key
//	app.records.remove(id)        delete one
//	app.shared(name)              a shared collection's records
//	app.ask(prompt, {json})       a question to the app's agent, its text
//	app.asset(name)               the URL of one of the app's assets
//	app.onChange(fn)              fn() whenever records or shared data change
//	                              (anyone's), until the returned stop() is called
//	app.confirm(msg)              a question, true or false
//	app.alert(msg)                a notice, resolved when read
//	app.prompt(msg, default)      a line of text, or null
//
// The three dialogs are oddjob's own, shown by the page the app sits in, so
// a question an app asks looks like every other question oddjob asks. The
// browser's confirm() and prompt() are not these: they block the page, look
// foreign, and a frame's are shown by the browser alone.
//
// Every call is relative, so it reaches only this app, and a failed one
// rejects with the server's own message.
const appPageHelper = `<script>(function(){if(window.app)return;
function q(p){var s=[];if(p)for(var k in p)if(p[k]!=null)s.push(encodeURIComponent(k)+"="+encodeURIComponent(p[k]));return s.length?"?"+s.join("&"):"";}
function json(r){return r.text().then(function(t){var d=null;try{d=t?JSON.parse(t):null;}catch(e){}if(!r.ok)throw new Error((d&&(d.error||d.message))||(t&&t.trim())||("HTTP "+r.status));return d;});}
function post(u,b){return fetch(u,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(b||{})}).then(json);}
window.app={
data:function(n,p){return fetch("data/"+encodeURIComponent(n)+q(p)).then(json);},
action:function(n,b){return post("action/"+encodeURIComponent(n),b);},
records:{list:function(){return fetch("records").then(json);},save:function(r){return post("records",r);},remove:function(id){return fetch("record?id="+encodeURIComponent(id),{method:"DELETE"}).then(json);}},
shared:function(n){return fetch("shared/"+encodeURIComponent(n)).then(json);},
ask:function(p,o){return post("ask",{prompt:String(p),json:!!(o&&o.json)}).then(function(d){return d&&d.text;});},
asset:function(n){return "assets/"+n;},
confirm:function(m){return window.uiConfirm(m);},alert:function(m){return window.uiAlert(m);},prompt:function(m,d){return window.uiPrompt(m,d);},
onChange:function(fn){var v=null,stop=false;
(function loop(){if(stop)return;var s=v||{shared:"",records:""};
fetch("changes"+q({shared:s.shared,records:s.records})).then(json).then(function(d){if(stop||!d)return;var moved=v&&(d.shared!==v.shared||d.records!==v.records);v={shared:d.shared||"",records:d.records||""};if(moved){try{fn(d);}catch(e){console.error(e);}}loop();}).catch(function(){if(!stop)setTimeout(loop,3000);});})();
return function(){stop=true;};}
};})();</script>`
