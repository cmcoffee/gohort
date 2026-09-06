package admin

import ()

// The admin page's CSS and client-action JS. Each constant is wired into the
// page Head in page.go; the sections that use them live in page_<area>.go.

// The per-row "Reset password" modal, split for the typed ui.Head builder:
// adminUsersCSS (styles), adminUsersModalJS (el + openModal helpers), and
// adminResetPasswordAction (the client-action handler the Users table row
// button dispatches to by name). See serveNewAdminPage for the wire-up.
const adminUsersCSS = `
.admu-overlay { position:fixed; inset:0; background:rgba(0,0,0,0.45); display:flex; align-items:center; justify-content:center; z-index:1000; }
.admu-card { background:var(--bg-1); border:1px solid var(--border); border-radius:10px; padding:1rem 1.1rem; width:min(30rem,92vw); display:flex; flex-direction:column; gap:0.55rem; }
.admu-title { font-weight:600; color:var(--text-hi); }
.admu-opt { display:flex; align-items:center; gap:0.4rem; font-size:0.9rem; color:var(--text); }
.admu-in { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; }
.admu-row { display:flex; gap:0.5rem; margin-top:0.3rem; }
.admu-msg { font-size:0.82rem; }
.admu-msg.ok { color:var(--success); }
.admu-msg.err { color:var(--danger); }
.admu-link { border:1px solid var(--accent); border-radius:8px; padding:0.5rem 0.65rem; background:var(--bg-2); display:flex; flex-direction:column; gap:0.35rem; }
.admu-link code { font-family:ui-monospace,Menlo,monospace; font-size:0.78rem; color:var(--text); word-break:break-all; }
.admu-link button { align-self:flex-start; }`

// adminUsersModalJS defines the shared helpers (el, openModal) for the reset-
// password action. Emitted inside the ui.Head init block; function declarations
// hoist, so adminResetPasswordAction can call openModal.
const adminUsersModalJS = `function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function openModal(user, ctx){
    var overlay = el('div', {class:'admu-overlay'});
    var pw = el('input', {type:'password', class:'admu-in', placeholder:'New password (6+ characters)', autocomplete:'new-password'});
    pw.style.display='none';
    var rLink = el('input', {type:'radio', name:'admu-mode', value:'link'}); rLink.checked=true;
    var rSet = el('input', {type:'radio', name:'admu-mode', value:'set'});
    function sync(){ pw.style.display = rSet.checked ? '' : 'none'; }
    rLink.addEventListener('change', sync); rSet.addEventListener('change', sync);
    var msg = el('div', {class:'admu-msg'});
    var linkBox = el('div', {class:'admu-link'}); linkBox.style.display='none';
    var doBtn = el('button', {class:'ui-row-btn primary'}, ['Reset']);
    var cancel = el('button', {class:'ui-row-btn'}, ['Close']);
    cancel.addEventListener('click', function(){ overlay.remove(); });
    doBtn.addEventListener('click', function(){
      var mode = rSet.checked ? 'set' : 'link';
      var body = {mode:mode};
      msg.textContent=''; msg.className='admu-msg';
      if(mode==='set'){ if(pw.value.length<6){ msg.textContent='Password must be at least 6 characters.'; msg.className='admu-msg err'; return; } body.password=pw.value; }
      doBtn.disabled=true; var o=doBtn.textContent; doBtn.textContent='Working…';
      fetch('api/users/'+encodeURIComponent(user)+'/reset-password', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)})
        .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
        .then(function(d){
          doBtn.disabled=false; doBtn.textContent=o;
          if(d.status==='password_set'){ overlay.remove(); if(ctx&&ctx.reload) ctx.reload(); (window.uiAlert||window.alert)('Password updated for '+user+'.'); return; }
          msg.textContent = d.emailed ? ('Reset link emailed to '+user+'.') : 'Mail not configured — copy this link:'; msg.className='admu-msg ok';
          linkBox.innerHTML=''; linkBox.style.display='';
          var code=el('code',{text:d.link}); var cp=el('button',{class:'ui-row-btn'},['Copy']);
          cp.addEventListener('click', function(){ if(navigator.clipboard) navigator.clipboard.writeText(d.link); cp.textContent='Copied'; setTimeout(function(){ cp.textContent='Copy'; },1200); });
          linkBox.appendChild(code); linkBox.appendChild(cp);
        })
        .catch(function(e){ doBtn.disabled=false; doBtn.textContent=o; msg.textContent='Failed: '+(e&&e.message||e); msg.className='admu-msg err'; });
    });
    var card = el('div', {class:'admu-card'}, [
      el('div', {class:'admu-title'}, ['Reset password — '+user]),
      el('label', {class:'admu-opt'}, [rLink, ' Send reset link']),
      el('label', {class:'admu-opt'}, [rSet, ' Set new password']),
      pw,
      el('div', {class:'admu-row'}, [doBtn, cancel]),
      msg, linkBox
    ]);
    overlay.appendChild(card);
    // No backdrop-click-to-close (a drag-select copy ending on the backdrop
    // would dismiss mid-copy); the Close button dismisses.
    document.body.appendChild(overlay);
  }`

