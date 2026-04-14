package codewriter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webui"
)

const (
	snippetTable  = "codewriter_snippets"
	valueTable = "codewriter_values"
)

// SnippetRecord stores a saved code snippet with optional variables.
type SnippetRecord struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Lang string            `json:"lang"`
	Code string            `json:"code"`
	Vars map[string]string `json:"vars"` // variable_name -> substitution text
	Date string            `json:"date"`
}

// SavedValue is a named snippet of text the user can paste into scripts.
// Could be a hostname, a shell command like $(python3 get_pass.py), an
// env var like $DB_HOST, a connection string, etc.
type SavedValue struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Desc  string `json:"desc"`  // optional description
	Value string `json:"value"` // the text to paste
	Date  string `json:"date"`
}

func (T *CodeWriterAgent) WebPath() string { return "/codewriter" }
func (T *CodeWriterAgent) WebName() string { return "CodeWriter" }
func (T *CodeWriterAgent) WebDesc() string {
	return "Generate shell scripts, SQL queries, and code snippets."
}

func (T *CodeWriterAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := http.NewServeMux()
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "CodeWriter",
			AppName:  "CodeWriter",
			Prefix:   prefix,
			BodyHTML: cwBody,
			AppCSS:   cwCSS,
			AppJS:    cwJS,
		}))
	})
	sub.HandleFunc("/api/chat", T.handleChat)
	sub.HandleFunc("/api/snippets", T.handleSnippets)
	sub.HandleFunc("/api/snippet/", T.handleSnippet)
	sub.HandleFunc("/api/values", T.handleValues)
	sub.HandleFunc("/api/value/", T.handleValue)
	MountSubMux(mux, prefix, sub)
}

