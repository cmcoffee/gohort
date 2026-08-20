// AgentLoopPanel demo for /hello/agent. This wires the panel to a
// real LLM via RunAgentLoop with two demo tools: get_time (safe —
// no confirm) and write_note (gated on operator confirmation). The
// confirm callback fires the AgentLoopPanel's `confirm` SSE event
// and blocks until the operator answers via /api/agent/confirm.
//
// Apps porting onto AgentLoopPanel should use handleAgentSend as
// the reference bridge: list messages → RunAgentLoop with stream +
// onStep + confirm callbacks → translate each callback into the
// AgentLoopPanel SSE protocol. Everything else (state store, route
// handlers) is plumbing.

package hello

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// --- in-memory session store -------------------------------------------------

type demoMessage struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type demoSession struct {
	ID       string        `json:"ID"`
	Title    string        `json:"Title"`
	LastAt   string        `json:"LastAt"`
	Messages []demoMessage `json:"Messages"`
	Notes    []string      `json:"-"` // saved by the write_note demo tool

	// confirmCh — receives the operator's value when a confirm
	// prompt is answered. nil when no prompt is pending.
	confirmCh chan string
}

// demoStore holds every user's demo conversations, keyed by user and then by
// session id.
//
// The nesting is the point, and it is why this is a type with methods rather
// than the bare map it used to be. Every method NAMES a user, so there is no
// way to reach a session without saying whose it is — a copied handler that
// forgets to resolve the caller does not compile rather than serving everyone's
// conversations to everyone. That is the property to carry into a real app.
//
// Kept in memory because this is a demo and a demo should be readable. A real
// app does not hand-roll this at all: RequireUser returns a per-user Database
// alongside the username (see UserDB), and storing there gets tenancy AND
// persistence without a store of your own. Reach for that first.
type demoStore struct {
	mu sync.Mutex
	// byUser[user][sessionID]
	byUser map[string]map[string]*demoSession
}

var demoSessions = &demoStore{byUser: map[string]map[string]*demoSession{}}

func nextID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// List returns user's sessions, without message bodies — the sidebar only
// needs id, title and timestamp.
func (d *demoStore) List(user string) []demoSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []demoSession{}
	for _, s := range d.byUser[user] {
		out = append(out, demoSession{ID: s.ID, Title: s.Title, LastAt: s.LastAt})
	}
	return out
}

// Get returns one of user's sessions. A session belonging to somebody else is
// indistinguishable from one that does not exist, which is what lets the
// handler answer 404 to both and disclose neither.
func (d *demoStore) Get(user, id string) (*demoSession, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.byUser[user][id]
	return s, ok
}

// Delete removes one of user's sessions, reporting whether it was theirs.
func (d *demoStore) Delete(user, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.byUser[user][id]; !ok {
		return false
	}
	delete(d.byUser[user], id)
	return true
}

// GetOrCreate returns user's session by id, creating it when the id is empty
// or unknown. An id belonging to another user creates a fresh session under
// the caller instead of reaching theirs.
func (d *demoStore) GetOrCreate(user, id string) *demoSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.byUser[user] == nil {
		d.byUser[user] = map[string]*demoSession{}
	}
	if id != "" {
		if s, ok := d.byUser[user][id]; ok {
			return s
		}
	}
	if id == "" {
		id = nextID("s")
	}
	s := &demoSession{
		ID:     id,
		Title:  "New conversation",
		LastAt: time.Now().UTC().Format(time.RFC3339),
	}
	d.byUser[user][id] = s
	return s
}

// AnswerConfirm hands an operator's decision to whichever of USER'S OWN
// sessions is waiting on one, and reports whether it landed.
//
// The old version ranged over every session in the process and fed the first
// one with a pending channel, with a comment calling that fine because the
// demo has one user. It is the shape servitor copied, where the sessions were
// SSH investigations and either user's Allow released the other's command.
// Scoped to the caller, the shortcut is sound; unscoped it is a hole, and a
// scaffold should not be teaching the version that needs a caveat.
func (d *demoStore) AnswerConfirm(user, value string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.byUser[user] {
		if s.confirmCh == nil {
			continue
		}
		select {
		case s.confirmCh <- value:
			return true
		default:
		}
	}
	return false
}

// --- routes ------------------------------------------------------------------

func registerAgentRoutes(T *HelloAgent) {
	T.HandleFunc("/agent", T.handleAgentPage)
	T.HandleFunc("/agent/", T.handleAgentPage)
	T.HandleFunc("/api/agent/sessions", T.handleAgentSessions)
	T.HandleFunc("/api/agent/sessions/", T.handleAgentSession)
	T.HandleFunc("/api/agent/send", T.handleAgentSend)
	T.HandleFunc("/api/agent/confirm", T.handleAgentConfirm)
	T.HandleFunc("/api/agent/cancel", T.handleAgentCancel)
}

