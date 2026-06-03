// Servitor chat sessions — the persisted conversation history that
// backs the left rail, mirroring how orchestrate persists ChatSessions.
// Replaces the old DocWorkspace rail: a session is just a titled,
// appliance-scoped transcript that auto-creates on the first message
// and accumulates a turn per completed run. No draft / synthesis /
// supplements — plain chat history like every other app.
//
// The session id doubles as the in-flight run id (see handleChat /
// runSession), so it's stable across turns and the AgentLoopPanel's
// session_id, EventsURL subscription, cancel, and deep-link all key off
// the same value.
package servitor

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// sessionTable scopes sessions per appliance — switching the appliance
// picker swaps the rail's contents, same as the old workspace rail.
func sessionTable(applianceID string) string {
	return "servitor_sessions:" + applianceID
}

// sessionMsg is one turn-half in a persisted transcript. Kept minimal
// (role + text) — images and tool detail aren't replayed into the pane.
type sessionMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatSession is one conversation thread for an appliance. The wire
// field names match the AgentLoopPanel's defaults set in chat_page.go
// (IDField "id", TitleField "name", DateField "updated", MessagesField
// "messages").
type chatSession struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Messages []sessionMsg `json:"messages,omitempty"`
	Created  string       `json:"created"`
	Updated  string       `json:"updated"`
}

// sessionTitle derives a short rail label from the first user message.
func sessionTitle(msg string) string {
	t := strings.TrimSpace(msg)
	if len(t) > 80 {
		t = t[:80] + "…"
	}
	if t == "" {
		t = "New session"
	}
	return t
}

func loadSession(udb Database, applianceID, id string) (chatSession, bool) {
	var s chatSession
	if udb == nil || applianceID == "" || id == "" {
		return s, false
	}
	ok := udb.Get(sessionTable(applianceID), id, &s)
	return s, ok
}

// saveSession upserts a session, stamping Created (first save) + Updated.
// Honors a caller-supplied ID so the run id and session id stay equal.
func saveSession(udb Database, applianceID string, s chatSession) chatSession {
	if udb == nil || applianceID == "" {
		return s
	}
	now := time.Now().Format(time.RFC3339)
	if s.ID == "" {
		s.ID = UUIDv4()
	}
	if s.Created == "" {
		s.Created = now
	}
	s.Updated = now
	udb.Set(sessionTable(applianceID), s.ID, s)
	return s
}

// ensureSession resolves the active session for a send: returns the
// existing id when one is supplied and found; mints a fresh titled
// session (honoring a supplied id) otherwise. The returned id is what
// handleChat uses as the run id and echoes back to the client.
func ensureSession(udb Database, applianceID, id, firstMsg string) string {
	if udb == nil || applianceID == "" {
		return id
	}
	if id != "" {
		if _, ok := loadSession(udb, applianceID, id); ok {
			return id
		}
	}
	s := saveSession(udb, applianceID, chatSession{ID: id, Name: sessionTitle(firstMsg)})
	return s.ID
}

// appendTurn adds one user/assistant pair to a session and re-saves.
// No-op if the session has vanished (deleted mid-run).
func appendTurn(udb Database, applianceID, id, userMsg, assistantMsg string) {
	s, ok := loadSession(udb, applianceID, id)
	if !ok {
		return
	}
	if strings.TrimSpace(userMsg) != "" {
		s.Messages = append(s.Messages, sessionMsg{Role: "user", Content: userMsg})
	}
	if strings.TrimSpace(assistantMsg) != "" {
		s.Messages = append(s.Messages, sessionMsg{Role: "assistant", Content: assistantMsg})
	}
	saveSession(udb, applianceID, s)
}

func listSessions(udb Database, applianceID string) []chatSession {
	if udb == nil || applianceID == "" {
		return nil
	}
	tbl := sessionTable(applianceID)
	var out []chatSession
	for _, k := range udb.Keys(tbl) {
		var s chatSession
		if !udb.Get(tbl, k, &s) {
			continue
		}
		// Strip messages from the list payload — the rail only needs
		// id / name / updated.
		out = append(out, chatSession{ID: s.ID, Name: s.Name, Created: s.Created, Updated: s.Updated})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

// sessionMarkdown renders a transcript as markdown for the "Copy
// session" export.
func sessionMarkdown(s chatSession) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(s.Name)
	b.WriteString("\n\n")
	for _, m := range s.Messages {
		who := "User"
		if m.Role == "assistant" {
			who = "Assistant"
		}
		b.WriteString("## ")
		b.WriteString(who)
		b.WriteString("\n\n")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}

func deleteSession(udb Database, applianceID, id string) {
	if udb == nil || applianceID == "" || id == "" {
		return
	}
	udb.Unset(sessionTable(applianceID), id)
}

// handleServitorSessionList → GET /api/sessions?appliance_id=<id>.
// Returns [] (not an error) when appliance_id is blank so the rail's
// first fetch renders cleanly before a pick.
func (T *Servitor) handleServitorSessionList(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	appID := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
	if appID == "" {
		_ = json.NewEncoder(w).Encode([]chatSession{})
		return
	}
	_ = json.NewEncoder(w).Encode(listSessions(udb, appID))
}

// handleServitorSessionOne → GET (load transcript) / DELETE one session
// at /api/sessions/{id}?appliance_id=<id>.
func (T *Servitor) handleServitorSessionOne(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	appID := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
	// /api/sessions/{id}/export — the "Copy session" action's source.
	// Returns the transcript as markdown.
	if strings.HasSuffix(id, "/export") {
		id = strings.TrimSuffix(id, "/export")
		if id == "" || strings.Contains(id, "/") || appID == "" {
			http.NotFound(w, r)
			return
		}
		s, found := loadSession(udb, appID, id)
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(sessionMarkdown(s)))
		return
	}
	if id == "" || strings.Contains(id, "/") || appID == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s, found := loadSession(udb, appID, id)
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	case http.MethodDelete:
		deleteSession(udb, appID, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
