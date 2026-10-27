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
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/changes"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/textutil"
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

// sessionTitleChars is how much of the first message labels a session on
// the rail.
const sessionTitleChars = 80

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
	noteSessionsChanged(udb)
	return s
}

// noteSessionsChanged wakes the owner's open pages to re-read their session
// lists (the core/changes "sessions" topic), the Sessions rail among them.
func noteSessionsChanged(udb Database) {
	if user := UserOfDB(udb); user != "" {
		changes.Note(changes.ForUser("sessions", user))
	}
}

// sessionIDUsableBy reports whether user may run under session id: it is not
// live, and not holding a confirmation channel, for anybody else. An empty id
// is always usable (a fresh one is minted).
func sessionIDUsableBy(id, user string) bool {
	if id == "" {
		return true
	}
	if owner, live := probeSessions.OwnerOf(id); live && owner != user {
		return false
	}
	if v, ok := confirmChans.Load(id); ok {
		if p, ok := v.(pendingConfirm); ok && p.owner != user {
			return false
		}
	}
	return true
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
	s := saveSession(udb, applianceID, chatSession{ID: id, Name: textutil.FirstMessageTitle(firstMsg, sessionTitleChars)})
	return s.ID
}

// appendTurn adds one turn to a session and re-saves: the user's message,
// any notes they wrote mid-run that the run picked up, and the reply.
// No-op if the session has vanished (deleted mid-run).
func appendTurn(udb Database, applianceID, id, userMsg string, notes []string, assistantMsg string) {
	s, ok := loadSession(udb, applianceID, id)
	if !ok {
		return
	}
	if strings.TrimSpace(userMsg) != "" {
		s.Messages = append(s.Messages, sessionMsg{Role: "user", Content: userMsg})
	}
	for _, n := range notes {
		if strings.TrimSpace(n) != "" {
			s.Messages = append(s.Messages, sessionMsg{Role: "user", Content: n})
		}
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
	noteSessionsChanged(udb)
}

// handleServitorSessionList → GET /api/sessions?appliance_id=<id>.
// Returns [] (not an error) when appliance_id is blank so the rail's
// first fetch renders cleanly before a pick.
func (T *Servitor) handleServitorSessionList(c *jsonapi.Call) (any, error) {
	appID := strings.TrimSpace(c.R.URL.Query().Get("appliance_id"))
	if appID == "" {
		return []chatSession{}, nil
	}
	return listSessions(c.UDB, appID), nil
}

// handleServitorSessionOne → GET (load transcript) / DELETE one session
// at /api/sessions/{id}?appliance_id=<id>.
func (T *Servitor) handleServitorSessionOne() http.HandlerFunc {
	one := jsonapi.Methods{
		"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
			id, appID, err := sessionPath(c.R)
			if err != nil {
				return nil, err
			}
			s, found := loadSession(c.UDB, appID, id)
			if !found {
				return nil, jsonapi.NotFound("no such session")
			}
			// Decorated at read time rather than stored on the record: the guides an
			// investigation wrote are a relationship, and keeping a copy on the
			// session would go stale the moment one was renamed. The link table is
			// the source; this is a join.
			return struct {
				chatSession
				Guides []SessionGuide `json:"guides,omitempty"`
			}{chatSession: s, Guides: SessionGuides(c.UDB, id)}, nil
		}),
		"DELETE": jsonapi.Delete(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
			id, appID, err := sessionPath(c.R)
			if err != nil {
				return nil, err
			}
			deleteSession(c.UDB, appID, id)
			// The links go with the session, never with the guide: a guide is the
			// durable artifact and outlives the investigation that produced it.
			forgetSessionGuides(c.UDB, id)
			return jsonapi.Empty(http.StatusNoContent), nil
		}),
		// The chat's Edit and Delete on a message, the contract orchestrate's
		// sessions follow: {at: N} drops messages from index N on (Edit then
		// resends the changed text), {delete_at: N} drops just that one. The
		// next turn's history comes from the page, which drops the same rows.
		"PATCH": jsonapi.Patch(T.DB, func(c *jsonapi.Call, body sessionCutReq) (any, error) {
			id, appID, err := sessionPath(c.R)
			if err != nil {
				return nil, err
			}
			s, found := loadSession(c.UDB, appID, id)
			if !found {
				return nil, jsonapi.NotFound("no such session")
			}
			out := map[string]any{}
			if body.DeleteAt != nil {
				if di := *body.DeleteAt; di >= 0 && di < len(s.Messages) {
					s.Messages = append(s.Messages[:di], s.Messages[di+1:]...)
					s = saveSession(c.UDB, appID, s)
				}
				out["deleted_at"] = *body.DeleteAt
			} else {
				at := body.At
				if at < 0 {
					at = 0
				}
				if at > len(s.Messages) {
					at = len(s.Messages)
				}
				s.Messages = s.Messages[:at]
				s = saveSession(c.UDB, appID, s)
				out["at"] = at
			}
			out["messages_remaining"] = len(s.Messages)
			return out, nil
		}, jsonapi.MaxBody(1<<16)),
	}.Serve
	return func(w http.ResponseWriter, r *http.Request) {
		// /api/sessions/{id}/export — the "Copy session" action's source:
		// the transcript as markdown, not JSON.
		if strings.HasSuffix(r.URL.Path, "/export") {
			T.handleSessionExport(w, r)
			return
		}
		one(w, r)
	}
}

// sessionPath is the session id and appliance a /api/sessions/{id} request
// names: a 404 for one that names neither.
func sessionPath(r *http.Request) (id, appID string, err error) {
	id = strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	appID = strings.TrimSpace(r.URL.Query().Get("appliance_id"))
	if id == "" || strings.Contains(id, "/") || appID == "" {
		return "", "", jsonapi.NotFound("no such session")
	}
	return id, appID, nil
}

// sessionCutReq is what a session's PATCH is sent.
type sessionCutReq struct {
	At       int  `json:"at"`
	DeleteAt *int `json:"delete_at"`
}

// handleSessionExport answers /api/sessions/{id}/export with the transcript as
// markdown.
func (T *Servitor) handleSessionExport(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/export")
	appID := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
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
}
