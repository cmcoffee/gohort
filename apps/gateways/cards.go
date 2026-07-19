package gateways

// The card HTML for the Gateways page. Each rides in a ui.Card (the Card
// renderer re-executes the inline <script>) because it's app-specific behavior,
// not a core/ui primitive — same pattern as the Account page panels.
//
// credentialsHTML + userToolsHTML + globalToolsHTML call THIS app's own relative
// endpoints (api/credentials, api/tools, api/global-tools). connectionsHTML is
// the exception: its OAuth/MCP consent + callback endpoints stay registered on
// /account for redirect-URI stability, so it calls those by ABSOLUTE path.

// credentialsHTML — the user's own API-credential CRUD (view + inline add/edit).
const credentialsHTML = `<div id="acct-creds" class="acct-creds">Loading…</div>
<style>
.acct-creds { display:flex; flex-direction:column; gap:0.55rem; }
.acct-cred { border:1px solid var(--border); border-radius:8px; padding:0.55rem 0.75rem; display:flex; align-items:center; gap:0.6rem; }
.acct-cred-meta { flex:1; min-width:0; }
.acct-cred-name { font-weight:600; color:var(--text); }
.acct-cred-sub { font-size:0.75rem; color:var(--text-mute); margin-top:0.1rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.acct-cred-badge { font-size:0.68rem; font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; background:var(--bg-2); color:var(--text-mute); margin-left:0.4rem; }
.acct-cred-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:6px; padding:0.3rem 0.7rem; font:inherit; font-size:0.8rem; }
.acct-cred-btn:hover { color:var(--text); }
.acct-cred-btn.danger:hover { color:var(--danger); border-color:var(--danger); }
.acct-cred-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
.acct-cred-form { border:1px solid var(--accent); border-radius:8px; padding:0.75rem 0.85rem; display:flex; flex-direction:column; gap:0.5rem; background:var(--bg-2); }
.acct-cred-form label { font-size:0.78rem; color:var(--text-mute); display:flex; flex-direction:column; gap:0.2rem; }
.acct-cred-form input, .acct-cred-form select, .acct-cred-form textarea { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; font-size:0.85rem; }
.acct-cred-form .row { display:flex; gap:0.6rem; }
.acct-cred-form .row > label { flex:1; }
.acct-cred-form .actions { display:flex; gap:0.5rem; align-items:center; margin-top:0.2rem; }
.acct-cred-form .chk { flex-direction:row; align-items:center; gap:0.4rem; }
.acct-cred-form .chk input { width:auto; }
.acct-cred-add { align-self:flex-start; cursor:pointer; background:var(--accent); color:#fff; border:0; border-radius:6px; padding:0.4rem 0.9rem; font:inherit; font-weight:600; }
.acct-cred-msg { font-size:0.8rem; }
.acct-cred-msg.err { color:var(--danger); }
</style>
<script>
(function(){
  var root = document.getElementById('acct-creds');
  if (!root) return;
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  var TYPES = [['bearer','Bearer token'],['header','Custom header'],['query','Query param'],['basic_auth','Basic auth'],['none','No auth (public)']];
  function load(){ return fetch('api/credentials',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }
  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-cred-empty',text:'No credentials yet. Add one to let your agents call an API as you.'})); }
    list.forEach(function(c){
      var sub = (c.base_url||'') + (c.description ? '  ·  '+c.description : '');
      var meta = el('div',{class:'acct-cred-meta'},[
        el('div',{class:'acct-cred-name'},[ document.createTextNode(c.name), el('span',{class:'acct-cred-badge',text:c.type}) ]),
        el('div',{class:'acct-cred-sub',text: sub})
      ]);
      var edit = el('button',{class:'acct-cred-btn',text:'Edit'});
      edit.addEventListener('click',function(){ showForm(c); });
      var del = el('button',{class:'acct-cred-btn danger',text:'Delete'});
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Delete credential "'+c.name+'"? Agents and tools using it stop working.') : Promise.resolve(confirm('Delete "'+c.name+'"?'));
        go.then(function(ok){ if(!ok) return; fetch('api/credentials?name='+encodeURIComponent(c.name),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      root.appendChild(el('div',{class:'acct-cred'},[meta,edit,del]));
    });
    var add = el('button',{class:'acct-cred-add',text:'+ Add credential'});
    add.addEventListener('click',function(){ showForm(null); });
    root.appendChild(add);
  }
  function field(labelText, node){ return el('label',{},[document.createTextNode(labelText), node]); }
  function showForm(existing){
    var editing = !!existing;
    var nameI = el('input',{type:'text',placeholder:'lower_snake_case',value: editing?existing.name:''});
    if(editing) nameI.setAttribute('readonly','readonly');
    var typeSel = el('select',{});
    TYPES.forEach(function(t){ var o=el('option',{value:t[0],text:t[1]}); if(editing&&existing.type===t[0]) o.setAttribute('selected','selected'); typeSel.appendChild(o); });
    var baseI = el('input',{type:'text',placeholder:'https://api.example.com',value: editing?(existing.base_url||''):''});
    var paramWrap = field('Header / query name', el('input',{type:'text',placeholder:'X-Api-Key',value: editing?(existing.param_name||''):''}));
    var paramI = paramWrap.querySelector('input');
    var descI = el('input',{type:'text',placeholder:'What this is for (optional)',value: editing?(existing.description||''):''});
    var secretI = el('input',{type:'password',placeholder: editing?'Leave blank to keep current secret':'Paste your key / token'});
    var confirmC = el('input',{type:'checkbox'}); if(editing&&existing.requires_confirm) confirmC.setAttribute('checked','checked');
    var msg = el('span',{class:'acct-cred-msg'});
    function syncType(){
      var t = typeSel.value;
      paramWrap.style.display = (t==='header'||t==='query') ? '' : 'none';
      secretI.parentNode.style.display = (t==='none') ? 'none' : '';
    }
    typeSel.addEventListener('change', syncType);
    var save = el('button',{class:'acct-cred-add',text: editing?'Save':'Create'});
    var cancel = el('button',{class:'acct-cred-btn',text:'Cancel'});
    cancel.addEventListener('click', load);
    save.addEventListener('click',function(){
      msg.textContent=''; msg.className='acct-cred-msg';
      var body = { name:nameI.value.trim(), type:typeSel.value, base_url:baseI.value.trim(),
        param_name:paramI.value.trim(), description:descI.value.trim(), secret:secretI.value,
        requires_confirm:confirmC.checked };
      save.disabled=true; var orig=save.textContent; save.textContent='Saving…';
      fetch('api/credentials',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
        .then(function(r){ if(r.status===204){ load(); return; } return r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
        .catch(function(e){ save.disabled=false; save.textContent=orig; msg.className='acct-cred-msg err'; msg.textContent=(e&&e.message||e); });
    });
    var form = el('div',{class:'acct-cred-form'},[
      field('Name', nameI),
      el('div',{class:'row'},[ field('Type', typeSel), field('Base URL', baseI) ]),
      paramWrap,
      field('Description', descI),
      field('Secret', secretI),
      el('label',{class:'chk'},[confirmC, document.createTextNode('Require confirmation before each call')]),
      el('div',{class:'actions'},[save, cancel, msg])
    ]);
    root.innerHTML=''; root.appendChild(form); syncType(); nameI.focus();
  }
  load();
})();
</script>`

