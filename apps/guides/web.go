// Guides HTTP surface: the workbench page, the guide/section data endpoints, and
// the chat bridge (with the add_section / edit_section co-author tools injected
// into the bound Guide Author agent's run).
package guides

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// activeTable holds the per-user "which guide is open" marker, so the co-author
// tools know which document to write into.
const activeTable = "guides_active"

func (T *Guides) route(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	switch {
	case path == "":
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, "/guides/", http.StatusFound)
			return
		}
		T.servePage(w, r)
	case path == "guides":
		T.handleList(w, r, udb)
	case path == "guide":
		T.handleGuide(w, r, udb)
	case path == "new":
		T.handleNew(w, r, udb)
	case path == "chat/active":
		T.handleSetActive(w, r, udb)
	case path == "chat/send":
		T.handleChatSend(w, r, udb, user)
	case path == "chat/cancel":
		T.dispatchChat(w, r, "cancel", "")
	case path == "chat/sessions":
		T.dispatchChat(w, r, "sessions", "")
	case strings.HasPrefix(path, "chat/sessions/"):
		T.dispatchChat(w, r, "session-one", strings.TrimPrefix(path, "chat/sessions/"))
	default:
		http.NotFound(w, r)
	}
}

// --- data endpoints ----------------------------------------------------------

// handleList feeds the workbench's left list: [{id, title}].
func (T *Guides) handleList(w http.ResponseWriter, r *http.Request, udb Database) {
	type row struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	out := []row{}
	for _, g := range listGuides(udb) {
		out = append(out, row{ID: g.ID, Title: firstNonEmpty(g.Title, "Untitled guide")})
	}
	writeJSON(w, out)
}

// handleGuide GETs one guide rendered for the viewer ({id, title, html}) or
// DELETEs it.
func (T *Guides) handleGuide(w http.ResponseWriter, r *http.Request, udb Database) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	switch r.Method {
	case http.MethodGet:
		g, ok := loadGuide(udb, id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"id": g.ID, "title": g.Title, "html": renderGuideHTML(g)})
	case http.MethodDelete:
		deleteGuide(udb, id)
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleNew creates a guide from the workbench's New modal (a title field).
func (T *Guides) handleNew(w http.ResponseWriter, r *http.Request, udb Database) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	g := saveGuide(udb, Guide{
		ID:       newID(),
		Title:    firstNonEmpty(strings.TrimSpace(body.Title), "Untitled guide"),
		Subtitle: strings.TrimSpace(body.Subtitle),
	})
	writeJSON(w, map[string]string{"id": g.ID, "title": g.Title})
}

func (T *Guides) handleSetActive(w http.ResponseWriter, r *http.Request, udb Database) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	udb.Set(activeTable, "current", strings.TrimSpace(body.ID))
	w.WriteHeader(http.StatusNoContent)
}

func activeGuideID(udb Database) string {
	var id string
	udb.Get(activeTable, "current", &id)
	return id
}

// --- chat bridge -------------------------------------------------------------

// handleChatSend dispatches the chat to the bound Guide Author agent with the
// co-author tools injected, so add_section / edit_section write into this app's
// guide store (the same store the viewer renders).
func (T *Guides) handleChatSend(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	agent, ok := orch.LookupAppAgent(user, guideAgentID)
	if !ok {
		http.Error(w, "guide author agent unavailable", http.StatusServiceUnavailable)
		return
	}
	orch.PublicHandleSendWithAppTools(w, r, agent, T.coauthorTools(udb))
}

// dispatchChat forwards cancel / session routes to orchestrate's PublicHandle*.
func (T *Guides) dispatchChat(w http.ResponseWriter, r *http.Request, kind, sid string) {
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := orch.LookupAppAgent(user, guideAgentID)
	if !ok {
		http.Error(w, "guide author agent unavailable", http.StatusServiceUnavailable)
		return
	}
	switch kind {
	case "cancel":
		orch.PublicHandleCancel(w, r, agent)
	case "sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case "session-one":
		orch.PublicHandleSessionOne(w, r, agent.ID, sid)
	default:
		http.NotFound(w, r)
	}
}

// --- helpers -----------------------------------------------------------------

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

var _ = fmt.Sprint