// adminResetPasswordAction is the client-action handler registered under
// "admin_reset_password"; the Users table row button dispatches to it by name.
// It calls openModal (defined in adminUsersModalJS).
const adminResetPasswordAction = `function(ctx){
  var u = ctx && ctx.record && (ctx.record.username || ctx.record.id);
  if(u) openModal(u, ctx);
}`

// artifactDownload is the shared client-action body: it navigates a hidden
// anchor at the unified artifact-export endpoint so the browser downloads a
// secret-free gohort.bundle/v1. All the export buttons below are one-liners
// over it, differing only in the query (individual vs all-of-type vs all).
const artifactDownloadHelper = `function __artifactDownload(href, filename){
  var a = document.createElement('a');
  a.href = href; a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
}`

// artifactExportControls layers the "Include dependencies" export preference on
// top of __artifactDownload. A single checkbox (default CHECKED) injects itself
// above the first export toolbar on the page; every export action routes its
// URL through __artifactExport, which appends deps=0 only when the admin opts
// out. Default-on means a fresh export carries the credentials (and referenced
// tools) the artifact needs, so it installs cleanly on another gohort. This is
// app-specific export behavior — it lives here in the admin app, NOT in
// core/ui, so the toolkit stays domain-agnostic.
const artifactExportControls = `
window.__artifactIncludeDeps = function(){
  var c = document.getElementById('artifact-include-deps');
  return !c || !!c.checked;   // absent -> default to including dependencies
};
function __artifactExport(query, filename){
  var href = 'api/artifacts/export' + (query || '');
  if(!window.__artifactIncludeDeps()){
    href += (href.indexOf('?') >= 0 ? '&' : '?') + 'deps=0';
  }
  __artifactDownload(href, filename);
}
(function(){
  function build(){
    var label = document.createElement('label');
    label.style.cssText = 'display:inline-flex;align-items:center;gap:0.4rem;font-size:0.82rem;color:var(--text-mute);margin:0 0 0.6rem';
    var cb = document.createElement('input');
    cb.type = 'checkbox'; cb.id = 'artifact-include-deps'; cb.checked = true;
    var span = document.createElement('span');
    span.textContent = 'Include dependencies (referenced API credentials & tools) in exports';
    label.appendChild(cb); label.appendChild(span);
    return label;
  }
  function tryInject(){
    if(document.getElementById('artifact-include-deps')) return true;
    var btns = document.querySelectorAll('button');
    for(var i=0;i<btns.length;i++){
      var t = (btns[i].textContent || '').trim();
      if(t.indexOf('Export all') === 0 || t === 'Export everything'){
        var bar = btns[i].parentNode;
        if(bar && bar.parentNode){ bar.parentNode.insertBefore(build(), bar); return true; }
      }
    }
    return false;
  }
  if(document.readyState !== 'loading') tryInject();
  document.addEventListener('DOMContentLoaded', tryInject);
  var obs = new MutationObserver(function(){ if(tryInject()) obs.disconnect(); });
  obs.observe(document.documentElement, {childList:true, subtree:true});
  // Safety valve: stop observing after 10s on pages with no export toolbar.
  setTimeout(function(){ obs.disconnect(); }, 10000);
})();`

// connectorsExportAction downloads ONE connector as a 1-item bundle. Dispatched
// by the per-row "Export" button; reads the row's name.
const connectorsExportAction = `function(ctx){
  var n = ctx && ctx.record && ctx.record.name;
  if(!n){ window.uiAlert && window.uiAlert('No connector selected.'); return; }
  __artifactExport('?type=connector&name=' + encodeURIComponent(n), n + '.gohort.json');
}`

// connectorsExportAllAction downloads every connector as one bundle.
const connectorsExportAllAction = `function(){
  __artifactExport('?all=connector', 'connectors.gohort.json');
}`