// handleChat receives the current editor code + a chat message, sends to the
// worker LLM, and returns the response. If the response contains code, we
// flag it so the frontend can show an "Apply" button.
func (T *CodeWriterAgent) handleChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Lang    string `json:"lang"`
		Code    string `json:"code"`
		Context string `json:"context"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	var code_context string
	if req.Context != "" {
		code_context = fmt.Sprintf("\n\nReference context (table schemas, docs, notes):\n```\n%s\n```", req.Context)
	}
	if req.Code != "" {
		lang := req.Lang
		if lang == "" {
			lang = "code"
		}
		code_context += fmt.Sprintf("\n\nCurrent script (%s):\n```%s\n%s\n```", req.Name, lang, req.Code)
	} else if req.Name != "" {
		code_context += fmt.Sprintf("\n\nScript name: %s\n(Editor is empty -- this is a new script.)", req.Name)
	}

	prompt := req.Message + code_context

	// Build system prompt with selected language hint and available value sets.
	system_prompt := T.SystemPrompt()
	if req.Lang != "" {
		system_prompt += fmt.Sprintf("\n\nThe user is currently working in %s. Default to %s for code output unless they specify otherwise.", req.Lang, req.Lang)
	}
	system_prompt += T.buildValuePrompt()

	agent := &FuzzAgent{LLM: T.FuzzAgent.LLM}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := agent.PingLLM(pingCtx); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	resp, err := agent.WorkerChat(context.Background(), []Message{
		{Role: "user", Content: prompt},
	}, WithSystemPrompt(system_prompt), WithMaxTokens(4096), WithThink(false))

	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text := strings.TrimSpace(ResponseText(resp))

	// Detect if the response contains a fenced code block.
	code_content := ""
	if strings.HasPrefix(text, "CODE:") {
		lines := strings.SplitN(text, "\n", 2)
		if len(lines) > 1 {
			text = strings.TrimSpace(lines[1])
		} else {
			text = strings.TrimPrefix(text, "CODE:")
		}
	}

	// Extract the first fenced code block as the applyable code.
	if idx := strings.Index(text, "```"); idx >= 0 {
		after := text[idx+3:]
		// Skip the language tag on the opening fence.
		if nl := strings.Index(after, "\n"); nl >= 0 {
			after = after[nl+1:]
		}
		if end := strings.Index(after, "```"); end >= 0 {
			code_content = after[:end]
			if len(code_content) > 0 && code_content[len(code_content)-1] == '\n' {
				code_content = code_content[:len(code_content)-1]
			}
		}
	}

	result := map[string]any{"content": text}
	if code_content != "" {
		result["code"] = code_content
		result["type"] = "code"
	} else {
		result["type"] = "chat"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// buildValuePrompt appends a note about placeholders to the system prompt.
func (T *CodeWriterAgent) buildValuePrompt() string {
	return "\n\nWhen generating code, use {{PLACEHOLDER}} syntax for values the user would likely need to fill in, such as credentials, hostnames, paths, and environment-specific settings. Examples: {{PASSWORD}}, {{DB_HOST}}, {{OUTPUT_PATH}}. The user has a UI that detects these placeholders and lets them fill in values."
}

// handleSnippets handles list (GET) and create/update (POST).
func (T *CodeWriterAgent) handleSnippets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		T.handleListSnippets(w, r)
	case http.MethodPost:
		T.handleSaveSnippet(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CodeWriterAgent) handleListSnippets(w http.ResponseWriter, r *http.Request) {
	if T.DB == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	var items []SnippetRecord
	for _, key := range T.DB.Keys(snippetTable) {
		var rec SnippetRecord
		if T.DB.Get(snippetTable, key, &rec) {
			items = append(items, rec)
		}
	}
	if items == nil {
		items = []SnippetRecord{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (T *CodeWriterAgent) handleSaveSnippet(w http.ResponseWriter, r *http.Request) {
	if T.DB == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	var req SnippetRecord
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Code == "" {
		http.Error(w, "name and code required", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	req.Date = time.Now().Format(time.RFC3339)
	T.DB.Set(snippetTable, req.ID, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(req)
}

// handleSnippet handles GET (load) and DELETE for /api/snippet/{id}.
func (T *CodeWriterAgent) handleSnippet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/snippet/")
	if id == "" || T.DB == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rec SnippetRecord
		if !T.DB.Get(snippetTable, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		T.DB.Unset(snippetTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- HTML / CSS / JS ---

// --- Saved Values ---

func (T *CodeWriterAgent) handleValues(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if T.DB == nil {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
			return
		}
		var items []SavedValue
		for _, key := range T.DB.Keys(valueTable) {
			var rec SavedValue
			if T.DB.Get(valueTable, key, &rec) {
				items = append(items, rec)
			}
		}
		if items == nil {
			items = []SavedValue{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		if T.DB == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req SavedValue
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			req.ID = UUIDv4()
		}
		req.Date = time.Now().Format(time.RFC3339)
		T.DB.Set(valueTable, req.ID, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(req)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CodeWriterAgent) handleValue(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/value/")
	if id == "" || T.DB == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rec SavedValue
		if !T.DB.Get(valueTable, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		T.DB.Unset(valueTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}


const cwCSS = `
body { height: 100vh; display: flex; flex-direction: column; }
#toolbar {
  display: flex; align-items: center; gap: 0.5rem; padding: 0.5rem 1rem;
  background: var(--bg-1); border-bottom: 1px solid var(--border);
}
#toolbar input, #toolbar select {
  background: var(--bg-0); border: 1px solid var(--border); color: var(--text);
  padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.85rem;
}
#toolbar input { flex: 1; }
#toolbar select { max-width: 120px; }
#toolbar button {
  background: var(--accent); color: #fff; border: none; border-radius: 6px;
  padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem; white-space: nowrap;
}
#toolbar button.secondary {
  background: transparent; border: 1px solid var(--border); color: var(--text-mute);
}
#toolbar button:hover { opacity: 0.9; }

#main { display: flex; flex: 1; overflow: hidden; }

#editor-pane {
  flex: 1; display: flex; flex-direction: column; border-right: 1px solid var(--border);
}
#editor {
  flex: 1; background: var(--bg-0); color: var(--text); border: none; resize: none;
  padding: 1rem; font-family: ui-monospace, Menlo, Consolas, 'Fira Code', monospace;
  font-size: 0.85rem; line-height: 1.6; outline: none; tab-size: 4;
}
#context-toggle {
  display: flex; align-items: center; gap: 0.4rem; padding: 0.3rem 1rem;
  background: var(--bg-1); border-top: 1px solid var(--border); cursor: pointer;
  font-size: 0.8rem; color: var(--text-mute); user-select: none;
}
#context-toggle:hover { color: var(--text-hi); }
#context-toggle .arrow { font-size: 0.6rem; transition: transform 0.15s; }
#context-toggle .arrow.open { transform: rotate(90deg); }
#context-pane {
  display: none; border-top: 1px solid var(--border);
}
#context-pane.open { display: flex; flex-direction: column; min-height: 120px; max-height: 40%; }
#context-pane textarea {
  flex: 1; background: var(--bg-0); color: var(--text); border: none; resize: none;
  padding: 0.75rem 1rem; font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 0.8rem; line-height: 1.5; outline: none;
}

#chat-pane {
  width: 520px; display: flex; flex-direction: column; background: var(--bg-1);
}
#chat-header {
  padding: 0.5rem 1rem; border-bottom: 1px solid var(--border); font-size: 0.85rem;
  color: var(--text-mute); font-weight: 600; display: flex; justify-content: space-between; align-items: center;
}
#chat-header button {
  background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 0.75rem; padding: 0 0.3rem;
}
#chat-header button:hover { color: var(--text-hi); }
#chat-messages { flex: 1; overflow-y: auto; padding: 0.75rem; }

.chat-msg { margin-bottom: 0.75rem; padding: 0.6rem 0.8rem; border-radius: 8px; font-size: 0.85rem; line-height: 1.5; }
.chat-msg.user { background: var(--accent); color: #fff; margin-left: 2rem; }
.chat-msg.assistant { background: var(--bg-0); color: var(--text); border: 1px solid var(--border); }
.chat-msg pre {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 4px;
  padding: 0.5rem; margin: 0.4rem 0; overflow-x: auto; font-size: 0.8rem;
  font-family: ui-monospace, Menlo, Consolas, monospace; white-space: pre; word-break: normal;
}
.chat-msg code { background: var(--bg-1); padding: 0.1rem 0.25rem; border-radius: 3px; font-size: 0.85em; }
.chat-msg pre code { background: none; padding: 0; }
.apply-btn {
  display: inline-block; margin-top: 0.4rem; background: var(--accent); color: #fff;
  border: none; border-radius: 4px; padding: 0.2rem 0.5rem; cursor: pointer; font-size: 0.75rem;
}
.apply-btn:hover { opacity: 0.9; }

#chat-input-area {
  display: flex; gap: 0.5rem; padding: 0.5rem 0.75rem; border-top: 1px solid var(--border);
}
#chat-input {
  flex: 1; background: var(--bg-0); border: 1px solid var(--border); color: var(--text);
  padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.85rem;
}
#chat-input:focus { border-color: var(--accent); outline: none; }
#chat-send {
  background: var(--accent); color: #fff; border: none; border-radius: 6px;
  padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem;
}

/* Snippets panel (modal overlay) */
#overlay {
  display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%;
  background: rgba(0,0,0,0.5); z-index: 99;
}
#snippets-panel {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1rem; width: 500px; max-height: 70vh; overflow-y: auto; z-index: 100;
}
#snippets-panel h3 { color: var(--text-hi); margin-bottom: 0.75rem; }
.snippet-item {
  display: flex; justify-content: space-between; align-items: center;
  padding: 0.5rem 0.75rem; border: 1px solid var(--border); border-radius: 6px;
  margin-bottom: 0.4rem; cursor: pointer; background: var(--bg-0);
}
.snippet-item:hover { border-color: var(--accent); }
.snippet-item .info { flex: 1; }
.snippet-item .title { color: var(--text-hi); font-weight: 600; font-size: 0.85rem; }
.snippet-item .meta { color: var(--text-mute); font-size: 0.75rem; }
.snippet-item .del-btn {
  background: none; border: none; color: var(--text-mute); cursor: pointer; font-size: 0.85rem;
}
.snippet-item .del-btn:hover { color: var(--danger); }

/* Variable fill modal */
#var-modal {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 90%; max-width: 550px; z-index: 101; max-height: 85vh; overflow-y: auto;
}
#var-modal h3 { margin: 0 0 0.5rem; font-size: 1rem; color: var(--text-hi); }
#var-modal .desc { font-size: 0.8rem; color: var(--text-mute); margin-bottom: 0.75rem; }
.var-row { margin-top: 0.6rem; padding: 0.5rem; background: var(--bg-2); border-radius: 6px; }
.var-row .var-name { font-size: 0.8rem; color: var(--accent); font-family: ui-monospace, Menlo, Consolas, monospace; font-weight: 600; }
.var-row input {
  width: 100%; padding: 0.35rem 0.5rem; background: var(--bg-0); border: 1px solid var(--border);
  border-radius: 4px; color: var(--text); font-size: 0.85rem; box-sizing: border-box;
  font-family: ui-monospace, Menlo, Consolas, monospace;
}
.var-row input:focus { border-color: var(--accent); outline: none; }
#var-modal .preview {
  margin-top: 0.75rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 6px;
  padding: 0.75rem; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 0.8rem;
  overflow-x: auto; white-space: pre; max-height: 200px; overflow-y: auto; color: var(--text);
}
#var-modal .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: flex-end; }
#var-modal .btns button { padding: 0.35rem 1rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }

/* Values modal */
#val-modal {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 90%; max-width: 550px; z-index: 101; max-height: 85vh; overflow-y: auto;
}
#val-modal h3 { margin: 0 0 0.75rem; font-size: 1rem; color: var(--text-hi); }
#val-modal .val-item {
  display: flex; justify-content: space-between; align-items: center;
  padding: 0.5rem 0.75rem; border: 1px solid var(--border); border-radius: 6px;
  margin-bottom: 0.4rem; cursor: pointer; background: var(--bg-0);
}
#val-modal .val-item:hover { border-color: var(--accent); }
#val-modal .val-item .info { flex: 1; }
#val-modal .val-item .title { color: var(--text-hi); font-weight: 600; font-size: 0.85rem; }
#val-modal .val-item .meta { color: var(--text-mute); font-size: 0.75rem; }
#val-modal .val-item .val-actions { display: flex; gap: 0.3rem; }
#val-modal .val-item .val-actions button {
  background: none; border: 1px solid var(--border); color: var(--text-mute); border-radius: 3px;
  padding: 0.15rem 0.4rem; cursor: pointer; font-size: 0.7rem;
}
#val-modal .val-item .val-actions button:hover { color: var(--text-hi); border-color: var(--text-mute); }
#val-modal .val-item .val-actions button.danger:hover { color: var(--danger); border-color: var(--danger); }
#val-modal .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: flex-end; }
#val-modal .btns button { padding: 0.35rem 1rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }

/* Value editor modal */
#val-edit-modal {
  display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 1.25rem; width: 90%; max-width: 550px; z-index: 102; max-height: 85vh; overflow-y: auto;
}
#val-edit-modal h3 { margin: 0 0 0.5rem; font-size: 1rem; color: var(--text-hi); }
#val-edit-modal label { display: block; font-size: 0.8rem; color: var(--text-mute); margin-bottom: 0.2rem; margin-top: 0.5rem; }
#val-edit-modal input {
  width: 100%; padding: 0.4rem 0.5rem; background: var(--bg-0); border: 1px solid var(--border);
  border-radius: 4px; color: var(--text); font-size: 0.85rem; box-sizing: border-box;
}
#val-edit-modal input:focus { border-color: var(--accent); outline: none; }
#val-edit-modal .btns { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: flex-end; }
#val-edit-modal .btns button { padding: 0.35rem 1rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }

.spinner { display: inline-block; width: 14px; height: 14px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; vertical-align: middle; margin-right: 0.3rem; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 700px) {
  #main { flex-direction: column; }
  #editor-pane { border-right: none; border-bottom: 1px solid var(--border); min-height: 40%; }
  #chat-pane { width: 100%; }
  #snippets-panel { width: 90%; }
}
`

const cwBody = `
<div id="toolbar">
  <button class="secondary" onclick="showSnippets()">Snippets</button>
  <input id="script-name" type="text" placeholder="Script name...">
  <select id="lang-select">
    <option value="bash">bash</option>
    <option value="sql">sql</option>
    <option value="python">python</option>
    <option value="powershell">powershell</option>
    <option value="go">go</option>
    <option value="">other</option>
  </select>
  <button onclick="saveSnippet()">Save</button>
  <button class="secondary" onclick="setVariables()">Variables</button>
  <button class="secondary" onclick="showValues()">Values</button>
  <button class="secondary" onclick="newScript()">New</button>
</div>
<div id="main">
  <div id="editor-pane">
    <textarea id="editor" placeholder="Your script appears here...

Ask the LLM in the chat to write a script, then click Apply to place it here.
Edit it directly, ask for changes, save it for later.

Use {{VARIABLE_NAME}} placeholders for reusable values.
Example: SELECT * FROM {{TABLE}} WHERE {{COLUMN}} = '{{VALUE}}'"></textarea>
    <div id="context-toggle" onclick="toggleContext()">
      <span class="arrow" id="context-arrow">&#9654;</span> Context (table schemas, reference docs, notes)
    </div>
    <div id="context-pane">
      <textarea id="context-editor" placeholder="Paste table schemas, DDL, column descriptions, API docs, or any reference material here.

The LLM reads this alongside the editor when you chat.

Example:
  users (id INT PK, email VARCHAR(255), created_at TIMESTAMP)
  orders (id INT PK, user_id INT FK->users.id, total DECIMAL, status ENUM('pending','paid','shipped'))"></textarea>
    </div>
  </div>
  <div id="chat-pane">
    <div id="chat-header">
      <span>Chat</span>
      <button onclick="document.getElementById('chat-messages').innerHTML=''">Clear</button>
    </div>
    <div id="chat-messages"></div>
    <div id="chat-input-area">
      <input id="chat-input" type="text" placeholder="Ask the LLM to write or modify code..." onkeydown="if(event.key==='Enter')sendChat()">
      <button id="chat-send" onclick="sendChat()">Send</button>
    </div>
  </div>
</div>
<div id="overlay" onclick="hideSnippets()"></div>
<div id="snippets-panel"><h3>Saved Snippets</h3><div id="snippets-list"></div></div>
<div id="var-modal">
  <h3 id="var-modal-title">Set Variables</h3>
  <div class="desc">Each variable can be a static value or a shell command that runs to produce the value.</div>
  <div id="var-inputs"></div>
  <div id="var-preview" class="preview"></div>
  <div class="btns">
    <button class="secondary" onclick="hideVarModal()">Cancel</button>
    <button onclick="applyVars()">Apply</button>
  </div>
</div>

<div id="val-modal">
  <h3>Values</h3>
  <div id="val-list"></div>
  <div class="btns">
    <button class="secondary" onclick="hideValModal()">Close</button>
    <button onclick="newValue()">New</button>
  </div>
</div>

<div id="val-edit-modal">
  <h3 id="val-edit-title">New Value</h3>
  <label>Name</label>
  <input id="val-edit-name" type="text" placeholder="e.g. MySQL Prod Password">
  <label>Description</label>
  <input id="val-edit-desc" type="text" placeholder="Optional">
  <label>Value</label>
  <input id="val-edit-value" type="text" placeholder="Text, command, connection string, etc." style="font-family:ui-monospace,Menlo,Consolas,monospace">
  <div class="btns">
    <button class="secondary" onclick="hideValEditModal()">Cancel</button>
    <button onclick="saveValue()">Save</button>
  </div>
</div>
`

const cwJS = `
var currentId = null;
var currentName = null;

function escapeHtml(s) {
  return String(s == null ? '' : s)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

// Extract {{VAR_NAME}} placeholders from code.
function extractVars(code) {
  var re = /\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}/g;
  var vars = [];
  var seen = {};
  var m;
  while ((m = re.exec(code)) !== null) {
    if (!seen[m[1]]) {
      vars.push(m[1]);
      seen[m[1]] = true;
    }
  }
  return vars;
}

// Format LLM response: convert fenced code blocks to <pre><code> and inline code.
function formatChat(text) {
  // Fenced code blocks.
  text = text.replace(/` + "```" + `(\\w*)\\n([\\s\\S]*?)` + "```" + `/g, function(m, lang, code) {
    return '<pre><code>' + escapeHtml(code.replace(/\\n$/, '')) + '</code></pre>';
  });
  // Inline code.
  text = text.replace(/` + "`" + `([^` + "`" + `]+)` + "`" + `/g, function(m, code) {
    return '<code>' + escapeHtml(code) + '</code>';
  });
  // Escape the rest but preserve existing HTML tags we just created.
  // (We already handled code blocks, so just convert newlines.)
  text = text.replace(/\n/g, '<br>');
  return text;
}

function addChatMsg(role, html, codeData) {
  var div = document.createElement('div');
  div.className = 'chat-msg ' + role;
  div.innerHTML = html;
  if (codeData) div.dataset.code = codeData;
  var msgs = document.getElementById('chat-messages');
  msgs.appendChild(div);
  msgs.scrollTop = msgs.scrollHeight;
}

// --- Chat ---

function sendChat() {
  var input = document.getElementById('chat-input');
  var msg = input.value.trim();
  if (!msg) return;
  input.value = '';
  addChatMsg('user', escapeHtml(msg));
  chatAPI(msg);
}

function toggleContext() {
  var pane = document.getElementById('context-pane');
  var arrow = document.getElementById('context-arrow');
  pane.classList.toggle('open');
  arrow.classList.toggle('open');
}

function chatAPI(message) {
  var name = document.getElementById('script-name').value.trim();
  var lang = document.getElementById('lang-select').value;
  var code = document.getElementById('editor').value;
  var ctx = document.getElementById('context-editor').value;
  document.getElementById('chat-send').disabled = true;
  addChatMsg('assistant', '<span class="spinner"></span> Thinking...');
  var thinkingMsg = document.getElementById('chat-messages').lastChild;

  fetch('api/chat', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name: name, lang: lang, code: code, context: ctx, message: message})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    if (thinkingMsg) thinkingMsg.remove();
    document.getElementById('chat-send').disabled = false;
    if (data.type === 'code' && data.code) {
      var html = formatChat(data.content);
      html += ' <button class="apply-btn" onclick="applyCode(this)">Apply to Editor</button>';
      addChatMsg('assistant', html, data.code);
    } else {
      addChatMsg('assistant', formatChat(data.content));
    }
  }).catch(function(err) {
    if (thinkingMsg) thinkingMsg.remove();
    document.getElementById('chat-send').disabled = false;
    addChatMsg('assistant', 'Error: ' + escapeHtml(err.message));
  });
}

function applyCode(btn) {
  var msg = btn.closest('.chat-msg');
  var code = msg.dataset.code;
  if (code) {
    document.getElementById('editor').value = code;
    btn.textContent = 'Applied!';
    btn.disabled = true;
    setTimeout(function() { btn.textContent = 'Apply to Editor'; btn.disabled = false; }, 1500);
  }
}

// --- Save / Load ---

// In-memory variable values: { VAR_NAME: "text to insert" }
var savedVarValues = {};

function saveSnippet() {
  var name = document.getElementById('script-name').value.trim();
  var code = document.getElementById('editor').value.trim();
  if (!name) { document.getElementById('script-name').focus(); addChatMsg('assistant', 'Enter a script name before saving.'); return; }
  if (!code) { addChatMsg('assistant', 'Editor is empty. Nothing to save.'); return; }

  var lang = document.getElementById('lang-select').value;
  var vars = {};
  var varNames = extractVars(code);
  for (var i = 0; i < varNames.length; i++) {
    vars[varNames[i]] = savedVarValues[varNames[i]] || '';
  }

  var body = {name: name, lang: lang, code: code, vars: vars};
  if (currentId && currentName === name) body.id = currentId;

  fetch('api/snippets', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body)
  }).then(function(r) { return r.json(); }).then(function(data) {
    currentId = data.id;
    currentName = name;
    addChatMsg('assistant', 'Saved <strong>' + escapeHtml(name) + '</strong>.');
  }).catch(function(err) {
    addChatMsg('assistant', 'Save failed: ' + err.message);
  });
}

function showSnippets() {
  document.getElementById('overlay').style.display = 'block';
  document.getElementById('snippets-panel').style.display = 'block';
  loadSnippets();
}

function hideSnippets() {
  document.getElementById('overlay').style.display = 'none';
  document.getElementById('snippets-panel').style.display = 'none';
}

function loadSnippets() {
  fetch('api/snippets').then(function(r) { return r.json(); }).then(function(items) {
    var list = document.getElementById('snippets-list');
    if (!items || items.length === 0) {
      list.innerHTML = '<div style="color:var(--text-mute);font-size:0.85rem;padding:0.5rem">No saved snippets yet.</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var s = items[i];
      var date = s.date ? new Date(s.date).toLocaleDateString() : '';
      var varCount = extractVars(s.code).length;
      var meta = s.lang + ' &middot; ' + date;
      if (varCount > 0) meta += ' &middot; ' + varCount + ' var' + (varCount > 1 ? 's' : '');
      html += '<div class="snippet-item" onclick="loadSnippet(\'' + s.id + '\')">'
        + '<div class="info"><div class="title">' + escapeHtml(s.name) + '</div><div class="meta">' + meta + '</div></div>'
        + '<button class="del-btn" onclick="event.stopPropagation();deleteSnippet(\'' + s.id + '\')" title="Delete">&times;</button>'
        + '</div>';
    }
    list.innerHTML = html;
  });
}

function loadSnippet(id) {
  fetch('api/snippet/' + id).then(function(r) { return r.json(); }).then(function(s) {
    currentId = s.id;
    currentName = s.name;
    document.getElementById('script-name').value = s.name;
    document.getElementById('editor').value = s.code;
    var sel = document.getElementById('lang-select');
    var found = false;
    for (var i = 0; i < sel.options.length; i++) {
      if (sel.options[i].value === s.lang) { sel.selectedIndex = i; found = true; break; }
    }
    if (!found) sel.value = '';
    hideSnippets();

    // Load saved variable values.
    savedVarValues = {};
    if (s.vars) {
      for (var k in s.vars) {
        if (s.vars[k]) savedVarValues[k] = s.vars[k];
      }
    }

    var vars = extractVars(s.code);
    if (vars.length > 0) {
      addChatMsg('assistant', 'Loaded <strong>' + escapeHtml(s.name) + '</strong> with ' + vars.length + ' variable' + (vars.length > 1 ? 's' : '') + '. Click <strong>Variables</strong> to fill in values.');
    } else {
      addChatMsg('assistant', 'Loaded <strong>' + escapeHtml(s.name) + '</strong>.');
    }
  });
}

function deleteSnippet(id) {
  fetch('api/snippet/' + id, {method: 'DELETE'}).then(function() {
    loadSnippets();
    if (currentId === id) currentId = null;
  });
}

function newScript() {
  currentId = null;
  currentName = null;
  savedVarValues = {};
  document.getElementById('script-name').value = '';
  document.getElementById('editor').value = '';
  document.getElementById('context-editor').value = '';
  document.getElementById('lang-select').selectedIndex = 0;
}

// --- Variables ---

function setVariables() {
  var code = document.getElementById('editor').value;
  var vars = extractVars(code);
  if (vars.length === 0) {
    addChatMsg('assistant', 'No <code>{{VARIABLE}}</code> placeholders found in the editor. Add placeholders like <code>{{TABLE_NAME}}</code> or <code>{{HOST}}</code> to use variables.');
    return;
  }
  // Fetch saved values so we can offer them as options.
  fetch('api/values').then(function(r) { return r.json(); }).then(function(items) {
    showVarModal(vars, items || []);
  }).catch(function() { showVarModal(vars, []); });
}

function showVarModal(vars, savedValues) {
  var container = document.getElementById('var-inputs');
  container.innerHTML = '';
  for (var i = 0; i < vars.length; i++) {
    var v = vars[i];
    var row = document.createElement('div');
    row.className = 'var-row';
    var nameSpan = document.createElement('div');
    nameSpan.className = 'var-name';
    nameSpan.textContent = '{{' + v + '}}';
    nameSpan.style.marginBottom = '0.2rem';
    row.appendChild(nameSpan);
    var inputRow = document.createElement('div');
    inputRow.style.display = 'flex';
    inputRow.style.gap = '0.4rem';
    inputRow.style.alignItems = 'center';
    var inp = document.createElement('input');
    inp.type = 'text';
    inp.setAttribute('data-var', v);
    inp.value = savedVarValues[v] || '';
    inp.placeholder = 'Type a value or pick from saved values';
    inp.style.fontFamily = 'ui-monospace, Menlo, Consolas, monospace';
    inp.style.flex = '1';
    inp.oninput = updateVarPreview;
    inputRow.appendChild(inp);
    var sel = document.createElement('select');
    sel.style.cssText = 'font-size:0.8rem;padding:0.3rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;color:var(--text);max-width:180px';
    var opt = document.createElement('option');
    opt.value = '';
    opt.textContent = 'Pick...';
    sel.appendChild(opt);
    // Script arguments.
    var argGroup = document.createElement('optgroup');
    argGroup.label = 'Script Arguments';
    for (var a = 1; a <= 9; a++) {
      var ao = document.createElement('option');
      ao.value = '$' + a;
      ao.textContent = '$' + a + ' (arg ' + a + ')';
      argGroup.appendChild(ao);
    }
    sel.appendChild(argGroup);
    // Saved values.
    if (savedValues.length > 0) {
      var valGroup = document.createElement('optgroup');
      valGroup.label = 'Saved Values';
      for (var j = 0; j < savedValues.length; j++) {
        var o = document.createElement('option');
        o.value = savedValues[j].value;
        o.textContent = savedValues[j].name;
        valGroup.appendChild(o);
      }
      sel.appendChild(valGroup);
    }
    sel.setAttribute('data-for', v);
    sel.onchange = function() {
      var varName = this.getAttribute('data-for');
      var input = container.querySelector('input[data-var="' + varName + '"]');
      if (this.value && input) {
        input.value = this.value;
        updateVarPreview();
      }
      this.selectedIndex = 0;
    };
    inputRow.appendChild(sel);
    row.appendChild(inputRow);
    container.appendChild(row);
  }
  document.getElementById('var-modal').style.display = 'block';
  document.getElementById('overlay').style.display = 'block';
  updateVarPreview();
}

function updateVarPreview() {
  var code = document.getElementById('editor').value;
  var inputs = document.querySelectorAll('#var-inputs input[data-var]');
  for (var i = 0; i < inputs.length; i++) {
    var v = inputs[i].getAttribute('data-var');
    var val = inputs[i].value || '{{' + v + '}}';
    code = code.split('{{' + v + '}}').join(val);
  }
  document.getElementById('var-preview').textContent = code;
}

function applyVars() {
  var code = document.getElementById('editor').value;
  var inputs = document.querySelectorAll('#var-inputs input[data-var]');
  for (var i = 0; i < inputs.length; i++) {
    var v = inputs[i].getAttribute('data-var');
    var val = inputs[i].value;
    if (val) {
      savedVarValues[v] = val;
      code = code.split('{{' + v + '}}').join(val);
    }
  }
  document.getElementById('editor').value = code;
  hideVarModal();

  // Persist variable values back to the saved snippet.
  if (currentId) {
    fetch('api/snippets', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        id: currentId,
        name: document.getElementById('script-name').value.trim(),
        lang: document.getElementById('lang-select').value,
        code: document.getElementById('editor').value,
        vars: savedVarValues
      })
    });
  }
}

function hideVarModal() {
  document.getElementById('var-modal').style.display = 'none';
  document.getElementById('overlay').style.display = 'none';
}

// Handle Tab key in editor for indentation.
document.getElementById('editor').addEventListener('keydown', function(e) {
  if (e.key === 'Tab') {
    e.preventDefault();
    var start = this.selectionStart;
    var end = this.selectionEnd;
    this.value = this.value.substring(0, start) + '    ' + this.value.substring(end);
    this.selectionStart = this.selectionEnd = start + 4;
  }
});

// --- Saved Values ---

var editingValId = null;

function showValues() {
  document.getElementById('overlay').style.display = 'block';
  document.getElementById('val-modal').style.display = 'block';
  loadValues();
}

function hideValModal() {
  document.getElementById('val-modal').style.display = 'none';
  document.getElementById('overlay').style.display = 'none';
}

function loadValues() {
  fetch('api/values').then(function(r) { return r.json(); }).then(function(items) {
    var list = document.getElementById('val-list');
    if (!items || items.length === 0) {
      list.innerHTML = '<div style="color:var(--text-mute);font-size:0.85rem;padding:0.5rem">No saved values yet.</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var v = items[i];
      html += '<div class="val-item">'
        + '<div class="info">'
        + '<div class="title">' + escapeHtml(v.name) + '</div>'
        + (v.desc ? '<div class="meta">' + escapeHtml(v.desc) + '</div>' : '')
        + '</div>'
        + '<div class="val-actions">'
        + '<button onclick="event.stopPropagation();editValue(\'' + v.id + '\')">Edit</button>'
        + '<button class="danger" onclick="event.stopPropagation();deleteValue(\'' + v.id + '\')">Delete</button>'
        + '</div></div>';
    }
    list.innerHTML = html;
  });
}

function deleteValue(id) {
  fetch('api/value/' + id, {method: 'DELETE'}).then(function() { loadValues(); });
}

function newValue() {
  editingValId = null;
  document.getElementById('val-edit-title').textContent = 'New Value';
  document.getElementById('val-edit-name').value = '';
  document.getElementById('val-edit-desc').value = '';
  document.getElementById('val-edit-value').value = '';
  document.getElementById('val-edit-modal').style.display = 'block';
  document.getElementById('val-edit-name').focus();
}

function editValue(id) {
  fetch('api/value/' + id).then(function(r) { return r.json(); }).then(function(v) {
    editingValId = v.id;
    document.getElementById('val-edit-title').textContent = 'Edit Value';
    document.getElementById('val-edit-name').value = v.name;
    document.getElementById('val-edit-desc').value = v.desc || '';
    document.getElementById('val-edit-value').value = v.value || '';
    document.getElementById('val-edit-modal').style.display = 'block';
  });
}

function saveValue() {
  var name = document.getElementById('val-edit-name').value.trim();
  if (!name) { document.getElementById('val-edit-name').focus(); return; }

  var body = {
    name: name,
    desc: document.getElementById('val-edit-desc').value.trim(),
    value: document.getElementById('val-edit-value').value
  };
  if (editingValId) body.id = editingValId;

  fetch('api/values', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body)
  }).then(function(r) { return r.json(); }).then(function() {
    hideValEditModal();
    loadValues();
  }).catch(function(err) {
    addChatMsg('assistant', 'Save failed: ' + err.message);
  });
}

function hideValEditModal() {
  document.getElementById('val-edit-modal').style.display = 'none';
}
`
