// Mobile dashboard for phantom: focused on quick toggles and a panic
// button. Lives at /phantom/mobile so the desktop UI at /phantom stays
// untouched. Reuses the existing /api/config and /api/conversations
// endpoints — no new server-side state.

package phantom

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webui"
)

// handleMobileDashboard renders the mobile dashboard page. Auth is
// enforced via the same session middleware as the rest of the phantom
// UI (RequireUser); unauthenticated requests bounce to the login page.
func (T *Phantom) handleMobileDashboard(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
		Title:    "Phantom — Mobile",
		AppName:  "Phantom",
		Prefix:   "/phantom",
		BodyHTML: phantomMobileBody,
		AppCSS:   phantomMobileCSS,
		AppJS:    phantomMobileJS,
	}))
}

// handleMobilePanic is the kill-switch endpoint. Flips the master
// Enabled flag off, AutoReplyAll off, ProactiveEnabled off, and sets
// every conv's AutoReply off. Idempotent — safe to call repeatedly.
func (T *Phantom) handleMobilePanic(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	cfg := defaultConfig(T.DB)
	cfg.Enabled = false
	cfg.AutoReplyAll = false
	cfg.ProactiveEnabled = false
	cfg.SecureAPIEnabled = false
	T.DB.Set(configTable, configKey, cfg)
	// Walk every conv and disable AutoReply.
	convsPaused := 0
	for _, k := range T.DB.Keys(conversationTable) {
		var c Conversation
		if !T.DB.Get(conversationTable, k, &c) {
			continue
		}
		if c.AutoReply {
			c.AutoReply = false
			T.DB.Set(conversationTable, k, c)
			convsPaused++
		}
	}
	Log("[phantom] PANIC button engaged: master + auto-reply-all + proactive + secure-api OFF; %d conversations had auto-reply paused", convsPaused)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":         "panic engaged",
		"convs_paused":   convsPaused,
	})
}

const phantomMobileBody = `
<div class="mobile-wrap">
  <div class="panic-bar">
    <button id="panic-btn" onclick="panic()">PANIC: disable everything</button>
    <span id="panic-status"></span>
  </div>

  <div class="section">
    <h2>Master switches</h2>
    <label class="big-toggle">
      <input type="checkbox" id="m-enabled" onchange="saveConfig()">
      <span>Phantom enabled</span>
    </label>
    <label class="big-toggle">
      <input type="checkbox" id="m-auto-all" onchange="saveConfig()">
      <span>Auto-reply to all conversations</span>
    </label>
    <label class="big-toggle">
      <input type="checkbox" id="m-secure-api" onchange="saveConfig()">
      <span>Allow secure-API tools</span>
    </label>
    <label class="big-toggle">
      <input type="checkbox" id="m-proactive" onchange="saveConfig()">
      <span>Allow proactive messaging</span>
    </label>
  </div>

  <div class="section">
    <h2>Conversations</h2>
    <div id="conv-list" class="conv-list">Loading…</div>
  </div>

  <div class="footer">
    <a href="/phantom/" class="full-config-link">Open full config →</a>
  </div>
</div>
`

const phantomMobileCSS = `
.mobile-wrap { max-width: 600px; margin: 0 auto; padding: 0.6rem; }
.section { background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px; padding: 0.8rem 0.9rem; margin-bottom: 0.8rem; }
.section h2 { font-size: 0.95rem; margin: 0 0 0.6rem 0; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; }

.panic-bar { margin-bottom: 0.8rem; display: flex; flex-direction: column; align-items: stretch; gap: 0.4rem; }
#panic-btn {
  background: #6e1c1c; color: #fff; border: 1px solid #f85149; border-radius: 10px;
  padding: 0.9rem; font-size: 1rem; font-weight: 600; cursor: pointer; letter-spacing: 0.02em;
  width: 100%;
}
#panic-btn:active { background: #f85149; }
#panic-status { font-size: 0.85rem; color: var(--text-mute); text-align: center; }

.big-toggle {
  display: flex; align-items: center; gap: 0.7rem; padding: 0.6rem 0.5rem;
  border-bottom: 1px solid var(--border); cursor: pointer;
}
.big-toggle:last-of-type { border-bottom: none; }
.big-toggle input[type="checkbox"] { width: 1.4rem; height: 1.4rem; flex-shrink: 0; cursor: pointer; }
.big-toggle span { font-size: 0.95rem; color: var(--text); }

.conv-list { display: flex; flex-direction: column; gap: 0.5rem; }
.conv-row {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.55rem 0.7rem; display: flex; align-items: center; gap: 0.6rem;
}
.conv-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 0.92rem; color: var(--text); }
.conv-meta { font-size: 0.7rem; color: var(--text-mute); }
.conv-toggle { flex-shrink: 0; width: 1.3rem; height: 1.3rem; cursor: pointer; }
.conv-tools-btn {
  flex-shrink: 0; padding: 0.3rem 0.6rem; font-size: 0.75rem;
  background: transparent; color: var(--text-mute); border: 1px solid var(--border);
  border-radius: 5px; cursor: pointer;
}
.conv-tools-btn:active { background: var(--bg-2); }

.history-panel {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.6rem 0.7rem; margin-top: 0.5rem; display: flex; flex-direction: column; gap: 0.4rem;
  max-height: 60vh; overflow-y: auto;
}
.history-header { font-size: 0.72rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; padding-bottom: 0.3rem; border-bottom: 1px solid var(--border); }
.history-empty { font-size: 0.85rem; color: var(--text-mute); }
.history-msg { padding: 0.35rem 0.45rem; border-radius: 6px; font-size: 0.85rem; line-height: 1.35; }
.history-msg.msg-user { background: var(--bg-2); }
.history-msg.msg-ai { background: var(--bg-2); border-left: 3px solid var(--accent, #4f8cff); }
.history-who { font-size: 0.7rem; color: var(--text-mute); margin-bottom: 0.15rem; }
.history-body { color: var(--text); white-space: pre-wrap; word-break: break-word; }

.tool-chips { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-top: 0.5rem; }
.tool-chip {
  font-size: 0.75rem; padding: 0.25rem 0.55rem; border-radius: 12px;
  background: var(--bg-1); color: var(--text-mute); border: 1px solid var(--border);
  cursor: pointer; user-select: none;
}
.tool-chip.on { background: var(--accent, #4f8cff); color: #fff; border-color: var(--accent, #4f8cff); }

.footer { margin-top: 1rem; text-align: center; padding-bottom: 1.5rem; }
.full-config-link { font-size: 0.85rem; color: var(--text-mute); text-decoration: none; }
.full-config-link:hover { color: var(--text); }

@media (min-width: 700px) {
  .mobile-wrap { padding: 1rem; }
}
`

