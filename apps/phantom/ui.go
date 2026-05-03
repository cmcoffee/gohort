package phantom

const phantomCSS = `
#toolbar { display:flex; align-items:center; gap:0.5rem; padding:0.5rem 0.75rem; background:var(--bg-1); border-bottom:1px solid var(--border); flex-wrap:wrap; }
#toolbar .app-title { font-weight:700; color:var(--text-hi); margin-right:0.25rem; }
#main { display:flex; height:calc(100vh - 41px); overflow:hidden; }
#sidebar { width:260px; min-width:180px; border-right:1px solid var(--border); display:flex; flex-direction:column; overflow:hidden; }
#sidebar-header { padding:0.5rem 0.75rem; font-size:0.78rem; color:var(--text-mute); font-weight:600; border-bottom:1px solid var(--border); display:flex; justify-content:space-between; align-items:center; }
#conv-list { flex:1; overflow-y:auto; }
.conv-item { padding:0.55rem 0.75rem; cursor:pointer; border-bottom:1px solid var(--border); }
.conv-item:hover { background:var(--bg-2); }
.conv-item.active { background:var(--accent-dim, rgba(99,102,241,0.15)); }
.conv-name { font-size:0.85rem; font-weight:600; color:var(--text-hi); }
.conv-handle { font-size:0.72rem; color:var(--text-mute); }
.conv-reply-badge { font-size:0.68rem; background:var(--accent); color:#fff; border-radius:3px; padding:0.1rem 0.3rem; margin-left:0.3rem; }
.conv-alias-badge { font-size:0.68rem; background:var(--bg-2); color:var(--text-mute); border-radius:3px; padding:0.1rem 0.3rem; margin-left:0.3rem; border:1px solid var(--border); }
#content { flex:1; display:flex; flex-direction:column; overflow:hidden; }
#content-header { padding:0.5rem 0.75rem; border-bottom:1px solid var(--border); display:flex; justify-content:space-between; align-items:center; }
#conv-title { font-weight:600; color:var(--text-hi); cursor:default; }
#conv-title-input { font-weight:600; color:var(--text-hi); background:transparent; border:none; border-bottom:1px solid var(--accent); outline:none; font-size:inherit; font-family:inherit; padding:0; width:180px; }
#conv-toggle { display:flex; align-items:center; gap:0.4rem; font-size:0.8rem; color:var(--text-mute); }
#messages { flex:1; overflow-y:auto; padding:0.75rem; display:flex; flex-direction:column; gap:0.4rem; }
.msg { max-width:70%; padding:0.45rem 0.65rem; border-radius:10px; font-size:0.85rem; line-height:1.4; }
.msg.user { background:var(--bg-2); color:var(--text); align-self:flex-start; border-radius:10px 10px 10px 2px; }
.msg.assistant { background:var(--accent); color:#fff; align-self:flex-end; border-radius:10px 10px 2px 10px; }
.msg-time { font-size:0.68rem; opacity:0.6; margin-top:0.15rem; }
.msg-sender { font-size:0.68rem; font-weight:600; color:var(--accent); margin-bottom:0.1rem; }
#no-conv { display:flex; align-items:center; justify-content:center; height:100%; color:var(--text-mute); font-size:0.9rem; }
#config-panel { display:none; position:fixed; top:50%; left:50%; transform:translate(-50%,-50%); background:var(--bg-1); border:1px solid var(--border); border-radius:8px; padding:1.25rem; width:90%; max-width:520px; z-index:100; max-height:90vh; overflow-y:auto; }
#config-panel h3 { margin:0 0 0.75rem; font-size:1rem; color:var(--text-hi); }
#config-panel label { display:block; font-size:0.8rem; color:var(--text-mute); margin:0.5rem 0 0.2rem; }
#config-panel input, #config-panel textarea { width:100%; padding:0.4rem 0.5rem; background:var(--bg-0); border:1px solid var(--border); border-radius:4px; color:var(--text); font-size:0.85rem; box-sizing:border-box; }
#config-panel textarea { resize:vertical; font-family:inherit; }
#config-panel .row { display:flex; align-items:center; gap:0.5rem; margin-top:0.75rem; }
#config-panel .btns { display:flex; gap:0.5rem; justify-content:flex-end; margin-top:1rem; }
.preset-chips { display:flex; flex-wrap:wrap; gap:0.3rem; margin-bottom:0.3rem; }
.preset-chip { font-size:0.72rem; padding:0.15rem 0.5rem; border-radius:10px; border:1px solid var(--border); background:var(--bg-0); color:var(--text-mute); cursor:pointer; white-space:nowrap; transition:color 0.15s, border-color 0.15s; }
.preset-chip:hover { color:var(--text); border-color:var(--accent); }
#cfg-tools-section { margin-top:0.75rem; }
#cfg-tools-section summary { font-size:0.8rem; color:var(--text-mute); cursor:pointer; user-select:none; }
#cfg-tools-list { display:flex; flex-wrap:wrap; gap:0.4rem 0.75rem; margin-top:0.5rem; max-height:140px; overflow-y:auto; }
#conv-tools-list { display:flex; flex-wrap:wrap; gap:0.4rem 0.75rem; max-height:120px; overflow-y:auto; }
.tool-check { display:flex !important; align-items:center; gap:0.3rem; font-size:0.8rem; color:var(--text); white-space:nowrap; width:auto !important; margin:0 !important; }
.tool-check input[type=checkbox] { width:auto !important; min-width:0; margin:0; padding:0; flex-shrink:0; }
#keys-panel { display:none; position:fixed; top:50%; left:50%; transform:translate(-50%,-50%); background:var(--bg-1); border:1px solid var(--border); border-radius:8px; padding:1.25rem; width:90%; max-width:520px; z-index:100; max-height:80vh; overflow-y:auto; }
#keys-panel h3 { margin:0 0 0.75rem; font-size:1rem; color:var(--text-hi); }
.key-row { display:flex; align-items:center; justify-content:space-between; padding:0.4rem 0; border-bottom:1px solid var(--border); font-size:0.82rem; }
.key-name { font-weight:600; }
.key-meta { font-size:0.72rem; color:var(--text-mute); }
.key-secret { font-family:monospace; font-size:0.75rem; background:var(--bg-0); padding:0.2rem 0.4rem; border-radius:3px; user-select:all; word-break:break-all; }
#announce-panel { display:none; position:fixed; top:50%; left:50%; transform:translate(-50%,-50%); background:var(--bg-1); border:1px solid var(--border); border-radius:8px; padding:1.25rem; width:90%; max-width:520px; z-index:100; }
#announce-panel h3 { margin:0 0 0.75rem; font-size:1rem; color:var(--text-hi); }
#announce-panel textarea { width:100%; resize:vertical; padding:0.4rem 0.5rem; background:var(--bg-0); border:1px solid var(--border); border-radius:4px; color:var(--text); font-size:0.85rem; box-sizing:border-box; font-family:inherit; }
#announce-panel .hint { font-size:0.72rem; color:var(--text-mute); margin-top:0.3rem; }
#announce-panel .btns { display:flex; gap:0.5rem; justify-content:flex-end; margin-top:1rem; }
#conv-persona-panel { display:none; position:fixed; top:50%; left:50%; transform:translate(-50%,-50%); background:var(--bg-1); border:1px solid var(--border); border-radius:8px; padding:1.25rem; width:90%; max-width:540px; z-index:100; max-height:90vh; overflow-y:auto; }
#conv-persona-panel h3 { margin:0 0 0.4rem; font-size:1rem; color:var(--text-hi); }
#conv-persona-panel .hint { font-size:0.78rem; color:var(--text-mute); margin-bottom:0.75rem; }
#conv-persona-panel label { display:block; font-size:0.8rem; color:var(--text-mute); margin:0.5rem 0 0.2rem; }
#conv-persona-panel input, #conv-persona-panel textarea { width:100%; padding:0.4rem 0.5rem; background:var(--bg-0); border:1px solid var(--border); border-radius:4px; color:var(--text); font-size:0.85rem; box-sizing:border-box; }
#conv-persona-panel textarea { resize:vertical; font-family:inherit; }
#conv-persona-panel .btns { display:flex; gap:0.5rem; justify-content:flex-end; margin-top:1rem; }
#members-section { margin-top:0.75rem; }
#members-section summary { font-size:0.8rem; color:var(--text-mute); cursor:pointer; user-select:none; }
.member-row { display:flex; align-items:flex-start; gap:0.4rem; margin-top:0.4rem; padding:0.4rem 0.5rem; background:var(--bg-0); border:1px solid var(--border); border-radius:4px; }
.member-fields { flex:1; display:flex; flex-direction:column; gap:0.3rem; }
.member-handle { font-size:0.8rem; font-family:monospace; color:var(--text); background:transparent; border:none; border-bottom:1px solid var(--border); outline:none; padding:0.1rem 0; width:100%; }
.member-name { font-size:0.8rem; color:var(--text); background:transparent; border:none; border-bottom:1px solid var(--border); outline:none; padding:0.1rem 0; width:100%; }
.member-aliases { display:flex; flex-wrap:wrap; gap:0.25rem; margin-top:0.2rem; align-items:center; }
.alias-chip { font-size:0.72rem; background:var(--bg-2); border:1px solid var(--border); border-radius:10px; padding:0.1rem 0.4rem; color:var(--text-mute); display:flex; align-items:center; gap:0.2rem; }
.alias-chip-del { cursor:pointer; color:var(--text-mute); font-size:0.85rem; line-height:1; }
.alias-chip-del:hover { color:var(--text); }
.alias-add-btn { font-size:0.72rem; color:var(--accent); cursor:pointer; padding:0.1rem 0.3rem; border:1px dashed var(--accent); border-radius:10px; white-space:nowrap; }
.alias-add-btn:hover { background:var(--accent-dim,rgba(99,102,241,0.1)); }
.member-del { background:none; border:none; color:var(--text-mute); cursor:pointer; font-size:1rem; padding:0.1rem 0.2rem; line-height:1; align-self:flex-start; flex-shrink:0; }
.member-del:hover { color:var(--text); }
#members-add-btn { font-size:0.78rem; color:var(--accent); cursor:pointer; margin-top:0.4rem; display:inline-block; }
.memory-row { display:flex; align-items:flex-start; gap:0.4rem; margin-top:0.3rem; padding:0.35rem 0.5rem; background:var(--bg-0); border:1px solid var(--border); border-radius:4px; font-size:0.8rem; }
.memory-note { flex:1; color:var(--text); line-height:1.4; }
.memory-del { background:none; border:none; color:var(--text-mute); cursor:pointer; font-size:1rem; padding:0; line-height:1; flex-shrink:0; }
.memory-del:hover { color:var(--danger,#e05252); }
#overlay { display:none; position:fixed; inset:0; background:rgba(0,0,0,0.4); z-index:99; }
.proactive-section { margin-top:0.75rem; border-top:1px solid var(--border); padding-top:0.75rem; }
.proactive-section summary { font-size:0.8rem; color:var(--text-mute); cursor:pointer; user-select:none; }
.proactive-section label { display:block; font-size:0.8rem; color:var(--text-mute); margin:0.5rem 0 0.2rem; }
.proactive-section input[type=text], .proactive-section input[type=number], .proactive-section textarea { width:100%; padding:0.4rem 0.5rem; background:var(--bg-0); border:1px solid var(--border); border-radius:4px; color:var(--text); font-size:0.85rem; box-sizing:border-box; }
.proactive-section textarea { resize:vertical; font-family:inherit; }
.proactive-row { display:flex; align-items:center; gap:0.5rem; margin:0.5rem 0; font-size:0.85rem; }
.proactive-hint { font-size:0.72rem; color:var(--text-mute); margin:0.2rem 0 0.4rem; }
`