// connectorFormDef defines the GENERIC template renderer on window (idempotent):
// one data-driven Add/Configure panel for any connector template — it reads the
// template's field schema and builds the form, with a Detect button when the
// template has one. No per-backend JS; ComfyUI, A1111, and future backends all
// render through this. Lives in the admin app (domain-agnostic), not core/ui.
const connectorFormDef = `
window.uiTemplateForm = function(cfg, reload){
  var apiBase = (cfg.target==='tool') ? 'api/tool' : 'api/connector';
  window.uiOpenSimpleModal({ title:(cfg.create?'Add ':'Configure ')+(cfg.label||'backend'), width:'720px', mount:function(body,dlg){
    var vals=cfg.values||{}, inputs={}, suggestLists=[];
    // A candidate list is computed from what the admin pasted, so it changes
    // with the document — rebuilt on load AND after every Detect, or the
    // options describe a workflow that is no longer in the box.
    function fillSuggestions(source){
      suggestLists.forEach(function(sg){
        var rows=(source&&source[sg.from])||vals[sg.from]||[];
        sg.el.innerHTML='';
        rows.forEach(function(o){
          var op=document.createElement('option');
          op.value=(o&&o.value!=null)?o.value:o;
          if(o&&o.label) op.label=o.label;
          if(o&&o.label) op.textContent=o.label;
          sg.el.appendChild(op);
        });
      });
    }
    var inCss='width:100%;padding:6px;box-sizing:border-box;font-size:12px;';
    var taCss='width:100%;height:120px;box-sizing:border-box;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:11px;line-height:1.4;';
    function heading(t){ var h=document.createElement('div'); h.textContent=t; h.style.cssText='font-size:12px;font-weight:600;margin:12px 0 6px;opacity:0.9;'; body.appendChild(h); }
    function makeField(f){
      var wrap=document.createElement('div'); wrap.style.cssText='margin:0 0 10px;';
      var lb=document.createElement('div'); lb.textContent=f.label+(f.advanced?' (advanced)':''); lb.style.cssText='font-size:12px;opacity:0.8;margin-bottom:3px;';
      var inp;
      if(f.type==='textarea'){ inp=document.createElement('textarea'); inp.spellcheck=false; inp.style.cssText=taCss; }
      else if(f.type==='bool'){ inp=document.createElement('input'); inp.type='checkbox'; }
      else if(f.type==='select'){ inp=document.createElement('select'); inp.style.cssText=inCss; (f.options||[]).forEach(function(o){ var op=document.createElement('option'); op.value=o; op.textContent=o; inp.appendChild(op); }); }
      else if(f.type==='file'){
        // Reads in the browser and drops the text into f.into, so the user
        // reviews it before saving. Never a value itself: excluded from
        // collect() below, which is what the 'file' type check there is for.
        inp=document.createElement('input'); inp.type='file'; inp.style.cssText=inCss;
        if(f.accept) inp.accept=f.accept;
        inp.onchange=function(){
          var file=inp.files&&inp.files[0]; if(!file) return;
          var target=inputs[f.into];
          if(!target){ msg.style.color='#e5484d'; msg.textContent='Nothing to load this into.'; return; }
          var rd=new FileReader();
          rd.onload=function(){ target.el.value=rd.result; msg.style.color=''; msg.textContent='Loaded '+file.name+' ('+rd.result.length+' chars). Review it, then Detect or Save.'; };
          rd.onerror=function(){ msg.style.color='#e5484d'; msg.textContent='Could not read '+file.name; };
          rd.readAsText(file);
        };
      }
      else { inp=document.createElement('input'); inp.type=(f.type==='number'?'number':'text'); inp.style.cssText=inCss; }
      // Suggestions, not a restriction: the input stays typeable, so a field
      // whose candidates were detected badly is still fillable by hand.
      if(f.suggest_from){
        var dlId='ui-sg-'+f.key+'-'+Math.random().toString(36).slice(2,8);
        var dlEl=document.createElement('datalist'); dlEl.id=dlId;
        inp.setAttribute('list',dlId); wrap.appendChild(dlEl);
        suggestLists.push({el:dlEl,from:f.suggest_from});
      }
      var v=(vals[f.key]!=null?vals[f.key]:(f.default!=null?f.default:''));
      if(f.type==='bool'){ inp.checked=!!v; } else { inp.value=v; }
      inputs[f.key]={el:inp,type:f.type};
      wrap.appendChild(lb); wrap.appendChild(inp);
      if(f.help){ var h=document.createElement('div'); h.style.cssText='font-size:11px;opacity:0.6;margin-top:2px;'; h.textContent=f.help; wrap.appendChild(h); }
      body.appendChild(wrap);
    }
    var nameInp=null;
    if(cfg.create){ heading(cfg.target==='tool'?'Tool':'Backend'); makeField({key:'__name',label:'Name (id)',type:'text',help:'letters, digits, underscore, dash'}); nameInp=inputs['__name'].el; }
    var groups={}, order=[];
    (cfg.fields||[]).forEach(function(f){ if(!groups[f.group]){groups[f.group]=[];order.push(f.group);} groups[f.group].push(f); });
    order.forEach(function(g){ heading(g); groups[g].forEach(makeField); });
    fillSuggestions(null);
    function collect(){ var out={}; Object.keys(inputs).forEach(function(k){ if(k==='__name'||inputs[k].type==='file')return; var it=inputs[k]; out[k]=(it.type==='bool'?it.el.checked:(it.type==='number'?(parseInt(it.el.value,10)||0):it.el.value)); }); return out; }
    var msg=document.createElement('div'); msg.style.cssText='font-size:12px;white-space:pre-wrap;min-height:16px;margin:6px 0;';
    var actions=document.createElement('div'); actions.style.cssText='margin-top:10px;display:flex;gap:8px;justify-content:flex-end;align-items:center;';
    if(cfg.detect){
      var det=document.createElement('button'); det.className='ui-row-btn'; det.textContent='Detect';
      det.onclick=function(){ msg.textContent=''; det.disabled=true; det.textContent='Detecting…';
        fetch(apiBase+'-detect?template='+encodeURIComponent(cfg.template),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({values:collect()})})
          .then(function(r){ if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status));}); return r.json(); })
          .then(function(d){ var dv=d.values||{}; Object.keys(dv).forEach(function(k){ if(inputs[k]){ var it=inputs[k]; if(it.type==='bool')it.el.checked=!!dv[k]; else it.el.value=dv[k]; } }); fillSuggestions(dv); det.disabled=false; det.textContent='Detect';
            if(d.warnings&&d.warnings.length){ msg.style.color='#f5a623'; msg.textContent='Detected with notes: '+d.warnings.join('; '); } else { msg.style.color='#3fb950'; msg.textContent='Detected.'; } })
          .catch(function(e){ det.disabled=false; det.textContent='Detect'; msg.style.color='#e5484d'; msg.textContent=(e&&e.message)||(''+e); });
      };
      actions.appendChild(det);
    }
    var defChk=null;
    if(cfg.create && cfg.category==='Image generation'){ var dl=document.createElement('label'); dl.style.cssText='font-size:12px;margin-right:auto;'; defChk=document.createElement('input'); defChk.type='checkbox'; defChk.checked=true; defChk.style.cssText='margin-right:5px;vertical-align:middle;'; dl.appendChild(defChk); dl.appendChild(document.createTextNode('Set as default provider')); actions.appendChild(dl); }
    var cancel=document.createElement('button'); cancel.className='ui-row-btn'; cancel.textContent='Cancel'; cancel.onclick=function(){ try{dlg.close();dlg.remove();}catch(e){} };
    var save=document.createElement('button'); save.className='ui-wb-action-btn'; save.textContent=cfg.create?'Create & approve':'Save';
    save.onclick=function(){ msg.style.color='#e5484d'; msg.textContent='';
      var payload={ template:cfg.template, connector:(cfg.create?'':cfg.connector), owner:(cfg.owner||''), name:(nameInp?nameInp.value.trim():''), values:collect(), set_default:(defChk?defChk.checked:false) };
      if(cfg.create && !payload.name){ msg.textContent='Name is required.'; return; }
      save.disabled=true; save.textContent='Saving…';
      fetch(apiBase+'-config',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
        .then(function(r){ if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status));}); try{dlg.close();dlg.remove();}catch(e){} if(reload)reload(); })
        .catch(function(e){ save.disabled=false; save.textContent=cfg.create?'Create & approve':'Save'; msg.textContent=(e&&e.message)||(''+e); });
    };
    actions.appendChild(cancel); actions.appendChild(save);
    body.appendChild(msg); body.appendChild(actions);
  }});
};
window.uiAddBackend=function(category, reload){
  fetch('api/connector-templates'+(category?('?category='+encodeURIComponent(category)):''),{cache:'no-store',credentials:'same-origin'})
    .then(function(r){ return r.json(); })
    .then(function(list){
      if(!list||!list.length){ window.uiAlert && window.uiAlert('No backend templates available.'); return; }
      function open(name){ fetch('api/connector-template?name='+encodeURIComponent(name),{cache:'no-store',credentials:'same-origin'}).then(function(r){return r.json();}).then(function(sc){ window.uiTemplateForm(sc, reload); }); }
      if(list.length===1){ open(list[0].name); return; }
      var labels=list.map(function(x){return x.label+' ('+x.name+')';});
      var pick=window.prompt('Add which backend?\n'+labels.join('\n'), list[0].name);
      if(pick){ open(pick.trim()); }
    })
    .catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
};
window.uiAddTool=function(reload){
  fetch('api/tool-templates',{cache:'no-store',credentials:'same-origin'})
    .then(function(r){ return r.json(); })
    .then(function(list){
      if(!list||!list.length){ window.uiAlert && window.uiAlert('No tool templates available.'); return; }
      function open(name){ fetch('api/tool-template?name='+encodeURIComponent(name),{cache:'no-store',credentials:'same-origin'}).then(function(r){return r.json();}).then(function(sc){ window.uiTemplateForm(sc, reload); }); }
      if(list.length===1){ open(list[0].name); return; }
      var labels=list.map(function(x){return x.label+' ('+x.name+')';});
      var pick=window.prompt('Add which tool?\n'+labels.join('\n'), list[0].name);
      if(pick){ open(pick.trim()); }
    })
    .catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
};
window.uiConfigureBackend=function(connector, reload){
  fetch('api/connector-config?connector='+encodeURIComponent(connector),{cache:'no-store',credentials:'same-origin'})
    .then(function(r){ if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status));}); return r.json(); })
    .then(function(sc){ window.uiTemplateForm(sc, reload); })
    .catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
};`

