(function(){function __desktop_modal_open(opts){return new Promise(function(resolve){try{var overlay=document.createElement('div');overlay.setAttribute('data-gohort-modal','1');overlay.style.position='fixed';overlay.style.top='0';overlay.style.left='0';overlay.style.right='0';overlay.style.bottom='0';overlay.style.width='100vw';overlay.style.height='100vh';overlay.style.zIndex='2147483646';overlay.style.display='flex';overlay.style.alignItems='center';overlay.style.justifyContent='center';overlay.style.background='rgba(0,0,0,0.55)';overlay.style.font='14px -apple-system,system-ui,sans-serif';var card=document.createElement('div');card.style.minWidth='340px';card.style.maxWidth='520px';card.style.padding='20px 22px';card.style.background='#161b22';card.style.color='#e6edf3';card.style.border='1px solid #30363d';card.style.borderRadius='10px';card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';var msg;if(opts._customBody){msg=opts._customBody;}else{msg=document.createElement('div');msg.textContent=opts.msg||'';}msg.style.marginBottom='18px';msg.style.lineHeight='1.5';msg.style.whiteSpace=opts._customBody?'normal':'pre-wrap';msg.style.wordBreak='break-word';var inp=null;if(opts.kind==='prompt'){inp=document.createElement('input');inp.type='text';inp.value=(opts.value!=null?opts.value:'');inp.style.width='100%';inp.style.boxSizing='border-box';inp.style.marginBottom='14px';inp.style.padding='7px 10px';inp.style.borderRadius='6px';inp.style.border='1px solid #30363d';inp.style.background='#0d1117';inp.style.color='#e6edf3';inp.style.font='13px -apple-system,system-ui,sans-serif';}var actions=document.createElement('div');actions.style.display='flex';actions.style.justifyContent='flex-end';actions.style.gap='8px';function done(v){document.removeEventListener('keydown',on_key,true);if(overlay.parentNode)overlay.parentNode.removeChild(overlay);resolve(v);}function mk_btn(label,primary,value){var b=document.createElement('button');b.textContent=label;b.style.padding='7px 16px';b.style.borderRadius='6px';b.style.cursor='pointer';b.style.font='13px -apple-system,system-ui,sans-serif';b.style.border='1px solid '+(primary?'#3a6ea5':'#30363d');b.style.background=primary?'#3a6ea5':'#21262d';b.style.color=primary?'#fff':'#c9d1d9';b.onclick=function(){done(value);};return b;}if(opts._buttons&&opts._buttons.length){opts._buttons.forEach(function(bspec){actions.appendChild(mk_btn(bspec.label,!!bspec.primary,bspec.value));});}else if(opts.kind==='confirm'){actions.appendChild(mk_btn('Cancel',false,false));actions.appendChild(mk_btn(opts.ok||'OK',true,true));}else if(opts.kind==='prompt'){actions.appendChild(mk_btn('Cancel',false,null));var okb=mk_btn(opts.ok||'OK',true,null);okb.onclick=function(){done(inp?inp.value:null);};actions.appendChild(okb);}else{actions.appendChild(mk_btn('OK',true,undefined));}card.appendChild(msg);if(inp)card.appendChild(inp);card.appendChild(actions);overlay.appendChild(card);function on_key(ev){if(opts._buttons&&opts._buttons.length){if(ev.key==='Escape'){ev.preventDefault();done(opts._escapeValue);}return;}if(ev.key==='Escape'){ev.preventDefault();done(opts.kind==='confirm'?false:(opts.kind==='prompt'?null:undefined));}else if(ev.key==='Enter'){ev.preventDefault();done(opts.kind==='confirm'?true:(opts.kind==='prompt'?(inp?inp.value:null):undefined));}}document.addEventListener('keydown',on_key,true);(document.body||document.documentElement).appendChild(overlay);if(inp){inp.focus();try{inp.select();}catch(e){}}else{var btns=actions.querySelectorAll('button');if(btns.length)btns[btns.length-1].focus();}console.log('[gohort-desktop] modal open:',opts.kind,opts.msg);}catch(err){console.error('[gohort-desktop] modal error:',err);resolve(opts.kind==='confirm'?true:(opts.kind==='prompt'?null:undefined));}});}window.__uiConfirmImpl=function(msg){return __desktop_modal_open({kind:'confirm',msg:msg});};window.__uiAlertImpl=function(msg){return __desktop_modal_open({kind:'alert',msg:msg});};window.__uiPromptImpl=function(msg,def){return __desktop_modal_open({kind:'prompt',msg:msg,value:def});};window.__uiClipboardImpl=function(text){if(window.runtime&&window.runtime.ClipboardSetText){try{var r=window.runtime.ClipboardSetText(text||'');return (r&&typeof r.then==='function')?r:Promise.resolve(true);}catch(e){return Promise.reject(e);}}return Promise.reject(new Error('Wails clipboard runtime unavailable'));};function __desktop_pretty(args){try{return JSON.stringify(args,null,2);}catch(e){return String(args);}}function __desktop_approval_open(req){var msg=document.createElement('div');var head=document.createElement('div');head.textContent='An agent on the server is asking to run a local tool:';head.style.cssText='margin-bottom:12px;';msg.appendChild(head);var nameRow=document.createElement('div');nameRow.style.cssText='font-family:ui-monospace,Menlo,monospace;font-size:13px;background:#0d1117;padding:8px 10px;border-radius:6px;margin-bottom:10px;border:1px solid #30363d;color:#79c0ff;';nameRow.textContent=req.name||'(unnamed)';msg.appendChild(nameRow);var argsLabel=document.createElement('div');argsLabel.textContent='Arguments:';argsLabel.style.cssText='font-size:12px;color:#8b949e;margin-bottom:4px;';msg.appendChild(argsLabel);var argsBox=document.createElement('pre');argsBox.style.cssText='font-family:ui-monospace,Menlo,monospace;font-size:12px;background:#0d1117;padding:8px 10px;border-radius:6px;margin:0;border:1px solid #30363d;color:#c9d1d9;max-height:200px;overflow:auto;white-space:pre-wrap;word-break:break-word;';argsBox.textContent=__desktop_pretty(req.args||{});msg.appendChild(argsBox);var toolLabel=(req.name||'this tool');__desktop_modal_open({_customBody:msg,_escapeValue:'deny',_buttons:[{label:'Deny',primary:false,value:'deny'},{label:'Allow once',primary:false,value:'once'},{label:'Always allow '+toolLabel,primary:true,value:'always'}]}).then(function(choice){var allow=(choice==='once'||choice==='always');var always=(choice==='always');if(window.go&&window.go.main&&window.go.main.App&&window.go.main.App.ApproveInvoke){window.go.main.App.ApproveInvoke(req.id||'',!!allow,!!always);}});}if(window.runtime&&window.runtime.EventsOn){window.runtime.EventsOn('bridge-approval',function(req){__desktop_approval_open(req);});console.log('[gohort-desktop] bridge approval listener installed');}function __desktop_logs_open(initial){var prev=document.getElementById('__desktop_logs');if(prev){prev.remove();}var overlay=document.createElement('div');overlay.id='__desktop_logs';overlay.style.position='fixed';overlay.style.top='0';overlay.style.left='0';overlay.style.right='0';overlay.style.bottom='0';overlay.style.zIndex='2147483645';overlay.style.background='rgba(0,0,0,0.7)';overlay.style.display='flex';overlay.style.alignItems='center';overlay.style.justifyContent='center';overlay.style.font='13px -apple-system,system-ui,sans-serif';var card=document.createElement('div');card.style.width='min(900px,92vw)';card.style.height='min(70vh,720px)';card.style.background='#0d1117';card.style.color='#c9d1d9';card.style.border='1px solid #30363d';card.style.borderRadius='10px';card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';card.style.display='flex';card.style.flexDirection='column';overlay.appendChild(card);var head=document.createElement('div');head.style.padding='10px 14px';head.style.borderBottom='1px solid #30363d';head.style.display='flex';head.style.alignItems='center';head.style.gap='8px';var title=document.createElement('div');title.textContent='Gohort Desktop — Logs';title.style.flex='1';title.style.fontWeight='600';head.appendChild(title);var copyBtn=document.createElement('button');copyBtn.textContent='Copy';copyBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #30363d;background:#21262d;color:#c9d1d9;';head.appendChild(copyBtn);var closeBtn=document.createElement('button');closeBtn.textContent='Close';closeBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #3a6ea5;background:#3a6ea5;color:#fff;';head.appendChild(closeBtn);card.appendChild(head);var body=document.createElement('pre');body.style.cssText='flex:1;margin:0;padding:10px 14px;overflow:auto;font:12px ui-monospace,Menlo,monospace;line-height:1.45;white-space:pre-wrap;word-break:break-word;background:#0d1117;color:#c9d1d9;';card.appendChild(body);function colorFor(level){if(level==='error'||level==='fatal')return '#ff7b72';if(level==='warn')return '#d29922';if(level==='notice')return '#79c0ff';return '#c9d1d9';}function render(lines){body.innerHTML='';lines.forEach(function(ln){var row=document.createElement('div');row.style.color=colorFor(ln.level);var t=ln.when?new Date(ln.when).toLocaleTimeString():'';row.textContent='['+(t)+' '+(ln.level||'').toUpperCase().padEnd(6)+'] '+(ln.text||'');body.appendChild(row);});body.scrollTop=body.scrollHeight;}function refresh(){if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.GetLogs)return;window.go.main.App.GetLogs().then(function(lines){render(lines||[]);});}if(initial&&initial.length){render(initial);}var pollId=setInterval(refresh,2000);refresh();function teardown(){clearInterval(pollId);overlay.remove();document.removeEventListener('keydown',onKey,true);}function onKey(e){if(e.key==='Escape'){e.preventDefault();teardown();}}document.addEventListener('keydown',onKey,true);closeBtn.onclick=teardown;copyBtn.onclick=function(){if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(body.innerText).then(function(){copyBtn.textContent='Copied';setTimeout(function(){copyBtn.textContent='Copy';},1500);});}};(document.body||document.documentElement).appendChild(overlay);}window.__desktop_logs_open=__desktop_logs_open;if(window.runtime&&window.runtime.EventsOn){window.runtime.EventsOn('show-logs',function(){__desktop_logs_open();});}function __desktop_folders_open(){var prev=document.getElementById('__desktop_folders');if(prev){prev.remove();}var overlay=document.createElement('div');overlay.id='__desktop_folders';overlay.style.position='fixed';overlay.style.top='0';overlay.style.left='0';overlay.style.right='0';overlay.style.bottom='0';overlay.style.zIndex='2147483645';overlay.style.background='rgba(0,0,0,0.7)';overlay.style.display='flex';overlay.style.alignItems='center';overlay.style.justifyContent='center';overlay.style.font='13px -apple-system,system-ui,sans-serif';var card=document.createElement('div');card.style.width='min(720px,92vw)';card.style.maxHeight='min(70vh,600px)';card.style.background='#0d1117';card.style.color='#c9d1d9';card.style.border='1px solid #30363d';card.style.borderRadius='10px';card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';card.style.display='flex';card.style.flexDirection='column';overlay.appendChild(card);var head=document.createElement('div');head.style.padding='10px 14px';head.style.borderBottom='1px solid #30363d';head.style.display='flex';head.style.alignItems='center';head.style.gap='8px';var title=document.createElement('div');title.textContent='Allowed Folders — gohort-desktop';title.style.flex='1';title.style.fontWeight='600';head.appendChild(title);var addBtn=document.createElement('button');addBtn.textContent='Add Folder…';addBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #2ea043;background:#238636;color:#fff;';head.appendChild(addBtn);var closeBtn=document.createElement('button');closeBtn.textContent='Close';closeBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #3a6ea5;background:#3a6ea5;color:#fff;';head.appendChild(closeBtn);card.appendChild(head);var help=document.createElement('div');help.textContent='Files and folders under these roots are exposed to gohort agents through the filesystem.* tools. Remove a root and the access goes away immediately.';help.style.cssText='padding:10px 14px;border-bottom:1px solid #30363d;color:#8b949e;font-size:12px;line-height:1.45;';card.appendChild(help);var listEl=document.createElement('div');listEl.style.cssText='flex:1;overflow:auto;padding:6px 8px;';card.appendChild(listEl);function refresh(){if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.GetReadRoots){return;}window.go.main.App.GetReadRoots().then(function(roots){listEl.innerHTML='';if(!roots||roots.length===0){var empty=document.createElement('div');empty.textContent='No folders allowed. Click Add Folder… to expose one.';empty.style.cssText='padding:18px 14px;color:#8b949e;text-align:center;';listEl.appendChild(empty);return;}roots.forEach(function(p){var row=document.createElement('div');row.style.cssText='display:flex;align-items:center;gap:8px;padding:8px 10px;border-bottom:1px solid #21262d;';var pathEl=document.createElement('div');pathEl.textContent=p;pathEl.style.cssText='flex:1;font:12px ui-monospace,Menlo,monospace;word-break:break-all;';row.appendChild(pathEl);var rm=document.createElement('button');rm.textContent='Remove';rm.style.cssText='padding:4px 10px;border-radius:5px;cursor:pointer;font:11px sans-serif;border:1px solid #4d2929;background:#3a1f1f;color:#ff9b9b;';rm.onclick=function(){rm.disabled=true;rm.textContent='Removing…';window.go.main.App.RemoveReadRoot(p).then(function(res){if(!res.ok){rm.disabled=false;rm.textContent='Remove';alert('Remove failed: '+(res.error||'unknown'));return;}refresh();});};row.appendChild(rm);listEl.appendChild(row);});});}addBtn.onclick=function(){if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.PickReadRoot){return;}addBtn.disabled=true;addBtn.textContent='Choose…';window.go.main.App.PickReadRoot().then(function(res){addBtn.disabled=false;addBtn.textContent='Add Folder…';if(res.error){alert('Add failed: '+res.error);return;}refresh();});};function teardown(){overlay.remove();document.removeEventListener('keydown',onKey,true);if(unsub){unsub();}}function onKey(e){if(e.key==='Escape'){e.preventDefault();teardown();}}document.addEventListener('keydown',onKey,true);closeBtn.onclick=teardown;var unsub=null;if(window.runtime&&window.runtime.EventsOn&&window.runtime.EventsOff){window.runtime.EventsOn('allowed-folders-changed',refresh);unsub=function(){window.runtime.EventsOff('allowed-folders-changed');};}(document.body||document.documentElement).appendChild(overlay);refresh();}if(window.runtime&&window.runtime.EventsOn){window.runtime.EventsOn('show-allowed-folders',function(){__desktop_folders_open();});}window.__desktop_tools_open=function(initial){var prev=document.getElementById('__desktop_tools');if(prev){prev.remove();}var overlay=document.createElement('div');overlay.id='__desktop_tools';overlay.style.position='fixed';overlay.style.top='0';overlay.style.left='0';overlay.style.right='0';overlay.style.bottom='0';overlay.style.zIndex='2147483645';overlay.style.background='rgba(0,0,0,0.7)';overlay.style.display='flex';overlay.style.alignItems='center';overlay.style.justifyContent='center';overlay.style.font='13px -apple-system,system-ui,sans-serif';var card=document.createElement('div');card.style.width='min(640px,92vw)';card.style.maxHeight='min(70vh,600px)';card.style.background='#0d1117';card.style.color='#c9d1d9';card.style.border='1px solid #30363d';card.style.borderRadius='10px';card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';card.style.display='flex';card.style.flexDirection='column';overlay.appendChild(card);var head=document.createElement('div');head.style.padding='10px 14px';head.style.borderBottom='1px solid #30363d';head.style.display='flex';head.style.alignItems='center';head.style.gap='8px';var title=document.createElement('div');title.textContent='Always-Allowed Tools — gohort-desktop';title.style.flex='1';title.style.fontWeight='600';head.appendChild(title);var closeBtn=document.createElement('button');closeBtn.textContent='Close';closeBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #3a6ea5;background:#3a6ea5;color:#fff;';head.appendChild(closeBtn);card.appendChild(head);var help=document.createElement('div');help.textContent='Tools you chose “Always allow” on. They run without prompting. Revoke one and it will ask again on its next call.';help.style.cssText='padding:10px 14px;border-bottom:1px solid #30363d;color:#8b949e;font-size:12px;line-height:1.45;';card.appendChild(help);var listEl=document.createElement('div');listEl.style.cssText='flex:1;overflow:auto;padding:6px 8px;';card.appendChild(listEl);function render(tools){listEl.innerHTML='';if(!tools||tools.length===0){var empty=document.createElement('div');empty.textContent='No always-allowed tools. Choose “Always allow” on an approval prompt to add one.';empty.style.cssText='padding:18px 14px;color:#8b949e;text-align:center;';listEl.appendChild(empty);return;}tools.forEach(function(name){var row=document.createElement('div');row.style.cssText='display:flex;align-items:center;gap:8px;padding:8px 10px;border-bottom:1px solid #21262d;';var nameEl=document.createElement('div');nameEl.textContent=name;nameEl.style.cssText='flex:1;font:12px ui-monospace,Menlo,monospace;word-break:break-all;color:#79c0ff;';row.appendChild(nameEl);var rm=document.createElement('button');rm.textContent='Revoke';rm.style.cssText='padding:4px 10px;border-radius:5px;cursor:pointer;font:11px sans-serif;border:1px solid #4d2929;background:#3a1f1f;color:#ff9b9b;';rm.onclick=function(){if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.RemoveAllowedTool){row.remove();return;}rm.disabled=true;rm.textContent='Revoking…';window.go.main.App.RemoveAllowedTool(name).then(function(res){if(res&&!res.ok){rm.disabled=false;rm.textContent='Revoke';return;}refresh();});};row.appendChild(rm);listEl.appendChild(row);});}function refresh(){if(window.go&&window.go.main&&window.go.main.App&&window.go.main.App.GetAllowedTools){window.go.main.App.GetAllowedTools().then(render);}}function teardown(){overlay.remove();document.removeEventListener('keydown',onKey,true);}function onKey(e){if(e.key==='Escape'){e.preventDefault();teardown();}}document.addEventListener('keydown',onKey,true);closeBtn.onclick=teardown;(document.body||document.documentElement).appendChild(overlay);render(initial||[]);refresh();};if(window.runtime&&window.runtime.EventsOn){window.runtime.EventsOn('show-tool-approvals',function(list){window.__desktop_tools_open(list||[]);});}console.log('[gohort-desktop] uiConfirm/uiAlert modal impl installed');window.confirm=function(msg){__desktop_toast('Confirmed: '+(msg||''));return true;};window.alert=function(msg){__desktop_toast(String(msg||''));};function __desktop_toast(text){var t=document.createElement('div');t.textContent=text;t.style.cssText='position:fixed;left:50%;bottom:32px;transform:translateX(-50%);z-index:99999;max-width:80vw;padding:8px 14px;background:rgba(20,20,20,0.92);color:#e8e8e8;border:1px solid rgba(255,255,255,0.14);border-radius:6px;font:13px -apple-system,system-ui,sans-serif;box-shadow:0 4px 12px rgba(0,0,0,0.4);pointer-events:none;opacity:0;transition:opacity 0.15s ease;';(document.body||document.documentElement).appendChild(t);requestAnimationFrame(function(){t.style.opacity='1';});setTimeout(function(){t.style.opacity='0';setTimeout(function(){if(t.parentNode)t.parentNode.removeChild(t);},300);},2400);}window.__desktop_toast=__desktop_toast;function is_external(url){return /^https?:\/\//i.test(url);}function open_overlay(url){var prev=document.getElementById('__gohort_desktop_overlay');if(prev)prev.remove();var overlay=document.createElement('div');overlay.id='__gohort_desktop_overlay';overlay.style.cssText='position:fixed;inset:0;z-index:1000000;background:#0d1117;display:flex;flex-direction:column;';var bar=document.createElement('div');bar.style.cssText='display:flex;align-items:center;gap:0.5rem;padding:6px 8px;background:#161b22;border-bottom:1px solid #30363d;font:12px -apple-system,system-ui,sans-serif;color:#8b949e;';var label=document.createElement('span');label.textContent=url;label.style.cssText='flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';var close=document.createElement('button');close.innerHTML='✕';close.title='Close (Esc)';close.style.cssText='width:26px;height:24px;border:1px solid #30363d;background:#21262d;color:#c9d1d9;cursor:pointer;border-radius:4px;font:14px sans-serif;line-height:1;padding:0;flex-shrink:0;';close.onclick=function(){overlay.remove();document.removeEventListener('keydown',on_esc);};bar.appendChild(label);bar.appendChild(close);var iframe=document.createElement('iframe');iframe.src=url;iframe.style.cssText='flex:1;border:0;width:100%;background:#0d1117;';overlay.appendChild(bar);overlay.appendChild(iframe);function on_esc(e){if(e.key==='Escape'){overlay.remove();document.removeEventListener('keydown',on_esc);}}document.addEventListener('keydown',on_esc);(document.body||document.documentElement).appendChild(overlay);}function __desktop_open_external(u){if(window.runtime&&window.runtime.BrowserOpenURL){try{window.runtime.BrowserOpenURL(String(u));return;}catch(e){}}try{fetch('/__desktop/open',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:String(u)})}).catch(function(){});}catch(e){}}window.open=function(url){if(!url)return null;if(is_external(String(url))){__desktop_open_external(String(url));return null;}open_overlay(String(url));return null;};function __desktop_is_cross_origin(href){try{return (new URL(href,location.href)).origin!==location.origin;}catch(e){return true;}}document.addEventListener('click',function(ev){var a=ev.target&&ev.target.closest&&ev.target.closest('a[href]');if(!a)return;var raw=a.getAttribute('href')||'';if(!raw||raw.charAt(0)==='#'||raw.indexOf('javascript:')===0)return;var blank=(a.getAttribute('target')==='_blank');var ext=is_external(raw)&&__desktop_is_cross_origin(raw);if(!blank&&!ext)return;ev.preventDefault();ev.stopPropagation();window.open(raw);},true);document.addEventListener('click',function(ev){var a=ev.target&&ev.target.closest&&ev.target.closest('a[download]');if(!a)return;var href=a.getAttribute('href')||'';if(href.indexOf('data:')!==0||href.indexOf(';base64,')<0)return;ev.preventDefault();ev.stopPropagation();var name=a.getAttribute('download')||'attachment';__desktop_toast('Saving '+name+'…');var comma=href.indexOf(',');var mime=(href.substring(5,comma).split(';')[0])||'application/octet-stream';var b64=href.substring(comma+1);fetch('/__desktop/save',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,mime:mime,b64:b64})}).then(function(r){return r.json();}).then(function(res){if(res&&res.error){__desktop_toast('Save failed: '+res.error);}else if(res&&res.path){__desktop_toast('Saved: '+res.path);}else{__desktop_toast('Save canceled');}}).catch(function(e){__desktop_toast('Save error: '+(e&&e.message||e));});},true);function add_refresh(){if(document.getElementById('__gohort_desktop_refresh'))return;var b=document.createElement('button');b.id='__gohort_desktop_refresh';b.title='Refresh (⌘R)';b.setAttribute('aria-label','Refresh');b.innerHTML='↻';b.style.cssText='position:fixed;top:6px;right:8px;z-index:99999;width:28px;height:28px;border-radius:50%;border:1px solid rgba(255,255,255,0.12);background:rgba(40,40,40,0.78);color:#ddd;cursor:pointer;font:16px -apple-system,system-ui,sans-serif;line-height:24px;padding:0;box-shadow:0 2px 6px rgba(0,0,0,0.35);transition:background 0.12s,color 0.12s;';b.onmouseover=function(){b.style.background='rgba(60,60,60,0.95)';b.style.color='#fff';};b.onmouseout=function(){b.style.background='rgba(40,40,40,0.78)';b.style.color='#ddd';};b.onclick=function(){location.reload();};(document.body||document.documentElement).appendChild(b);if(!document.getElementById('__gohort_desktop_pill_shift')){var psty=document.createElement('style');psty.id='__gohort_desktop_pill_shift';psty.textContent='.ui-live-pill-wrap{margin-right:2.75rem}';(document.head||document.documentElement).appendChild(psty);}}if(document.readyState==='loading'){document.addEventListener('DOMContentLoaded',add_refresh);}else{add_refresh();}})();(function(){var O='input,textarea,select,button,a[href],summary,[contenteditable=""],[contenteditable="true"],[tabindex],[role="button"],[role="checkbox"],[role="switch"],[role="tab"],[role="menuitem"],[role="option"],[role="radio"]';document.addEventListener('keydown',function(e){if(e.key!==' '&&e.code!=='Space')return;if(e.metaKey||e.ctrlKey||e.altKey)return;var t=e.target;if(t&&t.isContentEditable)return;if(t&&t.closest&&t.closest(O))return;var s=document.scrollingElement||document.documentElement;if(s&&s.scrollHeight>s.clientHeight+1)return;e.preventDefault();});})();
(function(){
// --- desktop copy path ---
// WKWebView's own pasteboard write has proven unreliable in this app
// (see menu.go copyAction's history). Mirror every copy the page sees
// into the Go-side pasteboard via POST /__desktop/copy — plain fetch,
// because proxy-served pages have no window.go. The Go handler falls
// back to Forge's tmux paste buffer when the posted text is empty, so
// the explicit menu action still works in the terminal where tmux owns
// the selection and the DOM has none.
var last_text='',last_at=0;
function toast(t){if(window.__desktop_toast)window.__desktop_toast(t);}
function selection_text(){
  var el=document.activeElement;
  if(el&&(el.tagName==='TEXTAREA'||el.tagName==='INPUT')){
    try{
      if(el.selectionStart!=null&&el.selectionEnd>el.selectionStart){
        return String(el.value).substring(el.selectionStart,el.selectionEnd);
      }
    }catch(e){}
  }
  try{return String(window.getSelection()||'');}catch(e){return '';}
}
function push_copy(text,quiet){
  var now=Date.now();
  if(text===last_text&&(now-last_at)<400)return; // the copy event + keydown both fire for one Cmd+C
  last_text=text;last_at=now;
  fetch('/__desktop/copy',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({text:text})})
    .then(function(r){return r.json();})
    .then(function(res){
      if(quiet)return;
      if(res&&res.error){toast('Copy failed: '+res.error);}
      else if(res&&res.copied>0){toast('Copied');}
      else{toast('Nothing to copy');}
    }).catch(function(){});
}
// Explicit copy (menu: Copy Selection / ⌘⇧C). Posts even when the
// selection is empty so the Go side can fall back to the tmux buffer.
window.__desktop_copy_selection=function(){push_copy(selection_text(),false);};
// Quiet mirrors: whenever the page performs a copy (context menu,
// Edit▸Copy reaching the webview, a copy button using execCommand),
// or Cmd/Ctrl+C lands as a keydown, push the selection Go-side too.
// Guarded on non-empty text so an empty selection never overwrites
// the pasteboard with the stale tmux buffer.
document.addEventListener('copy',function(){var t=selection_text();if(t)push_copy(t,true);},true);
document.addEventListener('keydown',function(e){
  if((e.metaKey||e.ctrlKey)&&!e.altKey&&(e.key==='c'||e.key==='C')){
    var t=selection_text();if(t)push_copy(t,true);
  }
},true);
})();
(function(){
// --- find on page ---
// WKWebView ships no find bar, so this is it: a small fixed bar driven
// by WebKit's window.find(). Opened by Cmd/Ctrl+F (captured here) or
// the Account ▸ Find on Page… menu item (WindowExecJS calls
// window.__desktop_find_open — the menu's key equivalent wins on macOS,
// so the keydown path mostly serves iframes and non-Mac builds).
var bar=null,input=null,count_el=null,last_query='';
function close_bar(){if(bar){bar.remove();bar=null;input=null;count_el=null;last_query='';}}
function count_matches(q){
  var text='';
  try{text=(document.body&&document.body.innerText)||'';}catch(e){return 0;}
  var n=0,i=0,lq=q.toLowerCase(),lt=text.toLowerCase();
  while((i=lt.indexOf(lq,i))!==-1){n++;i+=lq.length;if(n>999)break;}
  return n;
}
function do_find(backwards){
  if(!input)return;
  var q=input.value;
  if(!q){count_el.textContent='';return;}
  if(q!==last_query){
    last_query=q;
    try{window.getSelection().removeAllRanges();}catch(e){} // restart search from the top
    var n=count_matches(q);
    count_el.textContent=n?((n>999?'999+':String(n))+(n===1?' match':' matches')):'No matches';
  }
  var found=false;
  try{found=window.find(q,false,!!backwards,true,false,true,false);}catch(e){}
  if(!found)count_el.textContent='No matches';
  if(input)input.focus(); // keep typing; the match selection stays put in the document
}
function open_bar(){
  if(bar){input.focus();try{input.select();}catch(e){}return;}
  bar=document.createElement('div');
  bar.id='__desktop_findbar';
  bar.style.cssText='position:fixed;top:8px;right:48px;z-index:2147483647;display:flex;align-items:center;gap:6px;padding:6px 8px;background:#161b22;border:1px solid #30363d;border-radius:8px;box-shadow:0 6px 24px rgba(0,0,0,0.5);font:13px -apple-system,system-ui,sans-serif;-webkit-user-select:none;';
  input=document.createElement('input');
  input.type='text';input.placeholder='Find on page';
  input.style.cssText='width:200px;padding:5px 8px;border-radius:6px;border:1px solid #30363d;background:#0d1117;color:#e6edf3;font:13px -apple-system,system-ui,sans-serif;outline:none;';
  count_el=document.createElement('span');
  count_el.style.cssText='min-width:72px;color:#8b949e;font-size:12px;text-align:center;';
  function nav_btn(label,title,backwards){
    var b=document.createElement('button');
    b.textContent=label;b.title=title;
    b.style.cssText='width:26px;height:26px;border:1px solid #30363d;border-radius:6px;background:#21262d;color:#c9d1d9;cursor:pointer;font:14px sans-serif;line-height:1;padding:0;';
    b.onclick=function(){do_find(backwards);};
    return b;
  }
  var close=nav_btn('✕','Close (Esc)',false);
  close.onclick=close_bar;
  bar.appendChild(input);bar.appendChild(count_el);
  bar.appendChild(nav_btn('‹','Previous (Shift+Enter)',true));
  bar.appendChild(nav_btn('›','Next (Enter)',false));
  bar.appendChild(close);
  input.addEventListener('keydown',function(e){
    if(e.key==='Enter'){e.preventDefault();do_find(!!e.shiftKey);}
    else if(e.key==='Escape'){e.preventDefault();e.stopPropagation();close_bar();}
  });
  var debounce=null;
  input.addEventListener('input',function(){
    if(debounce)clearTimeout(debounce);
    debounce=setTimeout(function(){do_find(false);},150);
  });
  (document.body||document.documentElement).appendChild(bar);
  input.focus();
}
window.__desktop_find_open=open_bar;
document.addEventListener('keydown',function(e){
  if((e.metaKey||e.ctrlKey)&&!e.altKey&&!e.shiftKey&&(e.key==='f'||e.key==='F')){
    e.preventDefault();open_bar();
  }
},true);
})();

(function(){
// --- image context menu: Copy Image / Save Image As… ---
// WKWebView's default menu can't reliably copy a data:-URI image (the
// form every chat image takes), and dragging an image out to Finder
// needs a native NSFilePromiseProvider that Wails doesn't expose. So
// right-click on any <img> gets a desktop menu instead: Copy Image
// writes real PNG bytes to the pasteboard via Go (/__desktop/copyimg),
// Save Image As… reuses the native save dialog (/__desktop/save).
function toast(t){if(window.__desktop_toast)window.__desktop_toast(t);}
function b64_of(img){
  var src=img.currentSrc||img.src||'';
  if(/^data:/.test(src)){
    var comma=src.indexOf(',');
    var meta=src.substring(5,comma);
    if(/;base64$/.test(meta)){
      return Promise.resolve({b64:src.substring(comma+1),mime:meta.split(';')[0]||'image/png'});
    }
  }
  // Same-origin images ride the proxy (cookies injected Go-side), so a
  // plain fetch returns the original bytes. Cross-origin external
  // images will fail CORS and surface as a toast — nothing we can do
  // for those without proxying them.
  return fetch(src).then(function(r){
    if(!r.ok)throw new Error('HTTP '+r.status);
    return r.blob();
  }).then(function(blob){
    return new Promise(function(res,rej){
      var fr=new FileReader();
      fr.onload=function(){var d=String(fr.result);var c=d.indexOf(',');res({b64:d.substring(c+1),mime:blob.type||'image/png'});};
      fr.onerror=function(){rej(new Error('read failed'));};
      fr.readAsDataURL(blob);
    });
  });
}
function img_name(img,mime){
  var src=img.currentSrc||img.src||'';
  if(!/^data:/.test(src)){
    try{var p=new URL(src,location.href).pathname.split('/').pop();if(p&&/\./.test(p))return p;}catch(e){}
  }
  var ext=((mime||'image/png').split('/')[1]||'png').split('+')[0];
  return 'image.'+ext;
}
var menu=null;
function close_menu(){
  if(menu){menu.remove();menu=null;
    document.removeEventListener('mousedown',on_away,true);
    document.removeEventListener('keydown',on_key,true);}
}
function on_away(e){if(menu&&!menu.contains(e.target))close_menu();}
function on_key(e){if(e.key==='Escape'){e.preventDefault();close_menu();}}
function item(label,fn){
  var d=document.createElement('div');
  d.textContent=label;
  d.style.cssText='padding:7px 14px;cursor:pointer;white-space:nowrap;';
  d.onmouseover=function(){d.style.background='#21262d';};
  d.onmouseout=function(){d.style.background='';};
  d.onclick=function(){close_menu();fn();};
  return d;
}
function open_menu(x,y,img){
  close_menu();
  menu=document.createElement('div');
  menu.style.cssText='position:fixed;z-index:2147483647;background:#161b22;color:#e6edf3;border:1px solid #30363d;border-radius:8px;box-shadow:0 8px 30px rgba(0,0,0,0.55);font:13px -apple-system,system-ui,sans-serif;padding:4px 0;min-width:170px;-webkit-user-select:none;';
  menu.style.left=x+'px';menu.style.top=y+'px';
  menu.appendChild(item('Copy Image',function(){
    b64_of(img).then(function(d){
      return fetch('/__desktop/copyimg',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({b64:d.b64,mime:d.mime})})
        .then(function(r){return r.json();})
        .then(function(res){if(res&&res.ok)toast('Image copied');else toast('Copy failed: '+((res&&res.error)||'unknown'));});
    }).catch(function(e){toast('Copy failed: '+(e&&e.message||e));});
  }));
  menu.appendChild(item('Save Image As…',function(){
    b64_of(img).then(function(d){
      return fetch('/__desktop/save',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:img_name(img,d.mime),mime:d.mime,b64:d.b64})})
        .then(function(r){return r.json();})
        .then(function(res){if(res&&res.error)toast('Save failed: '+res.error);else if(res&&res.path)toast('Saved: '+res.path);else toast('Save canceled');});
    }).catch(function(e){toast('Save failed: '+(e&&e.message||e));});
  }));
  (document.body||document.documentElement).appendChild(menu);
  var r=menu.getBoundingClientRect();
  if(x+r.width>innerWidth)menu.style.left=Math.max(0,innerWidth-r.width-6)+'px';
  if(y+r.height>innerHeight)menu.style.top=Math.max(0,innerHeight-r.height-6)+'px';
  document.addEventListener('mousedown',on_away,true);
  document.addEventListener('keydown',on_key,true);
}
document.addEventListener('contextmenu',function(e){
  var img=e.target&&e.target.closest&&e.target.closest('img');
  if(!img)return;
  e.preventDefault();e.stopPropagation();
  open_menu(e.clientX,e.clientY,img);
},true);
})();
