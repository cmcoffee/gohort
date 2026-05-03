package answer

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/private/research"
)

func (T *AnswerAgent) WebPath() string { return "/answer" }
func (T *AnswerAgent) WebName() string { return "Answer" }
func (T *AnswerAgent) WebDesc() string {
	return "Quick web-researched answers for technical questions and mini how-tos."
}

func (T *AnswerAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := NewWebUI(T, prefix, AppUIAssets{
		BodyHTML: answerHTML,
		AppCSS:   answerCSS,
		AppJS:    answerJS,
	})
	sub.HandleFunc("/api/ask", T.handleAsk)
	sub.HandleFunc("/api/history", T.handleHistory)
	sub.HandleFunc("/api/record/", T.handleRecord)
	sub.HandleFunc("/api/delete/", T.handleDelete)
	MountSubMux(mux, prefix, sub)
}

// AnswerEvent is streamed to the browser over SSE.
type AnswerEvent struct {
	Type    string         `json:"type"`
	Status  string         `json:"status,omitempty"`
	Answer  string         `json:"answer,omitempty"`
	Sources map[int]string `json:"sources,omitempty"`
	ID      string         `json:"id,omitempty"`
}

func (T *AnswerAgent) handleAsk(w http.ResponseWriter, r *http.Request) {
	question := strings.TrimSpace(r.URL.Query().Get("q"))
	if question == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	emit := func(status string) {
		sse.Send(AnswerEvent{Type: "status", Status: status})
	}

	result, runErr := research.RunQuickAnswer(r.Context(), &T.AppCore, question, emit)
	if runErr != nil {
		sse.Send(AnswerEvent{Type: "error", Status: runErr.Error()})
		return
	}

	id := UUIDv4()
	rec := AnswerRecord{
		ID:       id,
		Question: question,
		Answer:   result.Answer,
		Sources:  result.Sources,
		Date:     time.Now().Format(time.RFC3339),
	}
	if T.DB != nil {
		userDB(T.DB, r).Set(answerTable, id, rec)
	}

	sse.Send(AnswerEvent{
		Type:    "done",
		Answer:  result.Answer,
		Sources: result.Sources,
		ID:      id,
	})
}

func (T *AnswerAgent) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if T.DB == nil {
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}
	udb := userDB(T.DB, r)
	var records []AnswerRecord
	for _, key := range udb.Keys(answerTable) {
		var rec AnswerRecord
		if udb.Get(answerTable, key, &rec) {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Date > records[j].Date
	})
	type summary struct {
		ID       string `json:"ID"`
		Question string `json:"Question"`
		Date     string `json:"Date"`
	}
	out := make([]summary, len(records))
	for i, rec := range records {
		out[i] = summary{ID: rec.ID, Question: rec.Question, Date: rec.Date}
	}
	json.NewEncoder(w).Encode(out)
}

func (T *AnswerAgent) handleRecord(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/record/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if T.DB == nil {
		http.Error(w, "no db", http.StatusInternalServerError)
		return
	}
	var rec AnswerRecord
	if !userDB(T.DB, r).Get(answerTable, id, &rec) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (T *AnswerAgent) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/delete/")
	if id == "" || T.DB == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	userDB(T.DB, r).Unset(answerTable, id)
	w.WriteHeader(http.StatusNoContent)
}

func userDB(db Database, r *http.Request) Database {
	username := AuthCurrentUser(r)
	if username == "" {
		username = "default"
	}
	return db.Sub("answer_" + username)
}

// ---- static assets ----

