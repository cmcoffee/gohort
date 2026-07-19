package gateways

// connectionsHTML is the one Gateways surface that stays a hand-rolled Card: the
// per-user OAuth/MCP connect flow (consent popups, per-provider key entry) is
// genuinely custom behavior the declarative table primitives don't express. The
// credentials / tools / global-tools sections use ui.Table + FormPanel instead.
//
// Its OAuth/MCP consent + callback endpoints stay registered on /account for
// redirect-URI stability, so this card calls them by ABSOLUTE path even though it
// renders under /gateways.
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