// userToolsHTML — a read-mostly list of the user's own persistent tools + delete.
const userToolsHTML = `<div id="acct-tools" class="acct-tools">Loading…</div>
<style>
.acct-tools { display:flex; flex-direction:column; gap:0.55rem; }
.acct-tool { border:1px solid var(--border); border-radius:8px; padding:0.55rem 0.75rem; display:flex; align-items:center; gap:0.6rem; }
.acct-tool-meta { flex:1; min-width:0; }
.acct-tool-name { font-weight:600; color:var(--text); }
.acct-tool-badge { font-size:0.68rem; font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; background:var(--bg-2); color:var(--text-mute); margin-left:0.4rem; }
.acct-tool-badge.shared { background:color-mix(in srgb, var(--accent) 22%, transparent); color:var(--accent); }
.acct-tool-badge.missing { background:color-mix(in srgb, var(--danger) 20%, transparent); color:var(--danger); }
.acct-tool-sub { font-size:0.75rem; color:var(--text-mute); margin-top:0.1rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.acct-tool-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:6px; padding:0.3rem 0.7rem; font:inherit; font-size:0.8rem; }
.acct-tool-btn:hover { color:var(--danger); border-color:var(--danger); }
.acct-tool-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
</style>
<script>
(function(){
  var root = document.getElementById('acct-tools');
  if (!root) return;
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function load(){ return fetch('api/tools',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }
  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-tool-empty',text:'No tools yet. Ask the assistant in chat to build one for you.'})); return; }
    list.forEach(function(t){
      var name = el('div',{class:'acct-tool-name'},[document.createTextNode(t.name)]);
      if(t.mode) name.appendChild(el('span',{class:'acct-tool-badge',text:t.mode}));
      if(t.shared) name.appendChild(el('span',{class:'acct-tool-badge shared',text:'shared'}));
      if(t.missing) name.appendChild(el('span',{class:'acct-tool-badge missing',text:'missing '+(t.credential||'credential')}));
      var subText = t.description || '';
      if(t.last_used) subText = subText ? (subText+'  ·  last used '+t.last_used) : ('last used '+t.last_used);
      var meta = el('div',{class:'acct-tool-meta'},[ name, el('div',{class:'acct-tool-sub',text: subText}) ]);
      var del = el('button',{class:'acct-tool-btn',text:'Delete'});
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Delete tool "'+t.name+'"? Agents using it lose it.') : Promise.resolve(confirm('Delete "'+t.name+'"?'));
        go.then(function(ok){ if(!ok) return; fetch('api/tools?name='+encodeURIComponent(t.name),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      root.appendChild(el('div',{class:'acct-tool'},[meta,del]));
    });
  }
  load();
})();
</script>`