// connectorEditSpecAction opens an inline JSON editor for a connector's
// kind-specific Spec. Generic across every kind (the Spec IS the backend
// definition) — the immediate use is tuning a rest_image backend (checkpoint /
// default_steps / submit_body / endpoints) without re-authoring via the Builder.
// GET the pretty spec, edit, POST it back; the server re-validates against the
// kind and re-materializes an approved connector so the change takes effect now.
const connectorEditSpecAction = `function(ctx){
  var r = (ctx && ctx.record) || {};
  var name = r.name;
  var reload = ctx && ctx.reload;
  if(!name){ window.uiAlert && window.uiAlert('No connector selected.'); return; }
  fetch('api/connectors/spec?name=' + encodeURIComponent(name), {cache:'no-store', credentials:'same-origin'})
    .then(function(res){ if(!res.ok) return res.text().then(function(t){ throw new Error(t||('HTTP '+res.status)); }); return res.json(); })
    .then(function(data){
      window.uiOpenSimpleModal({ title: 'Edit spec: ' + name + ' (' + (data.kind||'') + ')', width: '760px', mount: function(body, dlg){
        var note = document.createElement('div');
        note.style.cssText = 'margin:0 0 8px;font-size:12px;opacity:0.75;line-height:1.45;';
        note.textContent = 'Edit the connector spec as JSON. Saving re-validates it against the connector kind; if the connector is approved it re-materializes immediately (e.g. a rest_image backend picks up a new default_steps / submit_body / checkpoint right away).';
        var ta = document.createElement('textarea');
        ta.value = data.spec || '{}';
        ta.spellcheck = false;
        ta.style.cssText = 'width:100%;height:380px;box-sizing:border-box;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px;line-height:1.45;';
        var msg = document.createElement('div');
        msg.style.cssText = 'margin:8px 0 0;font-size:12px;color:#e5484d;white-space:pre-wrap;min-height:16px;';
        var actions = document.createElement('div');
        actions.style.cssText = 'margin-top:10px;display:flex;gap:8px;justify-content:flex-end;';
        var cancel = document.createElement('button');
        cancel.className = 'ui-row-btn'; cancel.textContent = 'Cancel';
        cancel.onclick = function(){ try{ dlg.close(); dlg.remove(); }catch(e){} };
        var save = document.createElement('button');
        save.className = 'ui-wb-action-btn'; save.textContent = 'Save spec';
        save.onclick = function(){
          msg.textContent = '';
          var parsed;
          try { parsed = JSON.parse(ta.value); } catch(e){ msg.textContent = 'Invalid JSON: ' + ((e && e.message)||e); return; }
          save.disabled = true; save.textContent = 'Saving…';
          fetch('api/connectors?action=update_spec&name=' + encodeURIComponent(name), {
            method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(parsed)
          }).then(function(res){
            if(!res.ok) return res.text().then(function(t){ throw new Error(t||('HTTP '+res.status)); });
            try{ dlg.close(); dlg.remove(); }catch(e){}
            if(reload) reload(); else if(window.uiInvalidate) window.uiInvalidate(['api/connectors']);
          }).catch(function(e){ save.disabled=false; save.textContent='Save spec'; msg.textContent = (e && e.message)||(''+e); });
        };
        actions.appendChild(cancel); actions.appendChild(save);
        body.appendChild(note); body.appendChild(ta); body.appendChild(msg); body.appendChild(actions);
      }});
    }).catch(function(e){ window.uiAlert && window.uiAlert((e && e.message)||(''+e)); });
}`