const phantomMobileJS = `
var currentConfig = null;
var conversations = [];
var availableTools = [];

function loadAll() {
  Promise.all([
    fetch('api/config').then(function(r){return r.json()}),
    fetch('api/conversations').then(function(r){return r.json()}),
    fetch('api/tools').then(function(r){return r.json()})
  ]).then(function(results){
    currentConfig = results[0] || {};
    conversations = (results[1] && results[1].conversations) || results[1] || [];
    availableTools = results[2] || [];
    paintMaster();
    paintConvs();
  }).catch(function(err){
    document.getElementById('conv-list').textContent = 'Failed to load: ' + err;
  });
}

function paintMaster() {
  document.getElementById('m-enabled').checked = !!currentConfig.enabled;
  document.getElementById('m-auto-all').checked = !!currentConfig.auto_reply_all;
  document.getElementById('m-secure-api').checked = !!currentConfig.secure_api_enabled;
  document.getElementById('m-proactive').checked = !!currentConfig.proactive_enabled;
}

function saveConfig() {
  if (!currentConfig) return;
  currentConfig.enabled = document.getElementById('m-enabled').checked;
  currentConfig.auto_reply_all = document.getElementById('m-auto-all').checked;
  currentConfig.secure_api_enabled = document.getElementById('m-secure-api').checked;
  currentConfig.proactive_enabled = document.getElementById('m-proactive').checked;
  fetch('api/config', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(currentConfig)
  }).then(function(r){
    if (!r.ok) alert('Save failed (HTTP ' + r.status + ')');
  });
}

function paintConvs() {
  var list = document.getElementById('conv-list');
  list.innerHTML = '';
  if (!conversations.length) {
    list.textContent = 'No conversations yet.';
    return;
  }
  // Sort by last activity descending (Updated field).
  conversations.sort(function(a, b){
    return (b.updated || '').localeCompare(a.updated || '');
  });
  conversations.forEach(function(c){
    var row = document.createElement('div');
    row.className = 'conv-row';

    var toggle = document.createElement('input');
    toggle.type = 'checkbox';
    toggle.className = 'conv-toggle';
    toggle.checked = !!c.auto_reply;
    toggle.title = 'Auto-reply for this conversation';
    toggle.addEventListener('change', function(){
      saveConv(c.chat_id, {auto_reply: toggle.checked});
    });
    row.appendChild(toggle);

    var nameWrap = document.createElement('div');
    nameWrap.className = 'conv-name';
    var label = c.display_name || c.handle || c.chat_id;
    nameWrap.textContent = label;
    if (c.updated) {
      var meta = document.createElement('span');
      meta.className = 'conv-meta';
      meta.textContent = ' · ' + relTime(c.updated);
      nameWrap.appendChild(meta);
    }
    row.appendChild(nameWrap);

    var historyBtn = document.createElement('button');
    historyBtn.className = 'conv-tools-btn';
    historyBtn.textContent = 'History';
    historyBtn.addEventListener('click', function(){
      toggleHistoryPanel(c, row);
    });
    row.appendChild(historyBtn);

    var toolsBtn = document.createElement('button');
    toolsBtn.className = 'conv-tools-btn';
    toolsBtn.textContent = 'Tools';
    toolsBtn.addEventListener('click', function(){
      toggleToolsPanel(c, row);
    });
    row.appendChild(toolsBtn);

    list.appendChild(row);
  });
}

// HISTORY_CONTEXT_LIMIT mirrors the recentMessages cap used by
// processMessage (web.go) so the operator sees exactly what the LLM
// has in its context window — no more, no less. If processMessage's
// limit changes, update this constant in lockstep.
var HISTORY_CONTEXT_LIMIT = 20;

function toggleHistoryPanel(conv, row) {
  // Toggle off if a history panel for this row is already open.
  var existing = row.nextElementSibling;
  if (existing && existing.classList.contains('history-panel')) {
    existing.remove();
    return;
  }
  // Close any other open expansion panels (history or tools) first.
  document.querySelectorAll('.history-panel, .tool-chips').forEach(function(el){ el.remove(); });

  var panel = document.createElement('div');
  panel.className = 'history-panel';
  panel.textContent = 'Loading…';
  row.parentNode.insertBefore(panel, row.nextSibling);

  fetch('api/conversation/' + encodeURIComponent(conv.chat_id))
    .then(function(r){ return r.json(); })
    .then(function(msgs){
      panel.innerHTML = '';
      var header = document.createElement('div');
      header.className = 'history-header';
      header.textContent = 'Last ' + HISTORY_CONTEXT_LIMIT + ' messages (LLM context window)';
      panel.appendChild(header);

      if (!msgs || !msgs.length) {
        var empty = document.createElement('div');
        empty.className = 'history-empty';
        empty.textContent = 'No messages yet.';
        panel.appendChild(empty);
        return;
      }

      // Server returns up to 50; trim to LLM context window so the
      // displayed slice matches what the model actually sees.
      var slice = msgs.slice(-HISTORY_CONTEXT_LIMIT);
      slice.forEach(function(m){
        var wrap = document.createElement('div');
        wrap.className = 'history-msg ' + (m.role === 'assistant' ? 'msg-ai' : 'msg-user');
        var label = m.role === 'assistant' ? 'AI' : (m.display_name || m.handle || 'them');
        var who = document.createElement('div');
        who.className = 'history-who';
        who.textContent = label + (m.timestamp ? ' · ' + relTime(m.timestamp) : '');
        wrap.appendChild(who);

        var body = document.createElement('div');
        body.className = 'history-body';
        body.textContent = m.text || '(no text)';
        wrap.appendChild(body);

        panel.appendChild(wrap);
      });
    })
    .catch(function(err){
      panel.textContent = 'Failed to load: ' + err;
    });
}

function toggleToolsPanel(conv, row) {
  // Toggle a chip panel below the row.
  var existing = row.nextElementSibling;
  if (existing && existing.classList.contains('tool-chips')) {
    existing.remove();
    return;
  }
  // Remove any other open expansion panels (history or tools) first.
  document.querySelectorAll('.history-panel, .tool-chips').forEach(function(el){ el.remove(); });

  var panel = document.createElement('div');
  panel.className = 'tool-chips';
  var enabled = conv.enabled_tools || [];
  availableTools.forEach(function(t){
    var chip = document.createElement('span');
    chip.className = 'tool-chip' + (enabled.indexOf(t.name) >= 0 ? ' on' : '');
    chip.textContent = t.name;
    chip.title = t.desc || '';
    chip.addEventListener('click', function(){
      var idx = enabled.indexOf(t.name);
      if (idx >= 0) {
        enabled.splice(idx, 1);
        chip.classList.remove('on');
      } else {
        enabled.push(t.name);
        chip.classList.add('on');
      }
      conv.enabled_tools = enabled;
      saveConv(conv.chat_id, {enabled_tools: enabled});
    });
    panel.appendChild(chip);
  });
  row.parentNode.insertBefore(panel, row.nextSibling);
}

function saveConv(chatID, partial) {
  // Fetch the current full record, merge the partial, save.
  fetch('api/conversation/' + encodeURIComponent(chatID))
    .then(function(r){ return r.json(); })
    .then(function(full){
      Object.assign(full, partial);
      return fetch('api/conversation/' + encodeURIComponent(chatID), {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(full)
      });
    })
    .then(function(r){
      if (!r.ok) alert('Save failed (HTTP ' + r.status + ')');
    });
}

function panic() {
  if (!confirm('Disable everything? Phantom master OFF, all conv auto-reply OFF, secure-API OFF, proactive OFF. Reversible.')) return;
  document.getElementById('panic-status').textContent = 'Engaging…';
  fetch('api/mobile/panic', {method: 'POST'})
    .then(function(r){ return r.json(); })
    .then(function(d){
      document.getElementById('panic-status').textContent = 'Engaged. ' + (d.convs_paused || 0) + ' conversations paused. Reload to confirm.';
      loadAll();
    })
    .catch(function(err){
      document.getElementById('panic-status').textContent = 'Failed: ' + err;
    });
}

function relTime(iso) {
  if (!iso) return '';
  var t = new Date(iso).getTime();
  if (!t) return '';
  var s = Math.round((Date.now() - t) / 1000);
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.round(s/60) + 'm ago';
  if (s < 86400) return Math.round(s/3600) + 'h ago';
  return Math.round(s/86400) + 'd ago';
}

loadAll();
`