// globalToolsHTML — the global-tool opt-in catalog: Add/Remove per shared tool.
const globalToolsHTML = `<div id="gw-global" class="gw-global">Loading…</div>
<style>
.gw-global { display:flex; flex-direction:column; gap:0.55rem; }
.gw-gt { border:1px solid var(--border); border-radius:8px; padding:0.55rem 0.75rem; display:flex; align-items:center; gap:0.6rem; }
.gw-gt-meta { flex:1; min-width:0; }
.gw-gt-name { font-weight:600; color:var(--text); }
.gw-gt-badge { font-size:0.68rem; font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; background:var(--bg-2); color:var(--text-mute); margin-left:0.4rem; }
.gw-gt-badge.missing { background:color-mix(in srgb, var(--danger) 20%, transparent); color:var(--danger); }
.gw-gt-sub { font-size:0.75rem; color:var(--text-mute); margin-top:0.1rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.gw-gt-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:6px; padding:0.3rem 0.75rem; font:inherit; font-size:0.8rem; }
.gw-gt-btn.add { background:var(--accent); color:#fff; border:0; }
.gw-gt-btn.added:hover { color:var(--danger); border-color:var(--danger); }
.gw-gt-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
</style>
<script>
(function(){
  var root = document.getElementById('gw-global');
  if (!root) return;
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function load(){ return fetch('api/global-tools',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }
  function toggle(name, adopt, btn){
    btn.disabled=true; var orig=btn.textContent; btn.textContent='…';
    fetch('api/global-tools',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,adopt:adopt})})
      .then(function(r){ if(r.status===204){ load(); return; } return r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; alert('Failed: '+(e&&e.message||e)); });
  }
  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'gw-gt-empty',text:'No global tools published yet. When your deployment shares one, it appears here to add.'})); return; }
    list.forEach(function(t){
      var name = el('div',{class:'gw-gt-name'},[document.createTextNode(t.name)]);
      if(t.mode) name.appendChild(el('span',{class:'gw-gt-badge',text:t.mode}));
      if(t.missing) name.appendChild(el('span',{class:'gw-gt-badge missing',text:'missing '+(t.credential||'credential')}));
      var meta = el('div',{class:'gw-gt-meta'},[ name, el('div',{class:'gw-gt-sub',text: t.description||''}) ]);
      var btn;
      if(t.adopted){
        btn = el('button',{class:'gw-gt-btn added',text:'Added'});
        btn.addEventListener('click',function(){ toggle(t.name, false, btn); });
      } else {
        btn = el('button',{class:'gw-gt-btn add',text:'+ Add'});
        btn.addEventListener('click',function(){ toggle(t.name, true, btn); });
      }
      root.appendChild(el('div',{class:'gw-gt'},[meta,btn]));
    });
  }
  load();
})();
</script>`

