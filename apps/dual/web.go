package dual

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *DualAgent) WebPath() string { return "/orchestrate" }
func (T *DualAgent) WebName() string { return "Orchestrate" }
func (T *DualAgent) WebDesc() string {
	return "Thinking orchestrator assigns tasks to a fast worker AI in real time."
}

func (T *DualAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := NewWebUI(T, prefix, AppUIAssets{
		BodyHTML: dualBody,
		AppCSS:   dualCSS,
		AppJS:    dualJS,
		HeadHTML: `<script src="https://cdn.jsdelivr.net/npm/marked@11.2.0/marked.min.js"></script>`,
	})
	sub.HandleFunc("/api/send", T.handleSend)
	sub.HandleFunc("/api/procedures", T.handleProcedures)
	MountSubMux(mux, prefix, sub)
}

// handleProcedures returns the worker's saved procedures.
func (T *DualAgent) handleProcedures(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	procs := loadProcedures(udb)
	if procs == nil {
		procs = []Procedure{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(procs)
}

func (T *DualAgent) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}

	var req struct {
		Message string `json:"message"`
		History []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	// SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Serialized SSE writer — called from the single agent-loop goroutine
	// as well as inline tool handlers (also synchronous), so no actual
	// concurrent access but the mutex keeps things safe if that changes.
	var mu sync.Mutex
	send := func(evType string, data any) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evType, body)
		flusher.Flush()
	}

	// Build conversation history.
	messages := make([]Message, 0, len(req.History)+1)
	for _, h := range req.History {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		messages = append(messages, Message{Role: h.Role, Content: h.Content})
	}
	messages = append(messages, Message{Role: "user", Content: req.Message})

	// Build worker system prompt (includes saved procedures).
	workerSysPrompt := workerSystemPromptFor(udb)

	// --- delegate tool: sends a task to the right (worker) LLM ---
	delegateTool := AgentToolDef{
		Tool: Tool{
			Name:        "delegate",
			Description: "Assign a specific task to the worker AI and receive its response. The worker is fast and execution-focused. Give it clear, self-contained instructions.",
			Parameters: map[string]ToolParam{
				"task": {
					Type:        "string",
					Description: "The specific task for the worker to complete. Be precise and include all necessary context.",
				},
				"context": {
					Type:        "string",
					Description: "Optional background context, prior results, or constraints the worker should know.",
				},
			},
			Required: []string{"task"},
		},
		Handler: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			bg, _ := args["context"].(string)

			var workerInput strings.Builder
			workerInput.WriteString(task)
			if bg != "" {
				workerInput.WriteString("\n\nContext: ")
				workerInput.WriteString(bg)
			}

			send("task", map[string]any{"text": task})

			var rightBuf strings.Builder
			resp, err := T.AppCore.LLM.ChatStream(
				ctx,
				[]Message{{Role: "user", Content: workerInput.String()}},
				func(chunk string) {
					rightBuf.WriteString(chunk)
					send("worker_chunk", map[string]any{"text": chunk})
				},
				WithSystemPrompt(workerSysPrompt),
				WithThink(false),
				WithMaxTokens(2048),
			)

			result := strings.TrimSpace(rightBuf.String())
			if result == "" && resp != nil {
				result = resp.Content
			}

			// Strip procedure tags from the result returned to orchestrator;
			// execute any saves/deletes against the user's DB.
			result = parseProcedureActions(udb, result)

			send("worker_done", map[string]any{"text": result})

			if err != nil && ctx.Err() == nil {
				return result, err
			}
			return result, nil
		},
	}

	orchestratorSysPrompt := T.SystemPrompt()
	resp, _, loopErr := T.AppCore.RunAgentLoop(ctx, messages, AgentLoopConfig{
		SystemPrompt: orchestratorSysPrompt,
		Tools:        []AgentToolDef{delegateTool},
		MaxRounds:    16,
		ChatOptions:  []ChatOption{WithThink(false), WithMaxTokens(8192)},
		Stream: func(chunk string) {
			send("left_chunk", map[string]any{"text": chunk})
		},
		OnStep: func(step StepInfo) {
			if step.Done {
				return
			}
			for _, tc := range step.ToolCalls {
				Debug("[orchestrate] called tool: %s", tc.Name)
			}
		},
	})

	if loopErr != nil && ctx.Err() == nil {
		send("error", map[string]any{"text": loopErr.Error()})
		return
	}

	finalReply := ""
	if resp != nil {
		finalReply = strings.TrimSpace(resp.Content)
		if finalReply == "" {
			finalReply = strings.TrimSpace(resp.Reasoning)
		}
	}

	_ = time.Now() // keep time import used
	send("done", map[string]any{"reply": finalReply})
}