// toolsExportAction downloads ONE persistent tool as a 1-item bundle. The
// persistent-tools row nests the definition under .tool and carries the owning
// user in .owner (tools are per-user), so export needs both.
const toolsExportAction = `function(ctx){
  var r = (ctx && ctx.record) || {};
  var n = (r.tool && r.tool.name) || r.name;
  var o = r.owner || '';
  if(!n){ window.uiAlert && window.uiAlert('No tool selected.'); return; }
  __artifactExport('?type=tool&name=' + encodeURIComponent(n) + '&owner=' + encodeURIComponent(o), n + '.gohort.json');
}`

// toolsExportAllAction downloads every persistent tool (all owners) as one bundle.
const toolsExportAllAction = `function(){
  __artifactExport('?all=tool', 'tools.gohort.json');
}`

// scopeManageActionJS builds a scope-pill client action for one KIND
// (tool | pipeline | credential). All three share the same fetch/post glue
// against api/tool-scope?kind=<kind> and the generic pill renderer
// (window.uiRenderScopePills in core/ui) — only the record-identifier
// expression, the modal title label, and the decorate(st)→{primary,items,note}
// copy differ. Generalized from the original tool-only action so pipelines
// and credentials scope through identical UI with no new plumbing.
//
//	idExpr    — JS reading the backend identifier off the row record
//	labelExpr — JS reading the human label for the modal title
//	decorate  — JS function expression: function(st){ return {primary?,items,note}; }
func scopeManageActionJS(kind, idExpr, labelExpr, decorate string) string {
	return `function(ctx){
  var r = (ctx && ctx.record) || {};
  var id = ` + idExpr + `;
  var label = ` + labelExpr + `;
  var owner = r.owner || '';
  var reload = ctx && ctx.reload;
  if(!id){ window.uiAlert && window.uiAlert('Nothing selected.'); return; }
  var qs = 'name=' + encodeURIComponent(id) + '&owner=' + encodeURIComponent(owner) + '&kind=` + kind + `';
  window.uiOpenSimpleModal({
    title: 'Scope: ' + (label || id),
    width: '560px',
    mount: function(body){
      var host = document.createElement('div');
      body.appendChild(host);
      window.uiRenderScopePills(host, {
        load: function(){
          return fetch('api/tool-scope?' + qs, {cache:'no-store'}).then(function(res){
            if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
            return res.json();
          }).then(` + decorate + `);
        },
        toggle: function(key, on){
          var target = (key === '__primary__') ? 'global' : key;
          return fetch('api/tool-scope?' + qs, {
            method: 'POST',
            headers: {'Content-Type':'application/json'},
            body: JSON.stringify({ target: target, on: on })
          }).then(function(res){
            if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
            if(reload) reload();
          });
        }
      });
    }
  });
}`
}

