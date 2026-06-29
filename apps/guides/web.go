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
		T.handleGuide(w, r, udb, user)
	case path == "new":
		T.handleNew(w, r, udb)
	case path == "revisions":
		T.handleRevisions(w, r, udb)
	case path == "restore":
		T.handleRestore(w, r, udb)
	case path == "export":
		T.handleExport(w, r, udb)
	case path == "audit":
		T.handleAudit(w, r, udb, user)
	case path == "section":
		T.handleSection(w, r, udb)
	case path == "section/move":
		T.handleSectionMove(w, r, udb)
	case path == "section/add":
		T.handleSectionAdd(w, r, udb)
	case path == "collections":
		T.handleCollections(w, r, udb, user)
	case path == "references":
		T.handleReferences(w, r, udb, user)
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
func (T *Guides) handleGuide(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	switch r.Method {
	case http.MethodGet:
		g, ok := loadGuide(udb, id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"id": g.ID, "title": g.Title, "html": renderGuideHTML(g, true)})
	case http.MethodDelete:
		deleteGuide(udb, user, id)
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
	g := saveGuideRev(udb, Guide{
		ID:       newID(),
		Title:    firstNonEmpty(strings.TrimSpace(body.Title), "Untitled guide"),
		Subtitle: strings.TrimSpace(body.Subtitle),
	}, "Created guide")
	writeJSON(w, map[string]string{"id": g.ID, "title": g.Title})
}

// handleRevisions lists a guide's revision history (newest first): {id, at, note}.
func (T *Guides) handleRevisions(w http.ResponseWriter, r *http.Request, udb Database) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	revs := listRevisions(udb, id)
	type row struct {
		ID   string `json:"id"`
		At   string `json:"at"`
		Note string `json:"note"`
	}
	out := []row{}
	for i := len(revs) - 1; i >= 0; i-- { // newest first
		out = append(out, row{ID: revs[i].ID, At: revs[i].At, Note: revs[i].Note})
	}
	writeJSON(w, out)
}