// --- HTML / CSS / JS ---

const dualCSS = `
body { height: 100vh; display: flex; flex-direction: column; overflow: hidden; }

/* --- toolbar --- */
#toolbar {
  display: flex; align-items: center; gap: 0.5rem; padding: 0.5rem 1rem;
  background: var(--bg-1); border-bottom: 1px solid var(--border); flex-shrink: 0; flex-wrap: wrap;
}
#toolbar .app-title { font-weight: 700; color: var(--text-hi); }
.toolbar-sep { flex: 1; }
#toolbar button {
  background: transparent; border: 1px solid var(--border); color: var(--text-mute);
  border-radius: 6px; padding: 0.35rem 0.75rem; cursor: pointer; font-size: 0.8rem;
}
#toolbar button:hover { border-color: var(--accent); color: var(--text-hi); }
#btn-procs { position: relative; }
#proc-badge {
  position: absolute; top: -5px; right: -5px; background: var(--accent); color: #fff;
  border-radius: 50%; width: 16px; height: 16px; font-size: 0.65rem;
  display: none; align-items: center; justify-content: center;
}

/* --- split panes --- */
#panes { display: flex; flex: 1; overflow: hidden; }

.pane {
  display: flex; flex-direction: column; flex: 1; min-width: 0; overflow: hidden;
}
.pane:first-child { border-right: 1px solid var(--border); }

.pane-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0.4rem 1rem; border-bottom: 1px solid var(--border);
  font-size: 0.82rem; font-weight: 600; color: var(--text-mute); flex-shrink: 0;
}
.pane-header .pane-label { display: flex; align-items: center; gap: 0.5rem; }
.pane-header .pane-badge {
  font-size: 0.7rem; padding: 0.1rem 0.4rem; border-radius: 10px;
  background: var(--bg-2); color: var(--text-mute);
}
.pane-header .pane-badge.thinking { background: rgba(88,166,255,0.15); color: var(--accent); }
.pane-header .pane-badge.working  { background: rgba(63,185,80,0.15);  color: #3fb950; }
.pane-header button {
  background: none; border: none; color: var(--text-mute); cursor: pointer;
  font-size: 0.75rem; padding: 0.1rem 0.3rem;
}
.pane-header button:hover { color: var(--text-hi); }

.pane-msgs {
  flex: 1; overflow-y: auto; padding: 0.75rem 1rem;
}

/* --- message bubbles --- */
.msg { margin-bottom: 0.75rem; font-size: 0.85rem; line-height: 1.55; }
.msg.user-msg {
  background: var(--accent); color: #fff; border-radius: 8px;
  padding: 0.6rem 0.8rem; margin-left: 3rem;
}
.msg.left-msg {
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.6rem 0.8rem; margin-right: 3rem;
}
.msg.right-task {
  border-left: 3px solid #3fb950; padding: 0.35rem 0.7rem;
  color: var(--text-mute); font-size: 0.8rem; font-style: italic; margin: 0.3rem 0;
}
.msg.right-msg {
  background: var(--bg-0); border: 1px solid var(--border); border-left: 3px solid #3fb950;
  border-radius: 8px; padding: 0.6rem 0.8rem;
}
.msg.error-msg { color: var(--danger); padding: 0.3rem 0; font-size: 0.82rem; }
.msg pre {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 4px;
  padding: 0.5rem; margin: 0.4rem 0; overflow-x: auto; font-size: 0.8rem;
  font-family: ui-monospace, Menlo, Consolas, monospace; white-space: pre;
}
.msg code { background: var(--bg-1); padding: 0.1rem 0.25rem; border-radius: 3px; font-size: 0.85em; }
.msg pre code { background: none; padding: 0; }

/* streaming cursor blink */
.streaming-cursor::after {
  content: "▋"; color: var(--accent);
  animation: blink 1s step-end infinite;
}
@keyframes blink { 50% { opacity: 0; } }

/* think blocks from <think>...</think> tags */
.think-block {
  background: rgba(88,166,255,0.07); border-left: 2px solid var(--accent);
  padding: 0.3rem 0.6rem; margin: 0.3rem 0; border-radius: 0 4px 4px 0;
  color: var(--text-mute); font-size: 0.82rem; font-style: italic;
}
.think-label { font-weight: 600; font-style: normal; color: var(--accent); font-size: 0.75rem; margin-bottom: 0.2rem; }

/* --- pane divider --- */
#pane-divider {
  width: 5px; background: var(--border); cursor: col-resize; flex-shrink: 0;
}
#pane-divider:hover, #pane-divider.dragging { background: var(--accent); }

/* --- input area --- */
#input-area {
  display: flex; gap: 0.5rem; padding: 0.5rem 1rem 1rem;
  border-top: 1px solid var(--border); background: var(--bg-1); flex-shrink: 0;
  align-items: flex-end;
}
#msg-input {
  flex: 1; background: var(--bg-0); border: 1px solid var(--border); color: var(--text);
  padding: 0.4rem 0.6rem; border-radius: 6px; font-family: inherit;
  font-size: 0.85rem; resize: vertical; min-height: 56px; max-height: 200px;
}
#msg-input:focus { border-color: var(--accent); outline: none; }
#send-btn {
  background: var(--accent); color: #fff; border: none; border-radius: 6px;
  padding: 0.45rem 1rem; cursor: pointer; font-size: 0.85rem; white-space: nowrap;
}
#send-btn:disabled { opacity: 0.35; cursor: default; }

/* --- procedures modal --- */
#overlay { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 99; }
#proc-modal {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 90%; max-width: 600px; max-height: 80vh; overflow-y: auto; z-index: 100;
}
#proc-modal h3 { margin: 0 0 0.75rem; font-size: 1rem; color: var(--text-hi); }
.proc-entry {
  border: 1px solid var(--border); border-radius: 6px; padding: 0.6rem 0.75rem;
  margin-bottom: 0.5rem; background: var(--bg-0);
}
.proc-name { font-weight: 700; color: var(--accent); font-size: 0.85rem; }
.proc-desc { color: var(--text-mute); font-size: 0.8rem; margin: 0.1rem 0 0.35rem; }
.proc-steps { font-size: 0.78rem; font-family: ui-monospace, Menlo, Consolas, monospace;
  white-space: pre-wrap; color: var(--text); background: var(--bg-1);
  border-radius: 4px; padding: 0.4rem; }
#proc-empty { color: var(--text-mute); font-size: 0.85rem; font-style: italic; }
.modal-btns { display: flex; justify-content: flex-end; margin-top: 0.75rem; }
.modal-btns button { border: 1px solid var(--border); background: transparent; color: var(--text-mute); border-radius: 4px; padding: 0.3rem 0.9rem; cursor: pointer; font-size: 0.85rem; }
.modal-btns button:hover { border-color: var(--accent); color: var(--text-hi); }

.spinner { display: inline-block; width: 11px; height: 11px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; vertical-align: middle; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 720px) {
  #panes { flex-direction: column; }
  .pane:first-child { border-right: none; border-bottom: 1px solid var(--border); }
  #pane-divider { display: none; }
}
`