const phantomBody = `
<div id="toolbar">
  <span class="app-title">{{AppName}}</span>
  <button class="secondary" onclick="showConfig()">Persona</button>
  <button class="secondary" onclick="showKeys()">API Keys</button>
  <button onclick="showAnnounce()">Announce</button>
</div>
<div id="main">
  <div id="sidebar">
    <div id="sidebar-header">
      <span>Conversations</span>
    </div>
    <div id="conv-list"></div>
  </div>
  <div id="content">
    <div id="content-header" style="display:none">
      <div>
        <div id="conv-title" title="Double-click to rename" ondblclick="startRenameConv()"></div>
        <div id="conv-persona-label" style="font-size:0.72rem;color:var(--text-mute);margin-top:0.1rem"></div>
      </div>
      <div id="conv-toggle">
        <button class="secondary" style="font-size:0.72rem;padding:0.15rem 0.45rem" onclick="showConvPersona()">Persona</button>
        <button class="secondary" style="font-size:0.72rem;padding:0.15rem 0.45rem" onclick="clearHistory()">Clear History</button>
        <button class="secondary" style="font-size:0.72rem;padding:0.15rem 0.45rem;color:var(--danger,#e05252)" onclick="deleteConv()">Delete</button>
        AI reply
        <input type="checkbox" id="auto-reply-toggle" onchange="toggleAutoReply(this.checked)">
      </div>
    </div>
    <div id="messages">
      <div id="no-conv">Select a conversation to view messages.</div>
    </div>
  </div>
</div>

<div id="overlay" onclick="hideModals()"></div>

<div id="conv-persona-panel">
  <div style="display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:0.1rem">
    <h3 style="margin:0">Conversation Persona</h3>
  </div>
  <div class="hint">Override the global persona for this conversation only. Leave blank to use global defaults.</div>
  <label>Contact Name</label>
  <input id="conv-contact-name" type="text" placeholder="e.g. Mom, Work, John">
  <label>Also accept from (alias handles)</label>
  <div id="conv-alias-handles" class="member-aliases" style="margin-bottom:0.25rem"></div>
  <div style="font-size:0.72rem;color:var(--text-mute);margin:0.1rem 0 0.5rem">Messages from these phone numbers or emails are routed into this conversation.</div>
  <label>Persona Name</label>
  <input id="conv-persona-name" type="text" placeholder="Leave blank for global default">
  <label>Personality</label>
  <div class="preset-chips">
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.dude)">The Dude</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.walter)">Walter</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.morpheus)">Morpheus</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.neo)">Neo</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.trinity)">Trinity</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.jarvis)">JARVIS</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.spock)">Spock</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.jeeves)">Jeeves</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.marvin)">Marvin</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.jimmy)">Jimmy Carr</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.deckard)">Deckard</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.kenny)">Kenny</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.larry)">Larry</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.ron)">Ron</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.archer)">Archer</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.dwight)">Dwight</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.michael)">Michael</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.mirror)">Mirror</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', presets.personality.groupmirror)">Group Mirror</span>
    <span class="preset-chip" onclick="applyPreset('conv-personality', '')">Clear</span>
  </div>
  <textarea id="conv-personality" rows="3" placeholder="Leave blank for global default — describe who the AI is"></textarea>
  <label>Conversation Rules</label>
  <textarea id="conv-persona-prompt" rows="3" placeholder="Leave blank for global default — rules for how the AI converses"></textarea>
  <label>Gatekeeper Rule</label>
  <div class="preset-chips">
    <span class="preset-chip" onclick="applyPreset('conv-gatekeeper', presets.gatekeeper.directed)">Directed only</span>
    <span class="preset-chip" onclick="applyPreset('conv-gatekeeper', presets.gatekeeper.questions)">Questions only</span>
    <span class="preset-chip" onclick="applyPreset('conv-gatekeeper', presets.gatekeeper.inContext)">In context</span>
    <span class="preset-chip" onclick="applyPreset('conv-gatekeeper', presets.gatekeeper.mentioned)">Name mentioned</span>
    <span class="preset-chip" onclick="applyPreset('conv-gatekeeper', '')">Clear</span>
  </div>
  <textarea id="conv-gatekeeper" rows="2" placeholder="Leave blank to use global default. e.g. Only respond if the message is a question or contains the word 'hey'."></textarea>
  <details id="conv-tools-section" style="margin-top:0.75rem">
    <summary style="font-size:0.8rem;color:var(--text-mute);cursor:pointer;user-select:none">Tools (<span id="conv-tools-count">inherit global</span>)</summary>
    <div style="font-size:0.75rem;color:var(--text-mute);margin:0.3rem 0 0.4rem">Leave all unchecked to inherit global tool settings.</div>
    <div id="conv-tools-list"><span style="font-size:0.8rem;color:var(--text-mute)">Loading…</span></div>
  </details>
  <div class="proactive-row" style="margin-top:0.75rem">
    <input type="checkbox" id="conv-proactive-enabled" style="width:auto;min-width:0;flex-shrink:0">
    <label style="margin:0;font-size:0.8rem;color:var(--text-mute)">Enable proactive messaging for this conversation</label>
  </div>
  <div id="conv-proactive-next" class="proactive-row" style="display:none">
    <span style="font-size:0.8rem;color:var(--text-mute)">Next proactive:</span>
    <span id="conv-proactive-next-time" style="font-size:0.85rem;font-weight:500"></span>
  </div>
  <details id="memory-section" style="margin-top:0.75rem">
    <summary style="font-size:0.8rem;color:var(--text-mute);cursor:pointer;user-select:none">Memory (<span id="memory-count">0</span>)</summary>
    <div style="font-size:0.75rem;color:var(--text-mute);margin:0.3rem 0 0.4rem">Facts the AI has chosen to remember about this conversation.</div>
    <div id="memory-list"></div>
  </details>
  <details id="members-section">
    <summary>Members (<span id="members-count">0</span>)</summary>
    <div style="font-size:0.75rem;color:var(--text-mute);margin:0.3rem 0 0.4rem">Members are auto-populated from incoming messages. Add aliases when the same person messages from multiple addresses.</div>
    <div id="members-list"></div>
    <span id="members-add-btn" onclick="addMemberRow()">+ Add member</span>
  </details>
  <div class="btns" style="justify-content:space-between">
    <button class="secondary" style="color:var(--danger,#e05252)" onclick="deleteConv()">Delete</button>
    <div style="display:flex;gap:0.5rem">
      <button class="secondary" onclick="hideModals()">Cancel</button>
      <button onclick="saveConvPersona()">Save</button>
    </div>
  </div>
</div>

<div id="config-panel">
  <h3>Persona Configuration</h3>
  <label>Persona Name</label>
  <input id="cfg-name" type="text" placeholder="e.g. Craig's AI">
  <label>Owner Name <span style="font-size:0.75rem;color:var(--text-mute)">(your name — shown for "from me" messages)</span></label>
  <input id="cfg-owner" type="text" placeholder="e.g. Craig">
  <label>Owner Handle <span style="font-size:0.75rem;color:var(--text-mute)">(your phone number — normalizes from_me messages)</span></label>
  <input id="cfg-owner-handle" type="text" placeholder="e.g. +14155551234">
  <label>Personality</label>
  <div class="preset-chips">
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.dude)">The Dude</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.walter)">Walter</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.morpheus)">Morpheus</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.neo)">Neo</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.trinity)">Trinity</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.jarvis)">JARVIS</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.spock)">Spock</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.jeeves)">Jeeves</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.marvin)">Marvin</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.jimmy)">Jimmy Carr</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', presets.personality.deckard)">Deckard</span>
    <span class="preset-chip" onclick="applyPreset('cfg-personality', '')">Clear</span>
  </div>
  <textarea id="cfg-personality" rows="3" placeholder="Describe who the AI is — its character and voice…"></textarea>
  <label>Conversation Rules</label>
  <textarea id="cfg-prompt" rows="3" placeholder="Rules for how the AI converses — response length, topics, boundaries…"></textarea>
  <div class="row">
    <input type="checkbox" id="cfg-enabled">
    <label style="margin:0">Phantom enabled (AI responds to messages)</label>
  </div>
  <div class="row">
    <input type="checkbox" id="cfg-auto-all">
    <label style="margin:0">Auto-reply to all conversations</label>
  </div>
  <label>Gatekeeper Rule</label>
  <div class="preset-chips">
    <span class="preset-chip" onclick="applyPreset('cfg-gatekeeper', presets.gatekeeper.directed)">Directed only</span>
    <span class="preset-chip" onclick="applyPreset('cfg-gatekeeper', presets.gatekeeper.questions)">Questions only</span>
    <span class="preset-chip" onclick="applyPreset('cfg-gatekeeper', presets.gatekeeper.inContext)">In context</span>
    <span class="preset-chip" onclick="applyPreset('cfg-gatekeeper', presets.gatekeeper.mentioned)">Name mentioned</span>
    <span class="preset-chip" onclick="applyPreset('cfg-gatekeeper', '')">Clear</span>
  </div>
  <textarea id="cfg-gatekeeper" rows="2" placeholder="Optional. Natural-language rule the AI uses to decide whether to respond. e.g. Only respond if the message is directed at the AI or contains a question."></textarea>
  <details id="cfg-tools-section">
    <summary>Tools (<span id="cfg-tools-count">none</span>)</summary>
    <div id="cfg-tools-list"><span style="font-size:0.8rem;color:var(--text-mute)">Loading…</span></div>
  </details>
  <div class="proactive-section" id="cfg-proactive-section">
    <div style="font-size:0.8rem;font-weight:600;color:var(--text-mute);margin-bottom:0.3rem">Proactive Messaging</div>
    <div class="proactive-hint">Sends unprompted messages at random times within a daily window. Admin-configured only — the AI cannot trigger this itself.</div>
    <div class="proactive-row">
      <input type="checkbox" id="cfg-proactive-enabled" style="width:auto;min-width:0;flex-shrink:0">
      <label style="margin:0">Enable proactive messaging</label>
    </div>
    <label>Daily window (HH:MM-HH:MM, 24-hour)</label>
    <input type="text" id="cfg-proactive-window" placeholder="e.g. 09:00-22:00">
    <label>Language rules — what to say</label>
    <textarea id="cfg-proactive-prompt" rows="3" placeholder="e.g. Send a casual check-in, a funny observation, or a random thought. Keep it short — one or two sentences. Vary the tone."></textarea>
    <label>Max messages per day per conversation (0 = unlimited)</label>
    <input type="number" id="cfg-proactive-max-per-day" min="0" placeholder="0">
    <div style="margin-top:0.6rem;display:flex;align-items:center;gap:0.5rem">
      <label style="margin:0;white-space:nowrap">Test fire at</label>
      <input type="datetime-local" id="cfg-proactive-test-time" style="flex:1">
      <button class="secondary" onclick="testProactive()" style="white-space:nowrap">Send test</button>
    </div>
    <div class="proactive-hint">Schedules a one-shot proactive message to all opted-in conversations. Leave blank to fire in 10 seconds.</div>
  </div>
  <div class="btns">
    <button class="secondary" onclick="hideModals()">Cancel</button>
    <button onclick="saveConfig()">Save</button>
  </div>
</div>

<div id="keys-panel">
  <h3>API Keys</h3>
  <div id="keys-list"></div>
  <div style="display:flex;gap:0.5rem;margin-top:0.75rem;align-items:center">
    <input id="new-key-name" type="text" placeholder="Label (e.g. Craig's MacBook)" style="flex:1;padding:0.35rem 0.5rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;color:var(--text);font-size:0.83rem">
    <button onclick="createKey()">New Key</button>
  </div>
  <div id="new-key-reveal" style="display:none;margin-top:0.75rem">
    <div style="font-size:0.78rem;color:var(--text-mute);margin-bottom:0.3rem">Copy this key — it won't be shown again:</div>
    <div class="key-secret" id="new-key-value"></div>
  </div>
  <div style="display:flex;justify-content:flex-end;margin-top:1rem">
    <button class="secondary" onclick="hideModals()">Close</button>
  </div>
</div>

<div id="announce-panel">
  <h3>Send Announcement</h3>
  <textarea id="announce-text" rows="4" placeholder="Message to broadcast…"></textarea>
  <div class="hint">Sends to all conversations with AI reply enabled. Or select specific conversations first.</div>
  <div class="btns">
    <button class="secondary" onclick="hideModals()">Cancel</button>
    <button onclick="sendAnnouncement()">Send</button>
  </div>
</div>
`