const answerCSS = `
body { background:#0d1117; color:#c9d1d9; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif; }

#app { display:flex; height:100vh; overflow:hidden; }

/* sidebar */
#sidebar {
  width:260px; min-width:200px; max-width:340px;
  background:#161b22; border-right:1px solid #30363d;
  display:flex; flex-direction:column; overflow:hidden;
}
#sidebar-header {
  padding:0.75rem 1rem; border-bottom:1px solid #30363d;
  display:flex; align-items:center; justify-content:space-between;
}
#sidebar-header h2 { margin:0; font-size:0.9rem; color:#c9d1d9; font-weight:600; }
.sidebar-sort { display:flex; gap:2px; }
.sidebar-sort-btn {
  background:#0d1117; border:1px solid #30363d; color:#8b949e;
  font-size:0.7rem; padding:2px 7px; border-radius:4px; cursor:pointer;
}
.sidebar-sort-btn.active { background:#388bfd; border-color:#388bfd; color:#fff; }
#history-list { flex:1; overflow-y:auto; padding:0.5rem; }
.history-item {
  padding:0.5rem 0.6rem; border-radius:6px; cursor:pointer;
  border:1px solid transparent; margin-bottom:0.3rem;
  display:flex; align-items:flex-start; gap:0.4rem;
}
.history-item:hover { background:#1c2128; border-color:#30363d; }
.history-item.active { background:#1c2128; border-color:#388bfd; }
.history-item .hq { font-size:0.8rem; color:#c9d1d9; flex:1; line-height:1.35; }
.history-item .hd { font-size:0.7rem; color:#484f58; white-space:nowrap; }
.history-item .hdel {
  background:none; border:none; color:#484f58; cursor:pointer;
  font-size:0.75rem; padding:0 2px; line-height:1;
}
.history-item .hdel:hover { color:#f85149; }
#history-empty { color:#484f58; font-size:0.82rem; text-align:center; padding:1rem 0.5rem; }

/* main */
#main { flex:1; display:flex; flex-direction:column; overflow:hidden; }

/* question bar */
#question-bar {
  padding:1rem 1.25rem; border-bottom:1px solid #30363d;
  background:#161b22; display:flex; gap:0.5rem; align-items:flex-end;
}
#question-input {
  flex:1; background:#0d1117; border:1px solid #30363d; border-radius:8px;
  color:#c9d1d9; font-size:0.9rem; padding:0.55rem 0.75rem;
  resize:none; outline:none; font-family:inherit; min-height:40px; max-height:120px;
}
#question-input:focus { border-color:#388bfd; }
#question-input:disabled { opacity:0.45; cursor:not-allowed; }
#ask-btn {
  background:#388bfd; color:#fff; border:none; border-radius:8px;
  padding:0.55rem 1.1rem; font-size:0.88rem; font-weight:600;
  cursor:pointer; white-space:nowrap; height:40px;
}
#ask-btn:hover { background:#58a6ff; }
#ask-btn.cancel { background:#b91c1c; }
#ask-btn.cancel:hover { background:#ef4444; }

/* status */
#status-line {
  padding:0.3rem 1.25rem; font-size:0.78rem; color:#8b949e; min-height:1.5rem;
  display:flex; align-items:center; gap:0.4rem;
}
#status-line.active { border-bottom:1px solid #30363d; }
.ans-spinner {
  display:none; width:12px; height:12px; flex-shrink:0;
  border:2px solid #30363d; border-top-color:#388bfd;
  border-radius:50%; animation:ans-spin 0.7s linear infinite;
}
.ans-spinner.running { display:inline-block; }
@keyframes ans-spin { to { transform:rotate(360deg); } }

/* answer area */
#answer-area { flex:1; overflow-y:auto; padding:1.5rem 1.25rem; }
#answer-placeholder { color:#484f58; font-size:0.9rem; text-align:center; margin-top:3rem; }
#answer-content { max-width:820px; }
#answer-question { font-size:1.05rem; font-weight:600; color:#e6edf3; margin-bottom:1rem; line-height:1.4; }
.answer-body { line-height:1.7; font-size:0.9rem; }
.answer-body h1,.answer-body h2,.answer-body h3 { color:#e6edf3; margin:1rem 0 0.5rem; }
.answer-body p { margin:0 0 0.75rem; }
.answer-body ul,.answer-body ol { margin:0 0 0.75rem; padding-left:1.5rem; }
.answer-body li { margin-bottom:0.3rem; }
.answer-body code {
  background:#1c2128; border:1px solid #30363d; border-radius:4px;
  padding:1px 5px; font-size:0.85em; font-family:'Fira Code',monospace;
}
.answer-body pre {
  background:#1c2128; border:1px solid #30363d; border-radius:6px;
  padding:0.75rem 1rem; overflow-x:auto; margin:0 0 0.75rem;
}
.answer-body pre code { background:none; border:none; padding:0; }
.answer-body a { color:#58a6ff; }
.answer-body strong { color:#e6edf3; }
.answer-body table { border-collapse:collapse; margin:0.75rem 0; width:100%; font-size:0.85rem; }
.answer-body th, .answer-body td { border:1px solid #30363d; padding:0.35rem 0.75rem; text-align:left; }
.answer-body th { background:#1c2128; color:#e6edf3; font-weight:600; }
.answer-body tr:nth-child(even) td { background:#161b22; }
.answer-body blockquote { border-left:3px solid #30363d; margin:0 0 0.75rem; padding:0.25rem 0.75rem; color:#8b949e; }

/* sources */
#sources-section { margin-top:1.25rem; padding-top:1rem; border-top:1px solid #30363d; }
#sources-section h4 { font-size:0.82rem; color:#8b949e; margin:0 0 0.5rem; font-weight:600; text-transform:uppercase; letter-spacing:0.05em; }
#sources-list { list-style:none; padding:0; margin:0; display:flex; flex-direction:column; gap:0.3rem; }
#sources-list li { display:flex; align-items:baseline; gap:0.4rem; font-size:0.8rem; }
.src-num { color:#484f58; min-width:1.5rem; font-size:0.75rem; }
#sources-list a { color:#58a6ff; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; max-width:480px; }
#sources-list a:hover { text-decoration:underline; }
.src-ref { color:#58a6ff; text-decoration:none; font-size:0.8em; vertical-align:super; }
.src-ref:hover { text-decoration:underline; }

@media (max-width:640px) {
  #app { flex-direction:column; }
  #sidebar { width:100%; max-width:100%; height:auto; max-height:40vh; border-right:none; border-bottom:1px solid #30363d; }
  #main { min-height:60vh; }
}
`