const dualBody = `
<div id="toolbar">
  <span class="app-title">Orchestrate</span>
  <span class="toolbar-sep"></span>
  <button id="btn-clear" onclick="clearAll()">Clear</button>
  <button id="btn-procs" onclick="showProcedures()">
    Procedures
    <span id="proc-badge"></span>
  </button>
</div>

<div id="panes">
  <div class="pane" id="left-pane">
    <div class="pane-header">
      <div class="pane-label">
        🧠 Orchestrator
        <span class="pane-badge" id="left-badge">thinking</span>
      </div>
      <button onclick="clearPane('left')">Clear</button>
    </div>
    <div class="pane-msgs" id="left-msgs"></div>
  </div>

  <div id="pane-divider" onmousedown="startResize(event)"></div>

  <div class="pane" id="right-pane">
    <div class="pane-header">
      <div class="pane-label">
        ⚡ Worker
        <span class="pane-badge" id="right-badge">direct</span>
      </div>
      <button onclick="clearPane('right')">Clear</button>
    </div>
    <div class="pane-msgs" id="right-msgs"></div>
  </div>
</div>

<div id="input-area">
  <textarea id="msg-input" placeholder="Ask the orchestrator…  (Enter to send, Shift+Enter for newline)"
    onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();sendMessage();}"></textarea>
  <button id="send-btn" onclick="sendMessage()">Send</button>
</div>

<div id="overlay" onclick="hideModal()"></div>
<div id="proc-modal">
  <h3>Worker Procedures</h3>
  <div id="proc-list"></div>
  <div class="modal-btns"><button onclick="hideModal()">Close</button></div>
</div>
`

