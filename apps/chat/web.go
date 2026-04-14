package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webui"
)

func (T *ChatAgent) WebPath() string { return "/chat" }
func (T *ChatAgent) WebName() string { return "Chat (Tool Tester)" }
func (T *ChatAgent) WebDesc() string {
	return "Test tools against the local worker LLM via a simple chat interface."
}

func (T *ChatAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := http.NewServeMux()
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "Chat — Tool Tester",
			AppName:  "Chat",
			Prefix:   prefix,
			BodyHTML: chatBody,
			AppCSS:   chatCSS,
			AppJS:    chatJS,
		}))
	})
	sub.HandleFunc("/api/send", T.handleSend)
	sub.HandleFunc("/api/tools", T.handleTools)
	MountSubMux(mux, prefix, sub)
}

// blockedTools is the set of tool names the chat app refuses to expose,
// regardless of what the client requests. Tools that perform real-world
// side effects (sending email, executing shell commands) or that need a
// configured API key (web search) are blocked from the testing UI to keep
// it sandboxed.
var blockedTools = map[string]bool{
	"run_command": true, // shell execution — risky in a web UI
	"send_email":  true, // sends real email
	"web_search":  true, // can burn API quotas / rate limits
}

// allowedTools returns the registered tool list filtered by the blocklist.
func allowedTools() []ChatTool {
	var out []ChatTool
	for _, t := range RegisteredChatTools() {
		if blockedTools[t.Name()] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// handleTools returns the list of allowed tool names + descriptions so
// the UI can show the user which tools are available.
func (T *ChatAgent) handleTools(w http.ResponseWriter, r *http.Request) {
	type toolInfo struct {
		Name string `json:"name"`
		Desc string `json:"desc"`
	}
	var out []toolInfo
	for _, t := range allowedTools() {
		out = append(out, toolInfo{Name: t.Name(), Desc: t.Desc()})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// chatRequest is the wire format from the frontend.
type chatRequest struct {
	History []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
	Message string   `json:"message"`
	Tools   []string `json:"tools"` // optional whitelist; empty = all
}

// (chatResponse / toolCall types removed — chat now streams via SSE
// with discrete event types: chunk, tool_call, tool_result, done, error.)

// activeChats serializes per-IP requests so the same user can't have two
// in flight at once. Cheap concurrency limit for a testing tool.
var (
	activeChatsMu sync.Mutex
	activeChats   = make(map[string]bool)
)

func (T *ChatAgent) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	// Per-IP single-flight guard.
	clientKey := r.RemoteAddr
	activeChatsMu.Lock()
	if activeChats[clientKey] {
		activeChatsMu.Unlock()
		http.Error(w, "another request is in progress", http.StatusTooManyRequests)
		return
	}
	activeChats[clientKey] = true
	activeChatsMu.Unlock()
	defer func() {
		activeChatsMu.Lock()
		delete(activeChats, clientKey)
		activeChatsMu.Unlock()
	}()

	// Build the message history for the agent loop. The frontend tracks
	// the conversation client-side and sends it on every request.
	messages := make([]Message, 0, len(req.History)+1)
	for _, m := range req.History {
		messages = append(messages, Message{Role: m.Role, Content: m.Content})
	}
	messages = append(messages, Message{Role: "user", Content: req.Message})

	// Resolve tools. The chat app enforces its own blocklist (shell, email,
	// web search) regardless of what the client requests, so a malicious or
	// curious user can't pull a blocked tool into the chat by name.
	var toolNames []string
	if len(req.Tools) > 0 {
		for _, name := range req.Tools {
			if !blockedTools[name] {
				toolNames = append(toolNames, name)
			}
		}
	} else {
		for _, t := range allowedTools() {
			toolNames = append(toolNames, t.Name())
		}
	}

	agent := &FuzzAgent{LLM: T.FuzzAgent.LLM, LeadLLM: T.FuzzAgent.LeadLLM, MaxRounds: 8, PromptTools: T.FuzzAgent.PromptTools}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	tools, terr := GetAgentTools(toolNames...)
	if terr != nil {
		writeSSEEvent(w, "error", map[string]string{"message": "tool resolve failed: " + terr.Error()})
		return
	}

	// Set up SSE headers — single open response, server pushes events as
	// the agent loop progresses (chunks, tool calls, tool results, done).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Streaming agent loop. Mirrors RunAgentLoop's structure but uses
	// ChatStream so we can push tokens to the client as they arrive, and
	// emits SSE events at each phase (chunks, tool calls, tool results).
	streamMessages := make([]Message, len(messages))
	copy(streamMessages, messages)

	maxRounds := 8
	systemPrompt := T.SystemPrompt() + buildProcedurePrompt(T.DB)
	promptTools := agent.PromptTools

	toolDefs := make([]Tool, 0, len(tools))
	handlers := make(map[string]ToolHandlerFunc)
	for _, td := range tools {
		toolDefs = append(toolDefs, td.Tool)
		handlers[td.Tool.Name] = td.Handler
	}

	// In PromptTools mode, inject tool descriptions into the system prompt.
	if promptTools && len(tools) > 0 {
		systemPrompt += BuildToolPrompt(tools)
	}

	for round := 1; round <= maxRounds; round++ {
		opts := []ChatOption{}
		if systemPrompt != "" {
			opts = append(opts, WithSystemPrompt(systemPrompt))
		}
		// Only offer native tools when NOT in PromptTools mode.
		if !promptTools && len(toolDefs) > 0 && round < maxRounds {
			opts = append(opts, WithTools(toolDefs))
		}
		opts = append(opts, WithMaxTokens(2048), WithThink(false))

		// Stream this round's response. In PromptTools mode, stream chunks
		// to the client but hold back a trailing buffer that could be the
		// start of a <tool_call> tag. Once we're sure the trailing text is
		// NOT a tag prefix, flush it. If a full tag appears, stop streaming
		// and let the post-response handler deal with it.
		// Control tags that should be suppressed from the stream.
		// All start with "<" so we hold back content once we see "<".
		controlTags := []string{"<tool_call>", "<save_procedure>", "<delete_procedure>"}

		var promptBuf strings.Builder
		var holdback string       // trailing chars that might be a control tag
		var pendingNewlines int   // deferred trailing newlines — only emitted when more text follows
		tagDetected := false      // true once a control tag is found

		// emitChunk sends text to the client but defers trailing newlines.
		// They only get emitted when more non-whitespace text follows,
		// so the response never ends with blank lines.
		emitChunk := func(text string) {
			if text == "" {
				return
			}
			// Strip trailing newlines and defer them.
			trimmed := strings.TrimRight(text, "\n\r")
			trailingCount := len(text) - len(trimmed)

			// If we have deferred newlines and new non-empty content, emit them first.
			if pendingNewlines > 0 && trimmed != "" {
				nl := strings.Repeat("\n", pendingNewlines)
				writeSSEEvent(w, "chunk", map[string]string{"text": nl})
				flusher.Flush()
				pendingNewlines = 0
			}

			if trimmed != "" {
				writeSSEEvent(w, "chunk", map[string]string{"text": trimmed})
				flusher.Flush()
			}
			pendingNewlines += trailingCount
		}

		resp, err := agent.LLM.ChatStream(ctx, streamMessages, func(chunk string) {
			if chunk == "" {
				return
			}
			if !promptTools {
				emitChunk(chunk)
				return
			}

			promptBuf.WriteString(chunk)

			if tagDetected {
				return // inside a control tag, suppress everything
			}

			holdback += chunk

			// Check if any control tag appeared in the holdback.
			for _, tag := range controlTags {
				if idx := strings.Index(holdback, tag); idx >= 0 {
					tagDetected = true
					if idx > 0 {
						emitChunk(holdback[:idx])
					}
					holdback = ""
					return
				}
			}

			// If holdback contains a "<", hold everything from the "<"
			// onward — it might be the start of a control tag.
			if idx := strings.LastIndex(holdback, "<"); idx >= 0 {
				safe := holdback[:idx]
				if safe != "" {
					emitChunk(safe)
				}
				holdback = holdback[idx:]
				return
			}

			// No "<" anywhere — safe to flush everything.
			emitChunk(holdback)
			holdback = ""
		}, opts...)
		if err != nil {
			writeSSEEvent(w, "error", map[string]string{"message": err.Error()})
			flusher.Flush()
			return
		}

		// PromptTools path: parse <tool_call> tags from the buffered text.
		// Emit only the preamble (text before the tag) to the client.
		if promptTools {
			if resp == nil {
				writeSSEEvent(w, "done", map[string]any{"round": round})
				flusher.Flush()
				return
			}

			tc, preamble := ParsePromptToolCall(resp.Content, handlers)
			if tc == nil {
				// No tool call — parse procedure tags from the full buffer,
				// then flush any remaining holdback (stripped of procedure tags).
				parseProcedureActions(T.DB, promptBuf.String())
				remaining := strings.TrimRight(parseProcedureActions(nil, holdback), "\n\r ") // strip tags + trailing whitespace
				if remaining != "" {
					writeSSEEvent(w, "chunk", map[string]string{"text": remaining})
					flusher.Flush()
				}
				writeSSEEvent(w, "done", map[string]any{"round": round})
				flusher.Flush()
				return
			}

			// Emit only the preamble (text before <tool_call>) to the client.
			// Do NOT add preamble to message history — the LLM will repeat it
			// on the next round if it sees its own preamble as a prior message.
			if preamble = strings.TrimRight(preamble, "\n\r "); preamble != "" {
				emitChunk(preamble)
			}

			// Execute the tool and send SSE events.
			args_json, _ := json.Marshal(tc.Args)
			writeSSEEvent(w, "tool_call", map[string]string{
				"name": tc.Name,
				"args": string(args_json),
			})
			flusher.Flush()

			output, toolErr := handlers[tc.Name](tc.Args)
			var resultText string
			if toolErr != nil {
				resultText = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, toolErr)
			} else {
				resultText = fmt.Sprintf("Tool result from %s:\n%s", tc.Name, output)
			}
			writeSSEEvent(w, "tool_result", map[string]string{
				"name":   tc.Name,
				"result": truncate(resultText, 2000),
			})
			flusher.Flush()

			// Send result back as a plain user message.
			streamMessages = append(streamMessages, Message{Role: "user", Content: resultText})
			continue
		}

		// Native tool path (existing behavior).

		// No tool calls → this is the final answer. Send done and exit.
		if resp == nil || len(resp.ToolCalls) == 0 {
			// Parse procedure saves/deletes from the streamed response.
			if resp != nil && resp.Content != "" {
				parseProcedureActions(T.DB, resp.Content)
			}
			writeSSEEvent(w, "done", map[string]any{"round": round})
			flusher.Flush()
			return
		}

		// Tool calls present. Append the assistant's tool-call message to
		// history, then run each tool and append the result via the next
		// message's ToolResults field (matches the framework's Message shape).
		assistantMsg := Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		streamMessages = append(streamMessages, assistantMsg)

		var results []ToolResult
		for _, tc := range resp.ToolCalls {
			args_json, _ := json.Marshal(tc.Args)
			writeSSEEvent(w, "tool_call", map[string]string{
				"name": tc.Name,
				"args": string(args_json),
			})
			flusher.Flush()

			handler, ok := handlers[tc.Name]
			var output string
			var toolErr error
			if !ok {
				toolErr = fmt.Errorf("unknown tool: %s", tc.Name)
			} else {
				output, toolErr = handler(tc.Args)
			}

			result := output
			isErr := toolErr != nil
			if isErr {
				result = "ERROR: " + toolErr.Error()
			}
			writeSSEEvent(w, "tool_result", map[string]string{
				"name":   tc.Name,
				"result": truncate(result, 2000),
			})
			flusher.Flush()

			results = append(results, ToolResult{
				ID:      tc.ID,
				Content: result,
				IsError: isErr,
			})
		}
		// Tool results go in a "user" role message with ToolResults set —
		// this matches the format the framework's RunAgentLoop uses (see
		// core/agent_loop.go) and is what buildMessages knows how to
		// translate to native ollama tool-response messages.
		streamMessages = append(streamMessages, Message{
			Role:        "user",
			ToolResults: results,
		})
	}

	// Hit the max-rounds cap without a final answer.
	writeSSEEvent(w, "error", map[string]string{"message": fmt.Sprintf("agent loop exceeded %d rounds", maxRounds)})
	flusher.Flush()
}

// writeSSEEvent writes a single Server-Sent Event with a name and JSON payload.
func writeSSEEvent(w http.ResponseWriter, eventType string, data any) {
	body, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(body))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... [truncated, %d chars total]", len(s))
}

// --- HTML / CSS / JS ---

const chatCSS = `
body { display: flex; flex-direction: column; height: 100vh; height: 100dvh; margin: 0; }
#chat-header { padding: 0.75rem 1rem; background: var(--bg-1); border-bottom: 1px solid var(--border); display: flex; align-items: center; gap: 0.75rem; }
#chat-header h1 { font-size: 1rem; margin: 0; color: var(--text-hi); }
#tools-summary { color: var(--text-mute); font-size: 0.85rem; margin-left: auto; cursor: pointer; }
#tools-summary:hover { color: var(--text-hi); }
#tools-list { display: none; padding: 0.5rem 1rem; background: var(--bg-2); border-bottom: 1px solid var(--border); font-size: 0.8rem; color: var(--text-mute); max-height: 200px; overflow-y: auto; }
#tools-list .tool { padding: 0.2rem 0; }
#tools-list .tool b { color: var(--text); margin-right: 0.5rem; }
#chat-history { flex: 1; overflow-y: auto; padding: 1rem; }
.chat-msg { max-width: 80%; margin-bottom: 0.75rem; padding: 0.6rem 0.85rem; border-radius: 8px; line-height: 1.5; }
.chat-msg.user { background: var(--accent); color: #fff; margin-left: auto; padding: 0.15rem 0.45rem; border-radius: 3px; max-width: fit-content; line-height: 1.35; }
.chat-msg.assistant { background: var(--bg-1); border: 1px solid var(--border); }
.chat-msg.error { background: #2d1a1a; border: 1px solid var(--danger); color: #ffb4b4; }
.chat-msg pre { white-space: pre-wrap; word-break: break-word; margin: 0; font-family: inherit; }
.tool-call { margin-top: 0.4rem; background: var(--bg-2); border-left: 3px solid var(--warn); border-radius: 4px; font-size: 0.8rem; color: var(--text-mute); }
.tool-call summary { padding: 0.4rem 0.6rem; cursor: pointer; list-style: none; display: flex; align-items: center; gap: 0.4rem; }
.tool-call summary::-webkit-details-marker { display: none; }
.tool-call summary::before { content: '▶'; font-size: 0.6rem; color: var(--text-mute); transition: transform 0.15s; }
.tool-call[open] summary::before { transform: rotate(90deg); }
.tool-call .name { color: var(--warn); font-weight: 600; }
.tool-call .tool-status { color: var(--text-mute); font-style: italic; font-size: 0.75rem; }
.tool-call.pending .tool-status { color: var(--text-mute); }
.tool-call:not(.pending) .tool-status { color: var(--green, #3fb950); font-style: normal; }
.tool-details { padding: 0.3rem 0.6rem 0.4rem; }
.tool-call .args, .tool-call .result { display: block; margin-top: 0.2rem; font-family: ui-monospace, Menlo, monospace; font-size: 0.75rem; white-space: pre-wrap; word-break: break-word; }
.tool-call .result { color: var(--text); }
.chat-msg.error { background: #2d1a1a; border: 1px solid var(--danger); color: #ffb4b4; }
#chat-input-area { display: flex; gap: 0.5rem; padding: 0.75rem 1rem; background: var(--bg-1); border-top: 1px solid var(--border); }
#chat-input { flex: 1; min-height: 38px; max-height: 200px; padding: 0.5rem 0.75rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 6px; color: var(--text); font-family: inherit; font-size: 0.9rem; resize: vertical; }
#chat-input:focus { border-color: var(--accent); outline: none; }
#chat-send { padding: 0 1.25rem; }
#chat-send:disabled { opacity: 0.5; cursor: not-allowed; }

/* Mobile responsive */
@media (max-width: 600px) {
  #chat-header { padding: 0.5rem; gap: 0.5rem; flex-wrap: wrap; }
  #chat-header h1 { font-size: 0.9rem; }
  #tools-summary { font-size: 0.75rem; }
  #chat-history { padding: 0.5rem; }
  .chat-msg { max-width: 95%; font-size: 0.9rem; padding: 0.5rem 0.7rem; }
  .chat-msg.user { max-width: 85%; }
  .chat-msg pre { font-size: 0.8rem; }
  .tool-call { font-size: 0.75rem; }
  .tool-call .args, .tool-call .result { font-size: 0.7rem; }
  #chat-input-area { padding: 0.5rem; padding-bottom: calc(0.5rem + env(safe-area-inset-bottom, 0px)); gap: 0.4rem; }
  #chat-input { font-size: 1rem; min-height: 44px; padding: 0.6rem; -webkit-appearance: none; }
  #chat-send { padding: 0 1rem; min-height: 44px; font-size: 0.9rem; }
  #tools-list { font-size: 0.75rem; }
}
`

const chatBody = `
<div id="chat-header">
  <h1>Chat — Tool Tester</h1>
  <span id="tools-summary" onclick="toggleTools()">Loading tools…</span>
</div>
<div id="tools-list"></div>
<div id="chat-history"></div>
<div id="chat-input-area">
  <textarea id="chat-input" placeholder="Message…" rows="1"></textarea>
  <button id="chat-send" class="primary" onclick="sendChat()">Send</button>
</div>
`

const chatJS = `
var chatHistory = [];
var sending = false;

function escapeHtml(s) {
  return String(s == null ? '' : s)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

function loadTools() {
  fetch('api/tools').then(function(r){return r.json()}).then(function(tools){
    var summary = document.getElementById('tools-summary');
    summary.textContent = tools.length + ' tools available — click to expand';
    var list = document.getElementById('tools-list');
    var html = '';
    for (var i = 0; i < tools.length; i++) {
      html += '<div class="tool"><b>' + escapeHtml(tools[i].name) + '</b>' + escapeHtml(tools[i].desc) + '</div>';
    }
    list.innerHTML = html;
  });
}

function toggleTools() {
  var list = document.getElementById('tools-list');
  list.style.display = list.style.display === 'block' ? 'none' : 'block';
}

function appendUserMessage(text) {
  var hist = document.getElementById('chat-history');
  var div = document.createElement('div');
  div.className = 'chat-msg user';
  div.innerHTML = '<pre>' + escapeHtml(text) + '</pre>';
  hist.appendChild(div);
  hist.scrollTop = hist.scrollHeight;
}

// Create an empty assistant message placeholder that streams will fill in.
function createAssistantPlaceholder() {
  var hist = document.getElementById('chat-history');
  var div = document.createElement('div');
  div.className = 'chat-msg assistant';
  div.innerHTML = '<pre class="content"></pre><div class="tools"></div>';
  hist.appendChild(div);
  hist.scrollTop = hist.scrollHeight;
  return div;
}

function appendChunk(msgEl, text) {
  var pre = msgEl.querySelector('.content');
  pre.textContent += text;
  var hist = document.getElementById('chat-history');
  hist.scrollTop = hist.scrollHeight;
}

function appendToolCall(msgEl, name, args) {
  var tools = msgEl.querySelector('.tools');
  var tc = document.createElement('details');
  tc.className = 'tool-call pending';
  tc.dataset.name = name;
  tc.innerHTML = '<summary><span class="name">' + escapeHtml(name) + '</span> <span class="tool-status">running…</span></summary>'
    + '<div class="tool-details">'
    + '<div class="args">args: ' + escapeHtml(args) + '</div>'
    + '<div class="result"></div>'
    + '</div>';
  tools.appendChild(tc);
  var hist = document.getElementById('chat-history');
  hist.scrollTop = hist.scrollHeight;
}

function appendToolResult(msgEl, name, result) {
  var tools = msgEl.querySelector('.tools');
  var pending = tools.querySelectorAll('.tool-call.pending');
  for (var i = pending.length - 1; i >= 0; i--) {
    if (pending[i].dataset.name === name) {
      pending[i].classList.remove('pending');
      pending[i].querySelector('.tool-status').textContent = '✓';
      pending[i].querySelector('.result').textContent = 'result: ' + result;
      var hist = document.getElementById('chat-history');
      hist.scrollTop = hist.scrollHeight;
      return;
    }
  }
}

function appendError(msgEl, text) {
  if (msgEl) {
    msgEl.classList.add('error');
    var pre = msgEl.querySelector('.content');
    if (pre) pre.textContent += '\n[error] ' + text;
  } else {
    var hist = document.getElementById('chat-history');
    var div = document.createElement('div');
    div.className = 'chat-msg error';
    div.innerHTML = '<pre>[error] ' + escapeHtml(text) + '</pre>';
    hist.appendChild(div);
  }
}

function sendChat() {
  if (sending) return;
  var input = document.getElementById('chat-input');
  var msg = input.value.trim();
  if (!msg) return;
  input.value = '';
  appendUserMessage(msg);
  sending = true;
  var btn = document.getElementById('chat-send');
  btn.disabled = true;
  btn.textContent = 'Thinking…';

  var assistantEl = createAssistantPlaceholder();
  var fullReply = '';

  fetch('api/send', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({history: chatHistory, message: msg})
  }).then(function(response) {
    if (!response.ok) {
      return response.text().then(function(t) { throw new Error(t || 'HTTP ' + response.status); });
    }
    var reader = response.body.getReader();
    var decoder = new TextDecoder();
    var buffer = '';

    function handleEvent(eventType, data) {
      try { data = JSON.parse(data); } catch(e) { return; }
      if (eventType === 'chunk' && data.text) {
        fullReply += data.text;
        appendChunk(assistantEl, data.text);
      } else if (eventType === 'tool_call') {
        appendToolCall(assistantEl, data.name, data.args);
      } else if (eventType === 'tool_result') {
        appendToolResult(assistantEl, data.name, data.result);
      } else if (eventType === 'done') {
        finishChat(true);
      } else if (eventType === 'error') {
        appendError(assistantEl, data.message || 'unknown error');
        finishChat(false);
      }
    }

    function finishChat(success) {
      sending = false;
      btn.disabled = false;
      btn.textContent = 'Send';
      // Trim trailing whitespace from the rendered message.
      if (assistantEl) {
        var pre = assistantEl.querySelector('.content');
        if (pre) pre.textContent = pre.textContent.replace(/\s+$/, '');
      }
      if (success && fullReply) {
        chatHistory.push({role: 'user', content: msg});
        chatHistory.push({role: 'assistant', content: fullReply.replace(/\s+$/, '')});
      }
    }

    function pump() {
      return reader.read().then(function(result) {
        if (result.done) {
          // Stream ended without explicit done — treat as done if we got content.
          if (sending) finishChat(fullReply.length > 0);
          return;
        }
        buffer += decoder.decode(result.value, {stream: true});
        // Parse SSE events: blocks separated by \n\n.
        var parts = buffer.split('\n\n');
        buffer = parts.pop(); // last part may be incomplete
        for (var i = 0; i < parts.length; i++) {
          var block = parts[i];
          var eventType = '';
          var dataLines = [];
          var lines = block.split('\n');
          for (var j = 0; j < lines.length; j++) {
            var line = lines[j];
            if (line.indexOf('event: ') === 0) {
              eventType = line.slice(7).trim();
            } else if (line.indexOf('data: ') === 0) {
              dataLines.push(line.slice(6));
            }
          }
          if (eventType && dataLines.length > 0) {
            handleEvent(eventType, dataLines.join('\n'));
          }
        }
        return pump();
      });
    }
    return pump();
  }).catch(function(err) {
    sending = false;
    btn.disabled = false;
    btn.textContent = 'Send';
    appendError(assistantEl, 'Request failed: ' + err.message);
  });
}

document.getElementById('chat-input').addEventListener('keydown', function(e){
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendChat();
  }
});

loadTools();
`