// mapAgentsPillsJS is the shared items mapper: st.agents → per-agent pills.
//
// partial carries the third pill state through. Only a GROUPED kind (a
// category, whose scope is the union of its members') ever sets it; for a
// single tool, pipeline or credential it is always absent and the pill renders
// on/off exactly as before.
const mapAgentsPillsJS = `(st.agents || []).map(function(a){ return { key: a.id, label: a.name, on: !!a.on, under: a.parent_id, partial: !!a.partial }; })`

// toolPromoteGlobalAction moves an agent-scoped tool into its owner's user-wide
// pool — the on-ramp to the tier-1 user ACL, which is the only access control
// this page should be offering.
//
// It deliberately does ONE thing. The button it replaced opened the per-agent
// pill editor, which let an admin attach another user's tool to that user's
// agents — the wrong level: admin governs which USERS may reach a tool, and
// which of their own agents load it is the user's call in their agent editor.
// Promote reaches the user-ACL model; it doesn't reimplement a second one.
const toolPromoteGlobalAction = `async function(ctx){
  var r = ctx && ctx.record; if(!r) return;
  var name = (r.tool && r.tool.name) || r.name;
  var owner = r.owner || '';
  if(!name){ window.uiAlert && window.uiAlert('No tool selected.'); return; }
  var who = owner ? (owner + "'s") : 'the owner' + "'s";
  var msg = 'Move "' + name + '" into ' + who + ' user-wide pool? Every one of their '
          + 'agents can then use it, and you can Share it and set which users may '
          + 'adopt it from Global Tools.';
  // uiConfirm, not confirm: the desktop shim cannot answer a synchronous browser
  // dialog, so a bare confirm() there does not ask. It used to approve without
  // asking, which on a promote-to-global is exactly the wrong default.
  if(!(await (window.uiConfirm ? window.uiConfirm(msg) : Promise.resolve(window.confirm(msg))))) return;
  fetch('api/tool-scope?name=' + encodeURIComponent(name) + '&owner=' + encodeURIComponent(owner), {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({target: 'global', on: true})
  }).then(function(res){
    if(!res.ok) return res.text().then(function(t){ throw new Error(t || res.status); });
    if(ctx.refresh) ctx.refresh();
    window.uiToast && window.uiToast('Promoted "' + name + '" to the global pool.');
  }).catch(function(err){
    window.uiAlert && window.uiAlert('Promote failed: ' + (err && err.message || err));
  });
}`