const dualJS = `
var chatHistory = [];
var streaming = false;

// marked config
if (typeof marked !== 'undefined') {
  marked.setOptions({breaks: true, gfm: true});
}

// --- Send ---

function sendMessage() {
  if (streaming) return;
  var input = document.getElementById('msg-input');
  var msg = input.value.trim();
  if (!msg) return;
  input.value = '';

  chatHistory.push({role: 'user', content: msg});
  appendMsg('left', 'user-msg', escapeHtml(msg));
  appendMsg('right', 'user-msg', escapeHtml(msg));

  startStream(msg);
}

function startStream(msg) {
  streaming = true;
  document.getElementById('send-btn').disabled = true;
  document.getElementById('msg-input').disabled = true;
  setBadge('left', 'thinking', true);
  setBadge('right', 'direct', false);

  // Left: streaming accumulator div
  var leftDiv = createStreamDiv('left', 'left-msg');
  var rightDiv = null;

  var leftRaw = '';
  var rightRaw = '';

  var es = new EventSource('/api/send?' + (new URLSearchParams({_fake:'1'})));
  // Use fetch + manual SSE parse so we can POST the body.
  es.close();

  fetchSSE('api/send', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      message: msg,
      history: chatHistory.slice(0, -1)
    })
  }, function(evType, data) {
    switch (evType) {
      case 'left_chunk':
        leftRaw += data.text;
        updateStreamDiv(leftDiv, leftRaw);
        break;

      case 'task':
        rightDiv = null; // new task = new right div
        appendMsg('right', 'right-task', '↳ ' + escapeHtml(data.text));
        rightDiv = createStreamDiv('right', 'right-msg');
        rightRaw = '';
        setBadge('right', 'working…', true);
        break;

      case 'worker_chunk':
        rightRaw += data.text;
        if (rightDiv) updateStreamDiv(rightDiv, rightRaw);
        break;

      case 'worker_done':
        if (rightDiv) {
          rightDiv.classList.remove('streaming-cursor');
          rightDiv.innerHTML = formatMarkdown(rightRaw);
        }
        setBadge('right', 'direct', false);
        break;

      case 'done':
        leftDiv.classList.remove('streaming-cursor');
        leftDiv.innerHTML = formatMarkdown(leftRaw);
        if (data.reply) {
          chatHistory.push({role: 'assistant', content: data.reply});
        }
        endStream();
        refreshProcBadge();
        break;

      case 'error':
        appendMsg('left', 'error-msg', 'Error: ' + escapeHtml(data.text));
        endStream();
        break;
    }
  });
}

function endStream() {
  streaming = false;
  document.getElementById('send-btn').disabled = false;
  document.getElementById('msg-input').disabled = false;
  document.getElementById('msg-input').focus();
  setBadge('left', 'thinking', false);
  setBadge('right', 'direct', false);
}

// --- Simple SSE-over-POST fetch ---
// The browser EventSource doesn't support POST bodies, so we use fetch
// with a manual line-by-line SSE parser.

function fetchSSE(url, opts, onEvent) {
  fetch(url, opts).then(function(resp) {
    if (!resp.ok) {
      resp.text().then(function(t) { onEvent('error', {text: t}); });
      return;
    }
    var reader = resp.body.getReader();
    var decoder = new TextDecoder();
    var buf = '';
    var evType = 'message';

    function pump() {
      reader.read().then(function(res) {
        if (res.done) { onEvent('done', {reply: ''}); return; }
        buf += decoder.decode(res.value, {stream: true});
        var lines = buf.split('\n');
        buf = lines.pop();
        for (var i = 0; i < lines.length; i++) {
          var line = lines[i];
          if (line.startsWith('event: ')) {
            evType = line.slice(7).trim();
          } else if (line.startsWith('data: ')) {
            try {
              var payload = JSON.parse(line.slice(6));
              onEvent(evType, payload);
            } catch(e) {}
            evType = 'message';
          }
        }
        pump();
      }).catch(function(e) { onEvent('error', {text: e.message}); });
    }
    pump();
  }).catch(function(e) { onEvent('error', {text: e.message}); });
}

// --- DOM helpers ---

function appendMsg(side, cls, html) {
  var div = document.createElement('div');
  div.className = 'msg ' + cls;
  div.innerHTML = html;
  var msgs = document.getElementById(side + '-msgs');
  msgs.appendChild(div);
  msgs.scrollTop = msgs.scrollHeight;
  return div;
}

function createStreamDiv(side, cls) {
  var div = appendMsg(side, cls + ' streaming-cursor', '');
  return div;
}

function updateStreamDiv(div, raw) {
  // During streaming: render think-tagged text live but keep it fast.
  div.innerHTML = renderStreaming(raw);
  var msgs = div.parentElement;
  msgs.scrollTop = msgs.scrollHeight;
}

// renderStreaming: parse <think>...</think> blocks and render as styled divs,
// escape everything else. Used during live streaming for fast rendering.
function renderStreaming(raw) {
  var result = '';
  var inThink = false;
  var parts = raw.split(/(<\/?think>)/);
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i];
    if (p === '<think>') {
      inThink = true;
      result += '<div class="think-block"><div class="think-label">thinking…</div>';
    } else if (p === '</think>') {
      inThink = false;
      result += '</div>';
    } else {
      result += escapeHtml(p).replace(/\n/g, '<br>');
    }
  }
  if (inThink) result += '</div>';
  return result;
}

// formatMarkdown: final render using marked.js, with think blocks extracted first.
function formatMarkdown(raw) {
  // Extract and style <think> blocks, then run the rest through marked.
  var thinkHtml = '';
  var mainText = raw;

  var thinkMatch = raw.match(/^<think>([\s\S]*?)<\/think>\n*/);
  if (thinkMatch) {
    thinkHtml = '<div class="think-block"><div class="think-label">thinking</div>'
      + escapeHtml(thinkMatch[1]).replace(/\n/g, '<br>') + '</div>';
    mainText = raw.slice(thinkMatch[0].length);
  }

  var body = (typeof marked !== 'undefined')
    ? marked.parse(mainText)
    : escapeHtml(mainText).replace(/\n/g, '<br>');

  return thinkHtml + body;
}

function setBadge(side, text, active) {
  var b = document.getElementById(side + '-badge');
  b.textContent = text;
  b.className = 'pane-badge' + (active ? (side === 'left' ? ' thinking' : ' working') : '');
}

function clearAll() {
  chatHistory = [];
  document.getElementById('left-msgs').innerHTML = '';
  document.getElementById('right-msgs').innerHTML = '';
}

function clearPane(side) {
  document.getElementById(side + '-msgs').innerHTML = '';
}

// --- Procedures ---

function refreshProcBadge() {
  fetch('api/procedures').then(function(r) { return r.json(); }).then(function(procs) {
    var badge = document.getElementById('proc-badge');
    if (procs && procs.length > 0) {
      badge.textContent = procs.length;
      badge.style.display = 'flex';
    } else {
      badge.style.display = 'none';
    }
  }).catch(function(){});
}

function showProcedures() {
  fetch('api/procedures').then(function(r) { return r.json(); }).then(function(procs) {
    var list = document.getElementById('proc-list');
    if (!procs || procs.length === 0) {
      list.innerHTML = '<div id="proc-empty">No procedures saved yet. The worker will save useful procedures as it learns.</div>';
    } else {
      var html = '';
      procs.forEach(function(p) {
        html += '<div class="proc-entry">'
          + '<div class="proc-name">' + escapeHtml(p.name) + '</div>'
          + '<div class="proc-desc">' + escapeHtml(p.description) + '</div>'
          + '<div class="proc-steps">' + escapeHtml(p.steps) + '</div>'
          + '</div>';
      });
      list.innerHTML = html;
    }
    document.getElementById('proc-modal').style.display = 'block';
    document.getElementById('overlay').style.display = 'block';
  }).catch(function(e) { alert('Failed to load procedures: ' + e.message); });
}

function hideModal() {
  document.getElementById('proc-modal').style.display = 'none';
  document.getElementById('overlay').style.display = 'none';
}

// --- Pane resize ---

function startResize(e) {
  var divider = document.getElementById('pane-divider');
  var left = document.getElementById('left-pane');
  var panes = document.getElementById('panes');
  var startX = e.clientX;
  var startW = left.offsetWidth;
  divider.classList.add('dragging');

  function onMove(e) {
    var total = panes.offsetWidth - 5;
    var newW = Math.max(200, Math.min(startW + (e.clientX - startX), total - 200));
    left.style.flex = 'none';
    left.style.width = newW + 'px';
  }
  function onUp() {
    divider.classList.remove('dragging');
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
  }
  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}

// --- Utilities ---

function escapeHtml(s) {
  return String(s == null ? '' : s)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

// Init
refreshProcBadge();
document.getElementById('msg-input').focus();
`