// handleRestore makes a revision's snapshot the current guide (recording the
// restore itself as a new revision, so it's undoable too).
func (T *Guides) handleRestore(w http.ResponseWriter, r *http.Request, udb Database) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	revID := strings.TrimSpace(r.URL.Query().Get("rev"))
	rev, ok := loadRevision(udb, id, revID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	g := rev.Guide
	g.ID = id // guard against a snapshot carrying a stale id
	saveGuideRev(udb, g, "Restored revision from "+rev.At)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleAudit checks whether a guide is still current: it dispatches the
// seed-research agent over the guide's content to find outdated/now-incorrect
// claims, important changes since it was written, and gaps to add — with sources.
// Returns the audit as a markdown report for the UI to render. The user reads it,
// then asks the Guide Author to apply the fixes. Synchronous (an agent loop).
func (T *Guides) handleAudit(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, ok := loadGuide(udb, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if len(g.Sections) == 0 {
		writeJSON(w, map[string]string{"report": "_This guide has no sections yet — nothing to audit._"})
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	prompt := "Audit the following guide for accuracy and CURRENCY as of today. Research the current state of what it covers and report:\n" +
		"1. **Outdated or now-incorrect** content — anything that has changed (renamed commands/flags, deprecated APIs, changed defaults, superseded versions).\n" +
		"2. **Notable changes / new developments** since it was written that a reader should know.\n" +
		"3. **Gaps** — important things it should cover but doesn't.\n\n" +
		"Be specific: name the SECTION and the exact change needed, and cite sources for any claim that something changed. If a section is still accurate, say so briefly. End with a short prioritized list of recommended edits. If everything is current, say that plainly.\n\n" +
		"GUIDE:\n\n" + renderGuideMarkdownPlain(g)
	report, err := orch.RunAgentSync(r.Context(), user, user, "seed-research", prompt)
	if err != nil {
		http.Error(w, "audit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"report": report})
}

// --- inline section editing (viewer controls) --------------------------------

// sectionIdx finds a section by id in the guide, returning its slice index (-1
// if absent).
func sectionIdx(g Guide, sid string) int {
	for i := range g.Sections {
		if g.Sections[i].ID == sid {
			return i
		}
	}
	return -1
}

// handleSection GET (raw fields for the edit form) / POST (save title+markdown) /
// DELETE (remove) one section. ?guide=&section=.
func (T *Guides) handleSection(w http.ResponseWriter, r *http.Request, udb Database) {
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	sid := strings.TrimSpace(r.URL.Query().Get("section"))
	g, ok := loadGuide(udb, gid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	idx := sectionIdx(g, sid)
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s := g.Sections[idx]
		writeJSON(w, map[string]string{"id": s.ID, "title": s.Title, "markdown": s.Markdown})
	case http.MethodPost:
		var body struct {
			Title    string `json:"title"`
			Markdown string `json:"markdown"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		g.Sections[idx].Title = strings.TrimSpace(body.Title)
		g.Sections[idx].Markdown = strings.TrimSpace(body.Markdown)
		saveGuideRev(udb, g, "Edited section: "+g.Sections[idx].Title)
		writeJSON(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		removed := g.Sections[idx].Title
		g.Sections = append(g.Sections[:idx], g.Sections[idx+1:]...)
		normalizeOrder(&g)
		saveGuideRev(udb, g, "Removed section: "+removed)
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSectionMove reorders a section one step up/down. POST ?guide=&section=&dir=up|down.
func (T *Guides) handleSectionMove(w http.ResponseWriter, r *http.Request, udb Database) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	sid := strings.TrimSpace(r.URL.Query().Get("section"))
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	g, ok := loadGuide(udb, gid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	secs := g.sorted()
	idx := -1
	for i := range secs {
		if secs[i].ID == sid {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	target := idx - 1
	if dir == "down" {
		target = idx + 1
	}
	reordered, _ := reorderSections(secs, idx, target)
	g.Sections = reordered
	saveGuideRev(udb, g, "Moved section: "+sectionHeading(secs[idx], idx))
	writeJSON(w, map[string]bool{"ok": true})
}

// handleSectionAdd appends a new section. POST ?guide= with {title, markdown}.
func (T *Guides) handleSectionAdd(w http.ResponseWriter, r *http.Request, udb Database) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	g, ok := loadGuide(udb, gid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	title := firstNonEmpty(strings.TrimSpace(body.Title), "New section")
	g.Sections = append(g.Sections, Section{ID: newID(), Title: title, Markdown: strings.TrimSpace(body.Markdown), Order: g.nextOrder()})
	saveGuideRev(udb, g, "Added section: "+title)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleCollections drives the per-guide knowledge picker. GET ?guide=<id>
// returns {available: [{id,name,description}], attached: [ids]} — the
// collections the user can attach (their own + deployment-scoped) plus the set
// already attached to this guide. POST ?guide=<id> with {collections:[ids]}
// stores the attachment on the guide. Collections live in the shared
// collections home (CollectionsDB()), so guides never reaches into orchestrate.
func (T *Guides) handleCollections(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	switch r.Method {
	case http.MethodGet:
		type item struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
		}
		available := []item{}
		for _, c := range ListCollections(UserDB(CollectionsDB(), user), user) {
			available = append(available, item{ID: c.ID, Name: c.Name, Description: c.Description})
		}
		attached := []string{}
		if g, ok := loadGuide(udb, gid); ok && g.Collections != nil {
			attached = g.Collections
		}
		writeJSON(w, map[string]any{"available": available, "attached": attached})
	case http.MethodPost:
		g, ok := loadGuide(udb, gid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Collections []string `json:"collections"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		g.Collections = body.Collections
		saveGuide(udb, g) // not a content change — no revision snapshot
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReferences drives the per-guide Sources picker. GET ?guide=<id> returns
// {groups: [ReferenceGroup], attached: [{kind,item_id}]} — every reference source
// available to the user (servitor Systems, connected document sources) plus the
// selections already attached to this guide. POST ?guide=<id> with
// {references:[{kind,item_id}]} stores the selection on the guide. The registry
// is per-user + access-gated, so the user only ever sees their own sources.
func (T *Guides) handleReferences(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	switch r.Method {
	case http.MethodGet:
		groups := ReferenceGroups(user)
		if groups == nil {
			groups = []ReferenceGroup{}
		}
		attached := []ReferenceSelection{}
		if g, ok := loadGuide(udb, gid); ok && g.References != nil {
			attached = g.References
		}
		writeJSON(w, map[string]any{"groups": groups, "attached": attached})
	case http.MethodPost:
		g, ok := loadGuide(udb, gid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var body struct {
			References []ReferenceSelection `json:"references"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		g.References = body.References
		saveGuide(udb, g) // attachment is not a content change — no revision snapshot
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	orch.PublicHandleSendWithAppTools(w, r, agent, T.coauthorTools(udb, orch, user))
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