// pipelineScopeManageAction — Global (all agents run it) + per-agent attach.
var pipelineScopeManageAction = scopeManageActionJS("pipeline",
	`r.id || r.name`, `r.name || r.id`,
	`function(st){
    var note = st.global
      ? 'Global: every agent can run this pipeline. Turn an agent off to deny it there; turn Global off to scope it down to the agents left on.'
      : 'Agent-scoped: runnable only on the agents shown. Turn Global on to give it to all your agents.';
    return { primary: { label: 'Global (all agents)', on: !!st.global }, items: `+mapAgentsPillsJS+`, note: note };
  }`)

// categoryScopeManageAction — set access for EVERY tool claiming a category at
// once, and say so plainly when they don't agree.
//
// The pills mean the same thing they do for one tool; what differs is that a
// category's answer is the union of its members'. A target every member holds
// reads on, one no member holds reads off, and anything in between is the third
// state — which is what "Custom" names. Clicking a Custom pill settles it by
// turning every member on.
var categoryScopeManageAction = scopeManageActionJS("category",
	`r.name || r.id`, `r.name || r.id`,
	`function(st){
    var note;
    if (!(st.agents || []).length) {
      // An empty category: say that, rather than showing a pill grid implying
      // there is something here to grant.
      return { items: [], note: 'No tools claim this category yet. Use Members to add some, then set the category\u2019s access here and every tool in it moves together.' };
    }
    if (st.custom) {
      note = 'Custom: the tools in this category do not all have the same access, so there is no single answer for the category. A dashed pill is one they disagree on — click it to turn every tool in the category on there.';
    } else if (st.global) {
      note = 'Global: every tool in this category is in the user-wide pool, so all agents can use them. Turn an agent off to deny the whole category there.';
    } else {
      note = 'Agent-scoped: every tool in this category is available only on the agents shown. Turn Global on to move the whole category into the user-wide pool.';
    }
    return {
      primary: { label: 'Global (all agents)', on: !!st.global, partial: !!st.global_partial },
      items: `+mapAgentsPillsJS+`,
      note: note
    };
  }`)

// artifactsExportAllAction downloads EVERYTHING — connectors + tools +
// credentials + agents + skills + any future registered type — as one
// gohort.bundle/v1.
const artifactsExportAllAction = `function(){
  __artifactExport('', 'gohort-bundle.json');
}`

// credentialsExportAction downloads ONE API credential's CONFIG as a 1-item
// bundle. No secret travels — it lives in a separate encrypted key and is never
// part of the recipe; the importer supplies it on their side.
const credentialsExportAction = `function(ctx){
  var n = ctx && ctx.record && ctx.record.name;
  if(!n){ window.uiAlert && window.uiAlert('No credential selected.'); return; }
  __artifactExport('?type=credential&name=' + encodeURIComponent(n), n + '.gohort.json');
}`

// credentialsExportAllAction downloads every API credential's config as one bundle.
const credentialsExportAllAction = `function(){
  __artifactExport('?all=credential', 'credentials.gohort.json');
}`

