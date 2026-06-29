// Package account is the per-user settings surface — every logged-in user's own
// page (NOT admin-gated, distinct from the admin Site Settings). It gives the
// previously-scattered per-user preferences a home, and is where per-user
// integrations (individual OAuth connections) get connected/disconnected once
// the per-credential Scope work lands. Reached from the dashboard header
// (Account link next to Logout), not a tile (WebHidden).
package account

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(Account)) }

type Account struct {
	AppCore
}

func (T Account) Name() string         { return "account" }
func (T Account) SystemPrompt() string { return "" }
func (T Account) Desc() string         { return "Apps: your personal account + preferences." }
func (T *Account) Init() error         { return T.Flags.Parse() }
func (T *Account) Main() error {
	Log("account is a dashboard-only app. Start with: gohort serve")
	return nil
}

func (T *Account) WebPath() string { return "/account" }
func (T *Account) WebName() string { return "Account" }
func (T *Account) WebDesc() string { return "Your personal preferences + connected accounts." }

// WebHidden keeps Account off the app grid — it's reached from the dashboard
// header (the Account link next to Logout), not as an app tile competing with
// the real apps.
func (T *Account) WebHidden() bool { return true }

func (T *Account) Routes() {
	T.HandleFunc("/api/prefs", T.handlePrefs)
	T.HandleFunc("/api/connections", T.handleConnections)
	T.HandleFunc("/", T.servePage)
}

// handleConnections GET lists the per-user (per_user-scoped) credentials the user
// can connect, each flagged connected/not; POST sets or clears the user's secret
// for one. The secret value is never returned — only connected status.
func (T *Account) handleConnections(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		conns := Secure().PerUserConnectionsFor(user)
		if conns == nil {
			conns = []PerUserConnection{}
		}
		writeJSON(w, conns)
	case http.MethodPost:
		var body struct {
			Name   string `json:"name"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Guard: only let a user set a secret for a credential that's actually
		// per_user (don't let the account page touch shared/admin secrets).
		c, found := Secure().Load(body.Name)
		if !found || !c.IsPerUser() {
			http.Error(w, "no such per-user integration", http.StatusNotFound)
			return
		}
		if err := Secure().SaveUserSecret(body.Name, user, body.Secret); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- preferences endpoint ----------------------------------------------------

// handlePrefs GET returns the user's personal defaults; POST updates whichever
// fields are present (the FormPanel auto-saves per toggle).
func (T *Account) handlePrefs(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	db := AuthDB()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]bool{
			"notify":            AuthGetNotifyDefault(db, user),
			"private_mode":      AuthGetPrivateMode(db, user),
			"inferred_disabled": AuthGetInferredDisabled(db, user),
		})
	case http.MethodPost:
		var req struct {
			Notify           *bool `json:"notify,omitempty"`
			PrivateMode      *bool `json:"private_mode,omitempty"`
			InferredDisabled *bool `json:"inferred_disabled,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Notify != nil {
			AuthSetNotifyDefault(db, user, *req.Notify)
		}
		if req.PrivateMode != nil {
			AuthSetPrivateMode(db, user, *req.PrivateMode)
		}
		if req.InferredDisabled != nil {
			AuthSetInferredDisabled(db, user, *req.InferredDisabled)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- page --------------------------------------------------------------------

func (T *Account) servePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	ui.Page{
		Title:     "Account",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "640px",
		Sections: []ui.Section{
			{
				Title:    "Preferences",
				Subtitle: "Personal defaults, applied across your agents. Saved as you toggle.",
				Body: ui.FormPanel{
					Source: "api/prefs",
					Fields: []ui.FormField{
						{Field: "notify", Label: "Email notifications", Type: "toggle",
							Help: "Receive email when an agent finishes work for you."},
						{Field: "private_mode", Label: "Private mode by default", Type: "toggle",
							Help: "Mask network-capable tools (web search, fetch, …) by default — keeps turns local. Per-agent overrides still apply."},
						{Field: "inferred_disabled", Label: "Clean mode by default", Type: "toggle",
							Help: "Suppress the Reference Memory layer by default — agents answer fresh from your question + knowledge, without prior derived findings. Per-agent overrides still apply."},
					},
				},
			},
			{
				Title:    "Connected accounts",
				Subtitle: "Integrations you authorize with your own account (read or write as you). Your key is stored encrypted and never shown to the assistant.",
				Body:     ui.Card{HTML: connectionsHTML},
			},
		},
	}.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// connectionsHTML is the Connected-accounts panel: a container the inline script
// fills by fetching api/connections, rendering each per_user integration with a
// connected/not badge + a key field (Save / Disconnect). App-specific, so it
// rides in a Card rather than a core/ui primitive. The Card renderer re-executes
// this <script>.
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
  function el(t, a, k){ var n=document.createElement(t); if(a) for(var x in a){ if(x==='text') n.textContent=a[x]; else if(x==='class') n.className=a[x]; else n.setAttribute(x,a[x]); } (k||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function save(name, secret, btn, label){
    btn.disabled = true; var orig = btn.textContent; btn.textContent = '…';
    fetch('api/connections', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify({name:name, secret:secret})})
      .then(function(r){ if(!r.ok && r.status!==204) throw new Error('HTTP '+r.status); load(); })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; alert('Failed: '+(e&&e.message||e)); });
  }
  function load(){
    fetch('api/connections', {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(list){
      box.innerHTML = '';
      if (!list || !list.length){ box.appendChild(el('div',{class:'acct-conns-empty',text:'No per-user integrations available yet. When your admin enables one, it appears here to connect with your own account.'})); return; }
      list.forEach(function(c){
        var badge = el('span', {class:'acct-conn-badge '+(c.connected?'on':'off'), text: c.connected?'Connected':'Not connected'});
        var head = el('div', {class:'acct-conn-head'}, [el('span',{class:'acct-conn-name',text:c.name}), badge]);
        var card = el('div', {class:'acct-conn'}, [head]);
        if (c.description) card.appendChild(el('div',{class:'acct-conn-desc',text:c.description}));
        var inp = el('input', {type:'password', placeholder: c.connected?'Replace your key…':'Paste your key / token'});
        var saveBtn = el('button', {class:'ui-row-btn primary', text: c.connected?'Update':'Connect'});
        saveBtn.addEventListener('click', function(){ var v=inp.value.trim(); if(!v){ inp.focus(); return; } save(c.name, v, saveBtn); });
        var row = el('div', {class:'acct-conn-row'}, [inp, saveBtn]);
        if (c.connected){
          var dis = el('button', {class:'ui-row-btn', text:'Disconnect'});
          dis.addEventListener('click', function(){ if(!confirm('Disconnect '+c.name+'? Your stored key is removed.')) return; save(c.name, '', dis); });
          row.appendChild(dis);
        }
        card.appendChild(row);
        box.appendChild(card);
      });
    }).catch(function(){ box.textContent = 'Could not load connections.'; });
  }
  load();
})();
</script>`