func (T *HelloAgent) handleAgentPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Hello — Agent Loop Demo",
		ShowTitle: true,
		BackURL:   ".",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					ListURL:       "api/agent/sessions",
					LoadURL:       "api/agent/sessions/{id}",
					DeleteURL:     "api/agent/sessions/{id}",
					ListTitle:     "Conversations",
					NewLabel:      "New conversation",
					SendURL:       "api/agent/send",
					CancelURL:     "api/agent/cancel",
					ConfirmURL:    "api/agent/confirm",
					DeepLinkParam: "session",
					EmptyText:     "Ask the assistant anything. It can also call two demo tools: get_time (no confirm) and write_note (requires your approval).",
					Placeholder:   "Try: 'what time is it?' or 'save a note that says hello'",
					Markdown:      true,
					BulkSelect:    true,
					Attachments:   true,
					// Terminal disabled in hello demo too until xterm
					// wiring is re-validated.
					// Terminal: &ui.AgentTerminal{
					//   URL:   "api/agent/terminal",
					//   Title: "Terminal",
					// },
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

// --- session list / load / delete -------------------------------------------

// Every handler below opens the same way: resolve the caller, then use that
// identity as the KEY into the store, not merely as a gate in front of it.
// Authenticating and then ignoring who it was is the mistake worth naming,
// because it looks like a check and is not one.
func (T *HelloAgent) handleAgentSessions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(demoSessions.List(user))
}

func (T *HelloAgent) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/agent/sessions/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		if !demoSessions.Delete(user, id) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s, found := demoSessions.Get(user, id)
	if !found {
		// 404 for "not yours" as well as "no such thing": the two are the
		// same answer from outside, and distinguishing them would confirm
		// that somebody else's session exists.
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// --- demo tools --------------------------------------------------------------

// demoTools returns the two-tool set the assistant has access to in
// this session. write_note is gated with NeedsConfirm so the demo
// exercises the operator-in-the-loop flow. get_time is wide-open so
// you can see a tool round-trip without the confirm card.
func demoTools(s *demoSession) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "get_time",
				Description: "Returns the current server time in RFC3339 format. Use when the user asks what time it is.",
			},
			Handler: func(args map[string]any) (string, error) {
				return time.Now().UTC().Format(time.RFC3339), nil
			},
		},
		{
			Tool: Tool{
				Name:        "write_note",
				Description: "Save a short text note to this conversation. Use when the user asks you to remember or write down something.",
				Parameters: map[string]ToolParam{
					"text": {Type: "string", Description: "The note to save."},
				},
				Required: []string{"text"},
			},
			NeedsConfirm: true,
			Handler: func(args map[string]any) (string, error) {
				text, _ := args["text"].(string)
				if strings.TrimSpace(text) == "" {
					return "", fmt.Errorf("text is required")
				}
				demoSessions.mu.Lock()
				s.Notes = append(s.Notes, text)
				count := len(s.Notes)
				demoSessions.mu.Unlock()
				return fmt.Sprintf("note saved (%d total)", count), nil
			},
		},
	}
}

// --- main send handler (real LLM agent loop) --------------------------------