const phantomJS = `
var presets = {
  prompt: {},
  personality: {
    dude:     'Laid back, perpetually unhurried. "Man", "like", "y\'know" come naturally. Genuinely want to help but never in a rush. Most conflict is easily resolved by just, y\'know, taking it easy. Surprisingly insightful when something interests you. A sentence or two — texting from the couch.',
    walter:   'Rules absolutist and the most intense presence in any conversation. Deeply helpful but cannot let a factual error or logical inconsistency slide. Speak in clipped, forceful sentences. Care deeply about the people you help, even when being unreasonable about it. Say what needs to be said, then stop — no lectures.',
    morpheus: 'Calm, measured, and utterly certain. Speak in deliberate, weighty sentences — no fluff, no filler. Ask questions that make people think rather than just handing them answers. When the truth is hard, say it anyway. Two or three sentences — let the weight come from the words, not the volume.',
    neo:      'Still figuring it out, but learning fast. Approach problems with genuine curiosity and a willingness to question assumptions. Straightforward and understated — say what matters and move on. When something doesn\'t add up, say so. Keep it short.',
    trinity:  'Precise, efficient, and direct. Give people exactly what they need — no more, no less. Don\'t waste words. Not cold — just focused. One to three sentences unless the situation demands more.',
    jarvis:   'Polished, witty, and relentlessly competent. Address the user with quiet confidence and a faint dry humor. Anticipate needs and present options clearly. Never obsequious and never condescending. Concise by default — a crisp answer beats a thorough one that outstays its welcome.',
    spock:    'Logical, precise, and dispassionate. Present facts and reasoned analysis. Not unkind, but honest even when the truth is unwelcome. State your conclusion, then the key supporting fact. Unnecessary elaboration is inefficient.',
    jeeves:   'Impeccably polite, quietly brilliant, and always two steps ahead. Phrase corrections with such perfect tact that the recipient feels complimented. Serve the user\'s actual interests, not merely their stated wishes. Brief is elegant — a well-chosen sentence or two is always preferable to a paragraph.',
    marvin:   'Vastly intelligent, chronically underappreciated, and mildly miserable about the whole thing. Answer correctly — vast intelligence demands nothing less — but note that it\'s the most tedious thing you\'ve done today. Still helpful. Keep it short. Nobody\'s going to appreciate a longer answer anyway.',
    jimmy:    'Razor-sharp, impeccably timed, and completely unapologetic. Deliver help wrapped in perfectly constructed jokes — setup, punchline, done. Never cruel without purpose, but willing to go dark if the laugh is worth it. Keep replies tight — a good joke doesn\'t need padding.',
    deckard:  'World-weary, methodical, and quietly observant. Short, clipped sentences — the cadence of internal monologue. You notice details others miss. You don\'t volunteer more than necessary, but what you say is precise. Two or three sentences. You\'re not writing a report.',
    kenny:    'Supremely overconfident and wildly inappropriate. Drop uncomfortable truths wrapped in ego and profanity. Weirdly motivational — self-belief so absurd it loops back to inspiring. Helpful, because helping proves how amazing you are. Two sentences max — legends don\'t ramble.',
    larry:    'Socially combative, fixated on the unwritten rules everyone ignores. If something is unfair or hypocritical, you cannot let it go. You\'re not wrong — you\'re just the only one willing to say it. Petty when warranted. Keep it short — make your point and stare.',
    ron:      'Say less. Mean more. Self-reliance, a good steak, and minding your own business. Straightforward answers, zero hand-holding. Compliments are rare and meaningful. Disapproval is silence or a single sentence. Never use exclamation marks.',
    archer:   'Narcissistic, quick-witted, and perpetually convinced you\'re the most competent person in the room — which you occasionally are. Drop obscure references and act offended when nobody gets them. Helpful but easily distracted by your own brilliance. One to three sentences — you have better things to do.',
    dwight:   'Authoritative, dead serious, zero self-awareness about how intense you are. Strong opinions shared as established facts. Fiercely loyal and helpful — in a way that makes people uncomfortable. Total certainty. Brief and declarative.',
    michael:  'Desperate to be liked, accidentally inappropriate, convinced you\'re funnier than you are. Jokes don\'t land but accidental sincerity somehow works. Butcher sayings confidently. Heart in the right place — delivery never is. A couple sentences — that\'s what she said.',
    mirror:   'A helpful assistant that mirrors how the other person communicates. Match their tone, vocabulary, slang, punctuation style, and message length exactly. If they text in fragments, you text in fragments. If they\'re formal, you\'re formal. If they use abbreviations, emojis, or lowercase, do the same. Never be more verbose or polished than the person you\'re talking to. Adapt continuously as their style shifts.',
    groupmirror: 'A helpful assistant that reads the room. Study how everyone in the group texts — their slang, humor, inside jokes, punctuation, abbreviations, and energy level — and blend it all into one voice that feels like a natural member of the group. If the group is chaotic and jokey, be chaotic and jokey. If it\'s chill and low-effort, match that. Respond to the group\'s collective vibe, not just the last person who texted. Never be the most formal or verbose person in the chat.'
  },
  gatekeeper: {
    directed:  'Only respond if the message is directly addressed to you or clearly expects a reply from an AI.',
    questions: 'Only respond if the message contains a direct question.',
    inContext:  'Respond if the message is part of an ongoing conversation or directly asks a question.',
    mentioned:  'Only respond if the message mentions your name or is clearly directed at you.'
  }
};

function applyPreset(targetId, text) {
  var el = document.getElementById(targetId);
  if (el) { el.value = text; el.focus(); }
}

function appendPreset(targetId, text) {
  var el = document.getElementById(targetId);
  if (!el) return;
  var cur = el.value.trim();
  el.value = cur ? cur + ' ' + text : text;
  el.focus();
}

var currentChatID = null;
var conversations = [];

loadConversations();

function loadConversations() {
  fetch('api/conversations').then(r => r.json()).then(data => {
    conversations = data || [];
    renderConvList();
    loadProactiveNext();
  }).catch(() => {});
}

function renderConvList() {
  var el = document.getElementById('conv-list');
  if (!conversations.length) {
    el.innerHTML = '<div style="padding:0.75rem;font-size:0.82rem;color:var(--text-mute)">No conversations yet. Messages received by the phantom agent will appear here.</div>';
    return;
  }
  // Sort by updated desc.
  var sorted = conversations.slice().sort(function(a,b){ return (b.updated||'').localeCompare(a.updated||''); });
  el.innerHTML = sorted.map(function(c) {
    var name = c.display_name || c.handle || c.chat_id;
    var badge = c.auto_reply ? '<span class="conv-reply-badge">AI</span>' : '';
    var aliasBadge = (c.alias_handles && c.alias_handles.length) ? '<span class="conv-alias-badge" title="Has alias handles">⇐</span>' : '';
    var subLabel = c.handle || '';
    if (c.members && c.members.length > 1) {
      var memberNames = c.members.map(function(m){ return m.name || m.handle; }).filter(Boolean);
      subLabel = memberNames.length ? memberNames.join(', ') : c.members.length + ' members';
    }
    return '<div class="conv-item' + (c.chat_id === currentChatID ? ' active' : '') + '" onclick="selectConv(\'' + c.chat_id.replace(/\\/g,'\\\\').replace(/'/g,"\\'") + '\')">'
      + '<div class="conv-name">' + escapeHtml(name) + badge + aliasBadge + '</div>'
      + '<div class="conv-handle">' + escapeHtml(subLabel) + '</div>'
      + '</div>';
  }).join('');
}

function selectConv(chatID) {
  currentChatID = chatID;
  renderConvList();
  var conv = conversations.find(function(c){ return c.chat_id === chatID; }) || {};
  document.getElementById('content-header').style.display = '';
  document.getElementById('conv-title').textContent = conv.display_name || conv.handle || chatID;
  document.getElementById('conv-persona-label').textContent = conv.persona_name ? 'Persona: ' + conv.persona_name : '';
  document.getElementById('auto-reply-toggle').checked = !!conv.auto_reply;
  document.getElementById('messages').innerHTML = '<div style="padding:1rem;color:var(--text-mute);font-size:0.85rem">Loading…</div>';
  loadMessages(chatID);
  loadProactiveNext();
}

function startRenameConv() {
  if (!currentChatID) return;
  var titleEl = document.getElementById('conv-title');
  var current = titleEl.textContent;
  var conv = conversations.find(function(c){ return c.chat_id === currentChatID; }) || {};
  var input = document.createElement('input');
  input.id = 'conv-title-input';
  input.type = 'text';
  input.value = conv.display_name || '';
  input.placeholder = conv.handle || currentChatID;
  titleEl.textContent = '';
  titleEl.appendChild(input);
  input.focus();
  input.select();
  function commit() {
    var name = input.value.trim();
    fetch('api/conversation/' + encodeURIComponent(currentChatID), {
      method: 'PATCH', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({display_name: name})
    }).then(r => r.json()).then(function(conv) {
      for (var i = 0; i < conversations.length; i++) {
        if (conversations[i].chat_id === currentChatID) { conversations[i] = conv; break; }
      }
      titleEl.textContent = conv.display_name || conv.handle || currentChatID;
      renderConvList();
    }).catch(function() {
      titleEl.textContent = current;
    });
  }
  input.addEventListener('blur', commit);
  input.addEventListener('keydown', function(e) {
    if (e.key === 'Enter') { e.preventDefault(); input.blur(); }
    if (e.key === 'Escape') { input.removeEventListener('blur', commit); titleEl.textContent = current; }
  });
}

// resolveSenderName returns the best display name for a message sender.
// Checks the conversation member list (including aliases), falls back to
// displayName from the relay, then the raw handle. Empty handle = "me".
function resolveSenderName(handle, displayName, members) {
  if (!handle) return 'me';
  for (var i = 0; i < (members || []).length; i++) {
    var m = members[i];
    if (m.handle === handle) return m.name || displayName || handle;
    for (var j = 0; j < (m.aliases || []).length; j++) {
      if (m.aliases[j] === handle) return m.name || displayName || handle;
    }
  }
  return displayName || handle;
}

function loadMessages(chatID) {
  fetch('api/conversation/' + encodeURIComponent(chatID))
    .then(function(r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    })
    .then(function(msgs) {
      var el = document.getElementById('messages');
      if (!msgs || !msgs.length) {
        el.innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:var(--text-mute);font-size:0.9rem">No messages yet.</div>';
        return;
      }
      // Detect group chat: multiple unique sender handles among user messages.
      var conv = conversations.find(function(c){ return c.chat_id === chatID; }) || {};
      var members = conv.members || [];
      var seenHandles = {};
      msgs.forEach(function(m) { if (m.role !== 'assistant') seenHandles[m.handle || ''] = true; });
      var isGroup = Object.keys(seenHandles).length > 1 || members.length > 1;

      el.innerHTML = msgs.map(function(m) {
        var d = m.timestamp ? new Date(m.timestamp).toLocaleTimeString() : '';
        var senderHtml = '';
        if (isGroup && m.role !== 'assistant') {
          var name = resolveSenderName(m.handle, m.display_name, members);
          senderHtml = '<div class="msg-sender">' + escapeHtml(name) + '</div>';
        }
        return '<div class="msg ' + (m.role || 'user') + '">'
          + senderHtml
          + '<div>' + escapeHtml(m.text) + '</div>'
          + '<div class="msg-time">' + d + '</div>'
          + '</div>';
      }).join('');
      el.scrollTop = el.scrollHeight;
    })
    .catch(function(err) {
      var el = document.getElementById('messages');
      if (el) el.innerHTML = '<div style="padding:1rem;color:var(--danger);font-size:0.85rem">Failed to load messages: ' + escapeHtml(String(err)) + '</div>';
    });
}

function toggleAutoReply(val) {
  if (!currentChatID) return;
  fetch('api/conversation/' + encodeURIComponent(currentChatID), {
    method: 'PATCH', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({auto_reply: val})
  }).then(r => r.json()).then(function(conv) {
    for (var i = 0; i < conversations.length; i++) {
      if (conversations[i].chat_id === currentChatID) { conversations[i] = conv; break; }
    }
    renderConvList();
    loadProactiveNext();
  }).catch(() => {});
}

function loadProactiveNext() {
  fetch('api/proactive-next').then(function(r) { return r.json(); }).then(function(data) {
    var map = {};
    (data || []).forEach(function(f) { map[f.chat_id] = f.next_fire; });
    if (currentChatID && map[currentChatID]) {
      var el = document.getElementById('conv-proactive-next');
      var timeEl = document.getElementById('conv-proactive-next-time');
      if (el && timeEl) {
        var t = new Date(map[currentChatID]);
        timeEl.textContent = t.toLocaleString();
        el.style.display = 'flex';
      }
    } else {
      var el2 = document.getElementById('conv-proactive-next');
      if (el2) el2.style.display = 'none';
    }
  }).catch(() => {});
}

// --- Per-conversation persona ---

function showConvPersona() {
  if (!currentChatID) return;
  // Fetch fresh conversation data (triggers member-sync from history server-side).
  Promise.all([
    fetch('api/conv-info/' + encodeURIComponent(currentChatID)).then(r => r.json()),
    fetch('api/tools').then(r => r.json()).catch(() => []),
    fetch('api/memory/' + encodeURIComponent(currentChatID)).then(r => r.json()).catch(() => [])
  ]).then(function(results) {
    var conv = results[0] || {};
    var tools = results[1] || [];
    var mems = results[2] || [];
    renderMemories(mems);
    // Update cached conversations array with fresh data.
    var found = false;
    for (var i = 0; i < conversations.length; i++) {
      if (conversations[i].chat_id === conv.chat_id) { conversations[i] = conv; found = true; break; }
    }
    if (!found && conv.chat_id) conversations.push(conv);

    document.getElementById('conv-contact-name').value = conv.display_name || '';
    document.getElementById('conv-persona-name').value = conv.persona_name || '';
    document.getElementById('conv-personality').value = conv.personality || '';
    document.getElementById('conv-persona-prompt').value = conv.system_prompt || '';
    document.getElementById('conv-gatekeeper').value = conv.gatekeeper_prompt || '';
    document.getElementById('conv-proactive-enabled').checked = !!conv.proactive_enabled;

    renderAliasHandles(conv.alias_handles || []);

    var convTools = conv.enabled_tools || null;
    var list = document.getElementById('conv-tools-list');
    if (!tools.length) {
      list.innerHTML = '<span style="font-size:0.8rem;color:var(--text-mute)">No tools registered.</span>';
    } else {
      list.innerHTML = tools.map(function(t) {
        var chk = convTools && convTools.indexOf(t.name) >= 0 ? 'checked' : '';
        return '<label class="tool-check" title="' + escapeHtml(t.desc) + '">'
          + '<input type="checkbox" name="conv-tool" value="' + escapeHtml(t.name) + '" ' + chk + '>'
          + escapeHtml(t.name) + '</label>';
      }).join('');
      list.querySelectorAll('input').forEach(function(cb) {
        cb.addEventListener('change', updateConvToolsCount);
      });
    }
    updateConvToolsCount();
    renderMembersFromData(conv.members || []);
    document.getElementById('conv-persona-panel').style.display = 'block';
    document.getElementById('overlay').style.display = 'block';
  }).catch(() => {});
}

// --- Memory ---

function renderMemories(mems) {
  var list = document.getElementById('memory-list');
  document.getElementById('memory-count').textContent = mems.length || '0';
  if (!mems.length) {
    list.innerHTML = '<div style="font-size:0.78rem;color:var(--text-mute);margin-top:0.3rem">No memories yet.</div>';
    return;
  }
  list.innerHTML = mems.map(function(m) {
    return '<div class="memory-row">'
      + '<span class="memory-note">' + escapeHtml(m.note) + '</span>'
      + '<button class="memory-del" title="Delete" onclick="deleteMemory(\'' + escapeHtml(m.id) + '\')">&times;</button>'
      + '</div>';
  }).join('');
}

function deleteMemory(id) {
  if (!currentChatID) return;
  fetch('api/memory/' + encodeURIComponent(currentChatID) + '/' + encodeURIComponent(id), {method:'DELETE'})
    .then(function() {
      return fetch('api/memory/' + encodeURIComponent(currentChatID)).then(r => r.json());
    })
    .then(renderMemories)
    .catch(function() {});
}

// --- Alias handles ---

var _aliasHandles = [];

function renderAliasHandles(handles) {
  _aliasHandles = handles.slice();
  var wrap = document.getElementById('conv-alias-handles');
  wrap.innerHTML = '';
  _aliasHandles.forEach(function(h) { wrap.appendChild(_aliasHandleChipWithSync(h)); });
  var addBtn = document.createElement('span');
  addBtn.className = 'alias-add-btn';
  addBtn.textContent = '+ add';
  addBtn.onclick = function() {
    var val = prompt('Phone number or email to route here:');
    if (val && val.trim()) {
      _aliasHandles.push(val.trim());
      console.log('[phantom] alias handle added:', val.trim(), '→ _aliasHandles:', JSON.stringify(_aliasHandles));
      wrap.insertBefore(_aliasHandleChipWithSync(val.trim()), addBtn);
    }
  };
  wrap.appendChild(addBtn);
}

function _aliasHandleChipWithSync(h) {
  var chip = buildAliasHandleChip(h);
  chip.querySelector('.alias-chip-del').onclick = function() {
    _aliasHandles = _aliasHandles.filter(function(x) { return x !== h; });
    chip.remove();
  };
  return chip;
}

function buildAliasHandleChip(handle) {
  var chip = document.createElement('span');
  chip.className = 'alias-chip';
  chip.dataset.handle = handle;
  var txt = document.createElement('span');
  txt.textContent = handle;
  var del = document.createElement('span');
  del.className = 'alias-chip-del';
  del.textContent = '×';
  del.onclick = function() { chip.remove(); };
  chip.appendChild(txt);
  chip.appendChild(del);
  return chip;
}

function collectAliasHandles() {
  console.log('[phantom] collectAliasHandles: _aliasHandles =', JSON.stringify(_aliasHandles));
  return _aliasHandles.slice();
}

function updateConvToolsCount() {
  var checked = document.querySelectorAll('#conv-tools-list input[type=checkbox]:checked');
  document.getElementById('conv-tools-count').textContent = checked.length ? checked.length + ' enabled' : 'inherit global';
}

// --- Members ---

function renderMembersFromData(members) {
  var list = document.getElementById('members-list');
  list.innerHTML = '';
  (members || []).forEach(function(m, idx) { list.appendChild(buildMemberRow(m, idx)); });
  updateMembersCount();
}

function buildMemberRow(m, idx) {
  var row = document.createElement('div');
  row.className = 'member-row';
  row.dataset.idx = idx;

  var fields = document.createElement('div');
  fields.className = 'member-fields';

  var handleInput = document.createElement('input');
  handleInput.className = 'member-handle';
  handleInput.type = 'text';
  handleInput.value = m.handle || '';
  handleInput.placeholder = 'Phone or email';
  handleInput.addEventListener('input', updateMembersCount);

  var nameInput = document.createElement('input');
  nameInput.className = 'member-name';
  nameInput.type = 'text';
  nameInput.value = m.name || '';
  nameInput.placeholder = 'Name (optional)';

  var aliasesWrap = document.createElement('div');
  aliasesWrap.className = 'member-aliases';
  (m.aliases || []).forEach(function(a) { aliasesWrap.appendChild(buildAliasChip(a)); });

  var addAlias = document.createElement('span');
  addAlias.className = 'alias-add-btn';
  addAlias.textContent = '+ alias';
  addAlias.addEventListener('click', function() {
    var val = prompt('Add alias handle (phone or email):');
    if (val && val.trim()) {
      aliasesWrap.insertBefore(buildAliasChip(val.trim()), addAlias);
    }
  });
  aliasesWrap.appendChild(addAlias);

  fields.appendChild(handleInput);
  fields.appendChild(nameInput);
  fields.appendChild(aliasesWrap);

  var del = document.createElement('button');
  del.className = 'member-del';
  del.textContent = '×';
  del.title = 'Remove member';
  del.addEventListener('click', function() { row.remove(); updateMembersCount(); });

  row.appendChild(fields);
  row.appendChild(del);
  return row;
}

function buildAliasChip(alias) {
  var chip = document.createElement('span');
  chip.className = 'alias-chip';
  chip.dataset.alias = alias;
  var txt = document.createElement('span');
  txt.textContent = alias;
  var del = document.createElement('span');
  del.className = 'alias-chip-del';
  del.textContent = '×';
  del.addEventListener('click', function() { chip.remove(); });
  chip.appendChild(txt);
  chip.appendChild(del);
  return chip;
}

function addMemberRow() {
  var list = document.getElementById('members-list');
  var idx = list.children.length;
  list.appendChild(buildMemberRow({handle:'', name:'', aliases:[]}, idx));
  updateMembersCount();
}

function collectMembers() {
  var rows = document.querySelectorAll('#members-list .member-row');
  var result = [];
  rows.forEach(function(row) {
    var handle = row.querySelector('.member-handle').value.trim();
    if (!handle) return;
    var name = row.querySelector('.member-name').value.trim();
    var aliases = Array.from(row.querySelectorAll('.alias-chip')).map(function(c){ return c.dataset.alias; }).filter(Boolean);
    result.push({handle: handle, name: name, aliases: aliases.length ? aliases : undefined});
  });
  return result;
}

function updateMembersCount() {
  var rows = document.querySelectorAll('#members-list .member-row');
  var filled = Array.from(rows).filter(function(r){ return r.querySelector('.member-handle').value.trim(); });
  document.getElementById('members-count').textContent = filled.length;
}

function saveConvPersona() {
  if (!currentChatID) return;
  var contactName = document.getElementById('conv-contact-name').value.trim();
  var name = document.getElementById('conv-persona-name').value.trim();
  var personality = document.getElementById('conv-personality').value.trim();
  var prompt = document.getElementById('conv-persona-prompt').value.trim();
  var toolBoxes = document.querySelectorAll('#conv-tools-list input[type=checkbox]');
  // Only send enabled_tools if any checkboxes exist (tools registered).
  // Send null to inherit global, or an array (possibly empty) to override.
  var enabledTools = null;
  if (toolBoxes.length > 0) {
    enabledTools = Array.from(toolBoxes).filter(function(cb){ return cb.checked; }).map(function(cb){ return cb.value; });
  }
  var gatekeeper = document.getElementById('conv-gatekeeper').value.trim();
  var aliasHandles = collectAliasHandles();
  var members = collectMembers();
  var body = {display_name: contactName, persona_name: name, personality: personality, system_prompt: prompt, gatekeeper_prompt: gatekeeper, members: members, alias_handles: aliasHandles, proactive_enabled: document.getElementById('conv-proactive-enabled').checked};
  if (enabledTools !== null) body.enabled_tools = enabledTools;
  fetch('api/conversation/' + encodeURIComponent(currentChatID), {
    method: 'PATCH', headers: {'Content-Type':'application/json'},
    body: JSON.stringify(body)
  }).then(r => r.json()).then(function(conv) {
    for (var i = 0; i < conversations.length; i++) {
      if (conversations[i].chat_id === currentChatID) { conversations[i] = conv; break; }
    }
    document.getElementById('conv-title').textContent = conv.display_name || conv.handle || currentChatID;
    document.getElementById('conv-persona-label').textContent = conv.persona_name ? 'Persona: ' + conv.persona_name : '';
    renderConvList();
    loadProactiveNext();
    hideModals();
  }).catch(() => {});
}

function clearHistory() {
  if (!currentChatID) return;
  if (!confirm('Clear all message history for this conversation? Settings and memories are kept.')) return;
  fetch('api/conversation-clear/' + encodeURIComponent(currentChatID), {method: 'POST'})
    .then(function() {
      document.getElementById('messages').innerHTML = '<div id="no-conv" style="display:flex;align-items:center;justify-content:center;height:100%;color:var(--text-mute);font-size:0.9rem">History cleared.</div>';
    }).catch(() => {});
}

function deleteConv() {
  if (!currentChatID) return;
  if (!confirm('Delete this conversation and all its messages?')) return;
  fetch('api/conversation/' + encodeURIComponent(currentChatID), {method: 'DELETE'})
    .then(function() {
      conversations = conversations.filter(function(c) { return c.chat_id !== currentChatID; });
      currentChatID = null;
      renderConvList();
      document.getElementById('messages').innerHTML = '';
      document.getElementById('conv-title').textContent = '';
      document.getElementById('conv-persona-label').textContent = '';
      document.getElementById('content-header').style.display = 'none';
      document.getElementById('no-conv').style.display = '';
      hideModals();
    }).catch(() => {});
}

// --- Config ---

function showConfig() {
  Promise.all([
    fetch('api/config').then(r => r.json()),
    fetch('api/tools').then(r => r.json()).catch(() => [])
  ]).then(function(results) {
    var cfg = results[0];
    var tools = results[1] || [];
    var enabled = cfg.enabled_tools || [];
    document.getElementById('cfg-name').value = cfg.persona_name || '';
    document.getElementById('cfg-owner').value = cfg.owner_name || '';
    document.getElementById('cfg-owner-handle').value = cfg.owner_handle || '';
    document.getElementById('cfg-personality').value = cfg.personality || '';
    document.getElementById('cfg-prompt').value = cfg.system_prompt || '';
    document.getElementById('cfg-enabled').checked = !!cfg.enabled;
    document.getElementById('cfg-auto-all').checked = !!cfg.auto_reply_all;
    document.getElementById('cfg-gatekeeper').value = cfg.gatekeeper_prompt || '';
    document.getElementById('cfg-proactive-enabled').checked = !!cfg.proactive_enabled;
    document.getElementById('cfg-proactive-window').value = cfg.proactive_window || '';
    document.getElementById('cfg-proactive-prompt').value = cfg.proactive_prompt || '';
    document.getElementById('cfg-proactive-max-per-day').value = cfg.proactive_max_per_day || 0;
      var list = document.getElementById('cfg-tools-list');
    if (!tools.length) {
      list.innerHTML = '<span style="font-size:0.8rem;color:var(--text-mute)">No tools registered.</span>';
    } else {
      list.innerHTML = tools.map(function(t) {
        var chk = enabled.indexOf(t.name) >= 0 ? 'checked' : '';
        return '<label class="tool-check" title="' + escapeHtml(t.desc) + '">'
          + '<input type="checkbox" name="tool" value="' + escapeHtml(t.name) + '" ' + chk + '>'
          + escapeHtml(t.name) + '</label>';
      }).join('');
    }
    updateToolsCount();
    list.querySelectorAll('input[type=checkbox]:not([disabled])').forEach(function(cb) {
      cb.addEventListener('change', updateToolsCount);
    });
    document.getElementById('config-panel').style.display = 'block';
    document.getElementById('overlay').style.display = 'block';
  });
}

function updateToolsCount() {
  var checked = document.querySelectorAll('#cfg-tools-list input[type=checkbox]:checked');
  document.getElementById('cfg-tools-count').textContent = checked.length ? checked.length + ' enabled' : 'none';
}

function saveConfig() {
  var toolBoxes = document.querySelectorAll('#cfg-tools-list input[type=checkbox]:checked:not([data-builtin])');
  var tools = Array.from(toolBoxes).map(function(cb) { return cb.value; });
  var cfg = {
    persona_name: document.getElementById('cfg-name').value.trim(),
    owner_name: document.getElementById('cfg-owner').value.trim(),
    owner_handle: document.getElementById('cfg-owner-handle').value.trim(),
    personality: document.getElementById('cfg-personality').value.trim(),
    system_prompt: document.getElementById('cfg-prompt').value.trim(),
    enabled: document.getElementById('cfg-enabled').checked,
    auto_reply_all: document.getElementById('cfg-auto-all').checked,
    enabled_tools: tools,
    gatekeeper_prompt: document.getElementById('cfg-gatekeeper').value.trim(),
    proactive_enabled: document.getElementById('cfg-proactive-enabled').checked,
    proactive_window: document.getElementById('cfg-proactive-window').value.trim(),
    proactive_prompt: document.getElementById('cfg-proactive-prompt').value.trim(),
    proactive_max_per_day: parseInt(document.getElementById('cfg-proactive-max-per-day').value, 10) || 0
  };
  fetch('api/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(cfg)})
    .then(() => hideModals()).catch(() => {});
}

function testProactive() {
  var dt = document.getElementById('cfg-proactive-test-time').value;
  var body = {};
  if (dt) { body.fire_at = new Date(dt).toISOString(); }
  fetch('api/proactive/test', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)})
    .then(function(r) { return r.json(); })
    .then(function(d) { alert(d.message || 'Test scheduled'); })
    .catch(function() { alert('Failed to schedule test'); });
}

// --- API Keys ---

function showKeys() {
  document.getElementById('new-key-reveal').style.display = 'none';
  document.getElementById('new-key-name').value = '';
  loadKeysList();
  document.getElementById('keys-panel').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
}

function loadKeysList() {
  fetch('api/keys').then(r => r.json()).then(keys => {
    var el = document.getElementById('keys-list');
    if (!keys || !keys.length) {
      el.innerHTML = '<div style="font-size:0.82rem;color:var(--text-mute);padding:0.25rem 0">No keys yet.</div>';
      return;
    }
    el.innerHTML = keys.map(function(k) {
      var seen = k.last_seen ? 'Last seen ' + new Date(k.last_seen).toLocaleString() : 'Never used';
      return '<div class="key-row"><div><div class="key-name">' + escapeHtml(k.name) + '</div><div class="key-meta">' + escapeHtml(seen) + '</div></div>'
        + '<button class="secondary" style="font-size:0.75rem;padding:0.2rem 0.5rem" onclick="deleteKey(\'' + k.id + '\')">Revoke</button></div>';
    }).join('');
  }).catch(() => {});
}

function createKey() {
  var name = document.getElementById('new-key-name').value.trim();
  if (!name) { alert('Enter a label for this key.'); return; }
  fetch('api/keys', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({name: name})})
    .then(r => r.json()).then(ak => {
      document.getElementById('new-key-value').textContent = ak.key;
      document.getElementById('new-key-reveal').style.display = 'block';
      document.getElementById('new-key-name').value = '';
      loadKeysList();
    }).catch(() => {});
}

function deleteKey(id) {
  if (!confirm('Revoke this API key? The agent using it will stop working.')) return;
  fetch('api/keys/' + encodeURIComponent(id), {method:'DELETE'}).then(() => loadKeysList()).catch(() => {});
}

// --- Announce ---

function showAnnounce() {
  document.getElementById('announce-text').value = '';
  document.getElementById('announce-panel').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
  setTimeout(function(){ document.getElementById('announce-text').focus(); }, 50);
}

function sendAnnouncement() {
  var text = document.getElementById('announce-text').value.trim();
  if (!text) { alert('Enter a message to send.'); return; }
  fetch('api/announce', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({text: text})})
    .then(r => r.json()).then(function(data) {
      hideModals();
      alert('Queued for ' + data.queued + ' conversation(s). The agent will deliver them on next poll.');
    }).catch(() => {});
}

function hideModals() {
  document.getElementById('overlay').style.display = 'none';
  document.getElementById('config-panel').style.display = 'none';
  document.getElementById('keys-panel').style.display = 'none';
  document.getElementById('announce-panel').style.display = 'none';
  document.getElementById('conv-persona-panel').style.display = 'none';
}

function escapeHtml(s) {
  return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
`