const answerJS = `
var historyItems = [];
var historySort = 'date';
var currentID = '';
var currentSSE = null;

function init() {
  var inp = document.getElementById('question-input');
  inp.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); askQuestion(); }
  });
  inp.addEventListener('input', autoGrow);
  loadHistory();
}

function autoGrow() {
  var el = document.getElementById('question-input');
  el.style.height = 'auto';
  el.style.height = Math.min(el.scrollHeight, 120) + 'px';
}

function setRunning(on) {
  var btn = document.getElementById('ask-btn');
  var inp = document.getElementById('question-input');
  var sp = document.getElementById('answer-spinner');
  if (on) {
    btn.textContent = 'Cancel';
    btn.className = 'cancel';
    btn.onclick = cancelAnswer;
    inp.disabled = true;
    sp.className = 'ans-spinner running';
  } else {
    btn.textContent = 'Ask';
    btn.className = '';
    btn.onclick = askQuestion;
    inp.disabled = false;
    sp.className = 'ans-spinner';
  }
}

function cancelAnswer() {
  if (currentSSE) { currentSSE.close(); currentSSE = null; }
  setRunning(false);
  setStatus('Cancelled.', false);
}

function askQuestion() {
  var q = document.getElementById('question-input').value.trim();
  if (!q) return;
  if (currentSSE) { currentSSE.close(); currentSSE = null; }

  setRunning(true);
  document.getElementById('answer-placeholder').style.display = 'none';
  document.getElementById('answer-content').style.display = 'block';
  document.getElementById('answer-question').textContent = q;
  document.getElementById('answer-body').innerHTML = '';
  document.getElementById('sources-section').style.display = 'none';
  setStatus('Searching...', true);
  currentID = '';

  currentSSE = new EventSource('api/ask?q=' + encodeURIComponent(q));
  currentSSE.onmessage = function(e) {
    var ev = JSON.parse(e.data);
    if (ev.type === 'status') {
      setStatus(ev.status, true);
    } else if (ev.type === 'done') {
      currentSSE.close(); currentSSE = null;
      setRunning(false);
      setStatus('', false);
      currentID = ev.id || '';
      renderAnswer(ev.answer || '', ev.sources || {});
      loadHistory();
    } else if (ev.type === 'error') {
      currentSSE.close(); currentSSE = null;
      setRunning(false);
      setStatus('Error: ' + (ev.status || 'unknown error'), false);
    }
  };
  currentSSE.onerror = function() {
    if (currentSSE) { currentSSE.close(); currentSSE = null; }
    setRunning(false);
    setStatus('Connection lost.', false);
  };
}

function linkifyCitations(html, sources) {
  // Split on pre/code blocks so we don't linkify inside them.
  var parts = html.split(/(<(?:pre|code)[^>]*>[\s\S]*?<\/(?:pre|code)>)/i);
  for (var i = 0; i < parts.length; i += 2) {
    parts[i] = parts[i].replace(/\[(\d+(?:,\s*\d+)*)\]/g, function(match, inner) {
      var nums = inner.split(',').map(function(s) { return s.trim(); });
      if (nums.length === 1) {
        var url = sources[nums[0]];
        if (url) return '<a href="' + escapeHtml(url) + '" target="_blank" rel="noopener" class="src-ref">' + match + '</a>';
        return match;
      }
      var linked = nums.map(function(n) {
        var url = sources[n];
        if (url) return '<a href="' + escapeHtml(url) + '" target="_blank" rel="noopener" class="src-ref">' + n + '</a>';
        return n;
      });
      return '[' + linked.join(', ') + ']';
    });
  }
  return parts.join('');
}

function renderAnswer(answer, sources) {
  document.getElementById('answer-body').innerHTML = '<div class="answer-body">' + linkifyCitations(renderMarkdown(answer), sources) + '</div>';
  var sect = document.getElementById('sources-section');
  var list = document.getElementById('sources-list');
  list.innerHTML = '';
  var keys = Object.keys(sources).map(Number).sort(function(a, b) { return a - b; });
  if (keys.length === 0) { sect.style.display = 'none'; return; }
  for (var i = 0; i < keys.length; i++) {
    var n = keys[i];
    var url = sources[String(n)] || sources[n] || '';
    if (!url) continue;
    var domain = url.replace(/^https?:\/\//, '').split('/')[0];
    var li = document.createElement('li');
    li.innerHTML = '<span class="src-num">[' + n + ']</span>'
      + '<a href="' + escapeHtml(url) + '" target="_blank" rel="noopener">' + escapeHtml(domain) + '</a>';
    list.appendChild(li);
  }
  sect.style.display = 'block';
}

function setStatus(msg, active) {
  var el = document.getElementById('status-line');
  document.getElementById('status-text').textContent = msg;
  el.className = active ? 'active' : '';
}

function loadHistory() {
  fetch('api/history').then(function(r) { return r.json(); }).then(function(items) {
    historyItems = items || [];
    renderHistory();
  });
}

function setHistorySort(s) {
  historySort = s;
  document.getElementById('hsort-date').className = 'sidebar-sort-btn' + (s === 'date' ? ' active' : '');
  document.getElementById('hsort-name').className = 'sidebar-sort-btn' + (s === 'name' ? ' active' : '');
  renderHistory();
}

function renderHistory() {
  var list = document.getElementById('history-list');
  var empty = document.getElementById('history-empty');
  if (!historyItems || historyItems.length === 0) {
    list.innerHTML = '';
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';
  var sorted = historyItems.slice();
  if (historySort === 'name') {
    sorted.sort(function(a, b) { return (a.Question || '').localeCompare(b.Question || ''); });
  } else {
    sorted.sort(function(a, b) { return (b.Date || '') < (a.Date || '') ? -1 : 1; });
  }
  var html = '';
  for (var i = 0; i < sorted.length; i++) {
    var it = sorted[i];
    var active = it.ID === currentID ? ' active' : '';
    var dateStr = it.Date ? new Date(it.Date).toLocaleDateString() : '';
    html += '<div class="history-item' + active + '" data-id="' + escapeHtml(it.ID) + '" onclick="loadRecord(\'' + escapeHtml(it.ID) + '\')">'
      + '<div class="hq">' + escapeHtml(it.Question) + '</div>'
      + '<div style="display:flex;flex-direction:column;align-items:flex-end;gap:2px">'
      + '<span class="hd">' + escapeHtml(dateStr) + '</span>'
      + '<button class="hdel" onclick="event.stopPropagation();deleteRecord(\'' + escapeHtml(it.ID) + '\')" title="Delete">&times;</button>'
      + '</div></div>';
  }
  list.innerHTML = html;
}

function loadRecord(id) {
  fetch('api/record/' + id).then(function(r) { return r.json(); }).then(function(rec) {
    currentID = rec.ID;
    document.getElementById('question-input').value = rec.Question;
    autoGrow();
    document.getElementById('answer-placeholder').style.display = 'none';
    document.getElementById('answer-content').style.display = 'block';
    document.getElementById('answer-question').textContent = rec.Question;
    renderAnswer(rec.Answer || '', rec.Sources || {});
    renderHistory();
  });
}

function deleteRecord(id) {
  fetch('api/delete/' + id, {method: 'DELETE'}).then(function() {
    historyItems = historyItems.filter(function(it) { return it.ID !== id; });
    if (currentID === id) {
      currentID = '';
      document.getElementById('answer-content').style.display = 'none';
      document.getElementById('answer-placeholder').style.display = 'block';
    }
    renderHistory();
  });
}

window.addEventListener('DOMContentLoaded', init);
`