// artifactImportPreviewJS is the preview-then-confirm import flow:
// __artifactImportPreview() opens a file picker, dry-runs the chosen bundle
// against /api/artifacts/preview, and shows the result in a uiOpenModal —
// per-artifact import/skip predictions plus unmet-reference warnings. Nothing
// is written until the admin clicks the Import button, which posts the SAME
// bytes to /api/artifacts/import and surfaces the real result. App-specific
// (knows the artifact endpoints), so it lives here, not in core/ui.
const artifactImportPreviewJS = `
window.__artifactImportPreview = function(){
  var input = document.createElement('input');
  input.type = 'file'; input.accept = '.json,application/json';
  input.style.display = 'none';
  document.body.appendChild(input);
  input.addEventListener('change', function(){
    var f = input.files && input.files[0];
    input.remove();
    if(!f) return;
    var reader = new FileReader();
    reader.onload = function(){ __artifactPreviewOpen(String(reader.result || ''), f.name); };
    reader.onerror = function(){ (window.uiAlert||window.alert)('Could not read the selected file.'); };
    reader.readAsText(f);
  });
  input.click();
};
function __artifactPreviewOpen(text, filename){
  fetch('api/artifacts/preview', {method:'POST', credentials:'same-origin',
      headers:{'Content-Type':'application/json'}, body: JSON.stringify({pack:text})})
    .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t || ('HTTP '+r.status)); }); })
    .then(function(p){ __artifactPreviewModal(p, text, filename); })
    .catch(function(e){ (window.uiAlert||window.alert)('Preview failed: ' + (e && e.message || e)); });
}
function __artifactPreviewModal(p, text, filename){
  var n = p.would_import || 0, s = p.would_skip || 0;
  window.uiOpenModal({
    title: 'Import preview',
    subtitle: filename + ' — nothing has been imported yet. Imported artifacts land as drafts for review; a name that already exists is skipped.',
    width: '760px',
    actions: [
      {label: 'Cancel'},
      {label: 'Import ' + n + (n === 1 ? ' artifact' : ' artifacts'), primary: true, onClick: function(api, btn){
        btn.disabled = true; btn.textContent = 'Importing…';
        fetch('api/artifacts/import', {method:'POST', credentials:'same-origin',
            headers:{'Content-Type':'application/json'}, body: JSON.stringify({pack:text})})
          .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t || ('HTTP '+r.status)); }); })
          .then(function(d){
            api.close();
            if(window.uiInvalidate) window.uiInvalidate(['api/connectors','api/persistent-tools','api/secure-api','api/skills']);
            (window.uiAlert||window.alert)(d.message || 'Import complete.');
          })
          .catch(function(e){
            btn.disabled = false; btn.textContent = 'Import';
            (window.uiAlert||window.alert)('Import failed: ' + (e && e.message || e));
          });
      }}
    ],
    mount: function(body, api){
      if(!n && api.primaryButton) api.primaryButton.disabled = true;
      var sum = document.createElement('div');
      sum.style.cssText = 'font-size:0.85rem;color:var(--text-mute)';
      sum.textContent = 'Would import ' + n + ', skip ' + s + '.';
      body.appendChild(sum);
      var list = document.createElement('div');
      list.style.cssText = 'display:flex;flex-direction:column;gap:0.35rem';
      (p.items || []).forEach(function(it){
        var row = document.createElement('div');
        row.style.cssText = 'display:flex;gap:0.6rem;align-items:baseline;font-size:0.85rem;border-bottom:1px solid var(--border);padding-bottom:0.35rem';
        var imp = it.action === 'import';
        var badge = document.createElement('span');
        badge.textContent = imp ? 'import' : 'skip';
        badge.style.cssText = 'flex:0 0 3.6rem;text-align:center;font-size:0.72rem;border-radius:4px;padding:0.1rem 0.3rem;' +
          (imp ? 'background:rgba(99,102,241,0.15);color:var(--accent,#6366f1)' : 'background:rgba(139,148,158,0.15);color:var(--text-mute)');
        var typ = document.createElement('span');
        typ.style.cssText = 'flex:0 0 6.5rem;color:var(--text-mute)';
        typ.textContent = it.type;
        var name = document.createElement('span');
        name.style.cssText = 'font-weight:600;overflow-wrap:anywhere';
        name.textContent = it.name || '(unnamed)';
        row.appendChild(badge); row.appendChild(typ); row.appendChild(name);
        if(it.detail){
          var det = document.createElement('span');
          det.style.cssText = 'color:var(--text-mute);font-size:0.78rem';
          det.textContent = it.detail;
          row.appendChild(det);
        }
        list.appendChild(row);
        (it.warnings || []).forEach(function(wtext){
          var wrow = document.createElement('div');
          wrow.style.cssText = 'font-size:0.78rem;color:var(--warn,#d97706);padding-left:4.2rem';
          wrow.textContent = 'Warning: ' + wtext;
          list.appendChild(wrow);
        });
      });
      body.appendChild(list);
    }
  });
}`

// artifactsImportPreviewAction dispatches the "Import bundle…" toolbar button
// into the preview flow above.
const artifactsImportPreviewAction = `function(){ window.__artifactImportPreview(); }`

// skillsExportAction downloads ONE skill as a 1-item bundle. Skills are
// per-user and this surface lists the requesting admin's own pool, so no owner
// travels in the query — the export endpoint defaults owner to the requester.
const skillsExportAction = `function(ctx){
  var n = ctx && ctx.record && ctx.record.name;
  if(!n){ window.uiAlert && window.uiAlert('No skill selected.'); return; }
  __artifactExport('?type=skill&name=' + encodeURIComponent(n), n + '.gohort.json');
}`

// skillsExportAllAction downloads every skill (all owners) as one bundle.
const skillsExportAllAction = `function(){
  __artifactExport('?all=skill', 'skills.gohort.json');
}`