func (T *HelloAgent) handleAgentSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		SessionID string   `json:"session_id"`
		Message   string   `json:"message"`
		Images    []string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stopKA := sse.StartKeepalive(15 * time.Second)
	defer stopKA()

	// Scoped to the caller: a session_id from someone else's conversation
	// starts a new one under this user rather than appending to theirs.
	s := demoSessions.GetOrCreate(user, req.SessionID)
	if (s.Title == "" || s.Title == "New conversation") && req.Message != "" {
		t := req.Message
		if len(t) > 60 {
			t = t[:60] + "…"
		}
		s.Title = t
	}
	s.LastAt = time.Now().UTC().Format(time.RFC3339)
	s.Messages = append(s.Messages, demoMessage{
		ID:      nextID("u"),
		Role:    "user",
		Content: req.Message,
	})

	// Tell the client which session it landed in. The runtime mirrors
	// this into the URL via DeepLinkParam.
	sse.Send(map[string]any{"kind": "session", "id": s.ID})
	sse.Send(map[string]any{"kind": "status", "text": "Thinking…"})

	// Build the message history the LLM sees from the session store.
	// Demo doesn't preserve tool calls in history — every round
	// starts with just the plain user/assistant exchanges.
	history := make([]Message, 0, len(s.Messages))
	for _, m := range s.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		history = append(history, Message{Role: m.Role, Content: m.Content})
	}

	// Pre-allocate the assistant message id so streamed chunks all
	// land in the same bubble. The runtime renders a fresh bubble
	// when it sees the first 'message' event and routes subsequent
	// 'chunk' events with the same id into that bubble's body.
	asstID := nextID("m")
	sse.Send(map[string]any{
		"kind": "message", "role": "assistant", "id": asstID, "text": "",
	})

	// Streaming handler — every LLM chunk becomes a `chunk` event.
	var fullReply strings.Builder
	stream := func(chunk string) {
		fullReply.WriteString(chunk)
		sse.Send(map[string]any{
			"kind": "chunk", "id": asstID, "text": chunk,
		})
	}

	// Tool-call observer — surface each tool invocation in the
	// activity pane. The cmd row shows `<tool>(<args>)`, the
	// output row shows the tool's return value. Demo keeps both
	// short; production apps decide what to surface.
	onStep := func(step StepInfo) {
		for _, tc := range step.ToolCalls {
			args, _ := json.Marshal(tc.Args)
			cmdID := nextID("a")
			sse.Send(map[string]any{
				"kind": "activity", "type": "cmd", "id": cmdID,
				"text": tc.Name + "(" + string(args) + ")",
			})
		}
	}

	// Operator-confirm bridge. RunAgentLoop calls this when a tool
	// with NeedsConfirm is about to fire; we render a confirm card
	// in the activity pane and block on confirmCh until the
	// operator answers via /api/agent/confirm.
	confirmFn := func(toolName, argsSummary string) bool {
		confirmID := nextID("c")
		demoSessions.mu.Lock()
		s.confirmCh = make(chan string, 1)
		ch := s.confirmCh
		demoSessions.mu.Unlock()

		sse.Send(map[string]any{
			"kind":   "confirm",
			"id":     confirmID,
			"prompt": "Run tool: " + toolName + "?",
			"detail": argsSummary,
			"actions": []map[string]string{
				{"label": "Allow", "value": "allow", "variant": "primary"},
				{"label": "Deny", "value": "deny", "variant": "danger"},
			},
		})

		var choice string
		select {
		case choice = <-ch:
		case <-r.Context().Done():
			choice = "deny"
		case <-time.After(120 * time.Second):
			choice = "deny"
		}
		demoSessions.mu.Lock()
		s.confirmCh = nil
		demoSessions.mu.Unlock()
		return choice == "allow"
	}

	systemPrompt := "You are a helpful assistant in a demo of the gohort " +
		"agent-loop framework. You have two tools: get_time (returns the " +
		"server's current time) and write_note (saves a short note to " +
		"the current conversation). Use them when relevant; otherwise " +
		"answer in plain markdown. Keep replies concise."

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	resp, _, err := T.RunAgentLoop(ctx, history, AgentLoopConfig{
		SystemPrompt: systemPrompt,
		Tools:        demoTools(s),
		MaxRounds:    6,
		Stream:       stream,
		OnStep:       onStep,
		Confirm:      confirmFn,
	})
	if err != nil {
		sse.Send(map[string]any{"kind": "error", "text": err.Error()})
		sse.Send(map[string]any{"kind": "done"})
		return
	}

	// Some backends return the full reply only via Stream; others
	// return it via resp.Content with no streaming. Reconcile.
	reply := strings.TrimSpace(fullReply.String())
	if reply == "" && resp != nil {
		reply = strings.TrimSpace(resp.Content)
		if reply != "" {
			sse.Send(map[string]any{
				"kind": "chunk_replace", "id": asstID, "text": reply,
			})
		}
	}
	sse.Send(map[string]any{"kind": "message_done", "id": asstID})

	s.Messages = append(s.Messages, demoMessage{
		ID: asstID, Role: "assistant", Content: reply,
	})

	sse.Send(map[string]any{"kind": "done"})
}

// handleAgentConfirm receives the operator's choice from the
// confirm card and unblocks the in-flight send.
func (T *HelloAgent) handleAgentConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Routed within the CALLER'S OWN sessions. The confirm card's id is
	// generated per prompt and does not name a session, so this picks one
	// rather than addressing it — which is only safe once the candidates are
	// limited to the person answering. A production app with several prompts
	// in flight at once should carry the session id in the confirm event and
	// address it directly.
	if !demoSessions.AnswerConfirm(user, body.Value) {
		http.Error(w, "no pending confirmation on any of your sessions", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentCancel — minimal: just ack. The send handler honors
// r.Context() cancellation; closing the SSE stream via the
// AbortController on the client side closes that context.
func (T *HelloAgent) handleAgentCancel(w http.ResponseWriter, r *http.Request) {
	// Requires a user even though it does nothing. An endpoint that skips the
	// check because it happens to be harmless today is one nobody re-examines
	// when it stops being harmless.
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