const answerHTML = `
<div id="app">
  <div id="sidebar">
    <div id="sidebar-header">
      <h2>History</h2>
      <div class="sidebar-sort">
        <button id="hsort-date" class="sidebar-sort-btn active" onclick="setHistorySort('date')">Newest</button>
        <button id="hsort-name" class="sidebar-sort-btn" onclick="setHistorySort('name')">A&#x2013;Z</button>
      </div>
    </div>
    <div id="history-list"></div>
    <div id="history-empty" style="display:none">No history yet.</div>
  </div>
  <div id="main">
    <div id="question-bar">
      <textarea id="question-input" placeholder="Ask a technical question&#x2026;" rows="1"></textarea>
      <button id="ask-btn" onclick="askQuestion()">Ask</button>
    </div>
    <div id="status-line">
      <span id="answer-spinner" class="ans-spinner"></span>
      <span id="status-text"></span>
    </div>
    <div id="answer-area">
      <div id="answer-placeholder">Ask a question to get a quick, web-researched answer.</div>
      <div id="answer-content" style="display:none">
        <div id="answer-question"></div>
        <div id="answer-body"></div>
        <div id="sources-section" style="display:none">
          <h4>Sources</h4>
          <ul id="sources-list"></ul>
        </div>
      </div>
    </div>
  </div>
</div>
`