// connectionsHTML — the Connected-accounts panel. The OAuth/MCP consent + callback
// endpoints stay registered on /account (stable redirect URIs), so this card calls
// them by ABSOLUTE path even though it renders under /gateways.
const connectionsHTML = `<div id="acct-conns" class="acct-conns">Loading…</div>
<style>
.acct-conns { display: flex; flex-direction: column; gap: 0.6rem; }
.acct-conn { border: 1px solid var(--border); border-radius: 8px; padding: 0.7rem 0.8rem; }
.acct-conn-head { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
.acct-conn-name { font-weight: 600; color: var(--text); flex: 1; }
.acct-conn-badge { font-size: 0.7rem; font-weight: 600; padding: 0.1rem 0.5rem; border-radius: 999px; }
.acct-conn-badge.on { background: color-mix(in srgb, var(--success) 22%, transparent); color: var(--success); }
.acct-conn-badge.off { background: var(--bg-2); color: var(--text-mute); }
.acct-conn-desc { font-size: 0.82rem; color: var(--text-mute); margin-bottom: 0.5rem; }
.acct-conn-row { display: flex; gap: 0.4rem; align-items: center; }
.acct-conn-row input { flex: 1; background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 0.35rem 0.5rem; font: inherit; font-size: 0.85rem; }
.acct-conns-empty { color: var(--text-mute); font-style: italic; padding: 0.5rem 0; }
</style>
<script>
(function(){
  var box = document.getElementById('acct-conns');
  if (!box) return;
  var API = '/account/api/connections';
  // A consent popup reports success via postMessage; refresh so the badge flips.
  window.addEventListener('message', function(e){ if (e && e.data === 'gohort-mcp-connected') load(); });
  function el(t, a, k){ var n=document.createElement(t); if(a) for(var x in a){ if(x==='text') n.textContent=a[x]; else if(x==='class') n.className=a[x]; else n.setAttribute(x,a[x]); } (k||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function post(body, btn){
    btn.disabled = true; var orig = btn.textContent; btn.textContent = '…';
    return fetch(API, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)})
      .then(function(r){ if(!r.ok && r.status!==204) return r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); load(); })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; alert('Failed: '+(e&&e.message||e)); });
  }
  function save(name, secret, btn){ return post({name:name, secret:secret}, btn); }
  function disconnect(name, btn){ return post({name:name, disconnect:true}, btn); }
  function load(){
    fetch(API, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(list){
      box.innerHTML = '';
      if (!list || !list.length){ box.appendChild(el('div',{class:'acct-conns-empty',text:'No per-user integrations available yet. When your admin enables one, it appears here to connect with your own account.'})); return; }
      list.forEach(function(c){
        var badge = el('span', {class:'acct-conn-badge '+(c.connected?'on':'off'), text: c.connected?'Connected':'Not connected'});
        var head = el('div', {class:'acct-conn-head'}, [el('span',{class:'acct-conn-name',text:c.name}), badge]);
        var card = el('div', {class:'acct-conn'}, [head]);
        if (c.description) card.appendChild(el('div',{class:'acct-conn-desc',text:c.description}));
        var row = el('div', {class:'acct-conn-row'});
        if (c.oauth){
          if (c.connect_url){
            var conn = el('button', {class:'ui-row-btn primary', text: c.connected?'Reconnect':'Connect'});
            conn.addEventListener('click', function(){
              var w = window.open(c.connect_url, 'gohort-connect', 'width=600,height=760');
              if (!w) { window.open(c.connect_url, '_blank'); }
            });
            row.appendChild(conn);
          } else {
            var connA = el('a', {class:'ui-row-btn primary', href:'/account/oauth/start?cred='+encodeURIComponent(c.name), text: c.connected?'Reconnect':'Connect'});
            row.appendChild(connA);
          }
          if (c.connected){
            var d2 = el('button', {class:'ui-row-btn', text:'Disconnect'});
            d2.addEventListener('click', function(){ if(!confirm('Disconnect '+c.name+'? Your authorization is removed.')) return; disconnect(c.name, d2); });
            row.appendChild(d2);
          }
        } else {
          var inp = el('input', {type:'password', placeholder: c.connected?'Replace your key…':'Paste your key / token'});
          var saveBtn = el('button', {class:'ui-row-btn primary', text: c.connected?'Update':'Connect'});
          saveBtn.addEventListener('click', function(){ var v=inp.value.trim(); if(!v){ inp.focus(); return; } save(c.name, v, saveBtn); });
          row.appendChild(inp); row.appendChild(saveBtn);
          if (c.connected){
            var dis = el('button', {class:'ui-row-btn', text:'Disconnect'});
            dis.addEventListener('click', function(){ if(!confirm('Disconnect '+c.name+'? Your stored key is removed.')) return; disconnect(c.name, dis); });
            row.appendChild(dis);
          }
        }
        card.appendChild(row);
        box.appendChild(card);
      });
    }).catch(function(){ box.textContent = 'Could not load connections.'; });
  }
  load();
})();
</script>`
