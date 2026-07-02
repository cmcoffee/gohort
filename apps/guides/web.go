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
		T.handleList(w, r, udb, user)
	case path == "guide":
		T.handleGuide(w, r, udb, user)
	case path == "new":
		T.handleNew(w, r, udb, user)
	case path == "share":
		T.handleShare(w, r, udb, user)
	case path == "revisions":
		T.handleRevisions(w, r, udb, user)
	case path == "restore":
		T.handleRestore(w, r, udb, user)
	case path == "export":
		T.handleExport(w, r, udb, user)
	case path == "audit":
		T.handleAudit(w, r, udb, user)
	case path == "update-sources":
		T.handleUpdateFromSources(w, r, udb, user)
	case path == "section":
		T.handleSection(w, r, udb, user)
	case path == "section/move":
		T.handleSectionMove(w, r, udb, user)
	case path == "section/add":
		T.handleSectionAdd(w, r, udb, user)
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

// resolve resolves a guide for the request (own store first, else the shared
// index → owner's store) and reports the requester's rights: canManage (share /
// delete / manage knowledge: owner or admin) and canEdit (change content: a
// manager, OR anyone when the guide is shared for edit). ownerUDB is the store all
// content ops must use. When found is false the handler should 404.
func (T *Guides) resolve(r *http.Request, reqUDB Database, user, id string) (g Guide, ownerUDB Database, canManage, canEdit, found bool) {
	g, owner, oudb, ok := resolveGuide(T.DB, reqUDB, user, id)
	if !ok {
		return Guide{}, nil, false, false, false
	}
	canManage = CanManageShared(user, owner, RequestIsAdmin(r))
	return g, oudb, canManage, canManage || g.sharedForEdit(), true
}

// handleList feeds the workbench's left list: [{id, title, shared, own}]. It
// unions the user's own guides with guides others have shared read-only.
func (T *Guides) handleList(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	type row struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Shared bool   `json:"shared,omitempty"` // shared by SOMEONE ELSE (read-only to this user)
		Own    bool   `json:"own"`
	}
	out := []row{}
	seen := map[string]bool{}
	for _, g := range listGuides(udb) {
		seen[g.ID] = true
		out = append(out, row{ID: g.ID, Title: firstNonEmpty(g.Title, "Untitled guide"), Own: true})
	}
	// Guides shared by other users: resolve each from its owner's store, skipping
	// any the user already owns.
	for id, owner := range ListSharedOwners(T.DB, sharedGuidesIndex) {
		if seen[id] || owner == user {
			continue
		}
		if oudb := UserDB(T.DB, owner); oudb != nil {
			if g, ok := loadGuide(oudb, id); ok {
				suffix := " · shared"
				if g.sharedForEdit() {
					suffix = " · shared (editable)"
				}
				out = append(out, row{ID: g.ID, Title: firstNonEmpty(g.Title, "Untitled guide") + suffix, Shared: true})
			}
		}
	}
	writeJSON(w, out)
}

// handleGuide GETs one guide rendered for the viewer ({id, title, html, own,
// shared}) or DELETEs it. Shared guides resolve to the owner's store; the inline
// edit controls only render for a user who can manage the guide.
func (T *Guides) handleGuide(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, ownerUDB, canManage, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"id": g.ID, "title": g.Title, "html": renderGuideHTML(g, canEdit),
			"own": canManage, "can_edit": canEdit, "shared": g.Shared,
		})
	case http.MethodDelete:
		if !canManage {
			http.Error(w, "only the owner can delete this guide", http.StatusForbidden)
			return
		}
		deleteGuide(ownerUDB, user, id)
		SetSharedOwner(T.DB, sharedGuidesIndex, id, "", false) // drop from the shared index
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleNew creates a guide from the workbench's New modal (a title field). The
// creator is stamped as Owner; new guides are private until shared.
func (T *Guides) handleNew(w http.ResponseWriter, r *http.Request, udb Database, user string) {
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
		Owner:    user,
	}, "Created guide")
	writeJSON(w, map[string]string{"id": g.ID, "title": g.Title})
}

// handleShare drives the per-guide Share control. GET ?id=<id> returns
// {shared, can_manage, owner} — the current sharing state plus whether this user
// may change it. POST ?id=<id> with {shared:bool} publishes/unpublishes the guide
// read-only to all authenticated users (owner/admin only). Sharing keeps the guide
// in the owner's store; it just adds/removes the app-wide index entry.
func (T *Guides) handleShare(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, ownerUDB, canManage, _, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"shared": g.Shared, "mode": shareModeOf(g), "can_manage": canManage, "owner": g.Owner})
	case http.MethodPost:
		if !canManage {
			http.Error(w, "only the owner can change sharing for this guide", http.StatusForbidden)
			return
		}
		var body struct {
			Shared bool   `json:"shared"`
			Mode   string `json:"mode"` // "view" | "edit" (defaults to view)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		g.Shared = body.Shared
		g.ShareMode = normalizeShareMode(body.Mode)
		saveGuide(ownerUDB, g) // sharing flag is not a content change — no revision snapshot
		owner := g.Owner
		if owner == "" {
			owner = user
		}
		SetSharedOwner(T.DB, sharedGuidesIndex, id, owner, body.Shared)
		writeJSON(w, map[string]any{"ok": true, "shared": body.Shared, "mode": normalizeShareMode(body.Mode)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// normalizeShareMode maps an arbitrary mode string to a stored ShareMode: "edit"
// stays "edit", anything else is view-only ("").
func normalizeShareMode(mode string) string {
	if strings.TrimSpace(mode) == ShareModeEdit {
		return ShareModeEdit
	}
	return ""
}

// shareModeOf reports a guide's share mode for the UI: "edit" or "view".
func shareModeOf(g Guide) string {
	if g.ShareMode == ShareModeEdit {
		return ShareModeEdit
	}
	return "view"
}

// handleRevisions lists a guide's revision history (newest first): {id, at, note}.
func (T *Guides) handleRevisions(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	_, ownerUDB, _, _, found := T.resolve(r, udb, user, id)
	if !found {
		writeJSON(w, []any{})
		return
	}
	revs := listRevisions(ownerUDB, id)
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
// restore itself as a new revision, so it's undoable too). Owner/admin only.
func (T *Guides) handleRestore(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	cur, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
		return
	}
	revID := strings.TrimSpace(r.URL.Query().Get("rev"))
	rev, ok := loadRevision(ownerUDB, id, revID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	g := rev.Guide
	g.ID = id           // guard against a snapshot carrying a stale id
	g.Owner = cur.Owner // preserve ownership + sharing across a restore
	g.Shared = cur.Shared
	g.ShareMode = cur.ShareMode
	saveGuideRev(ownerUDB, g, "Restored revision from "+rev.At)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleAudit checks whether a guide is still current + still reflects its
// LINKED SOURCES. It gathers a snapshot of the guide's attached collections and
// reference sources (owner-scoped), then dispatches seed-research to compare the
// written guide against that snapshot (contradictions, material the sources now
// carry that the guide is missing) AND against the live web (general currency).
// Returns a markdown report naming the section + exact edit for each finding; the
// owner reads it and asks the Guide Author to apply the fixes (revisions preserve
// the prior version). The DOCUMENT is never auto-mutated — that's the whole point
// of a review-and-apply refresh over a blind regenerate. Owner/editor only, since
// they're the ones who can act on the report. Synchronous (an agent loop).
func (T *Guides) handleAudit(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, owner, _, ok := resolveGuide(T.DB, udb, user, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !(CanManageShared(user, owner, RequestIsAdmin(r)) || g.sharedForEdit()) {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
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
	// A bounded snapshot of the guide's CURRENT linked sources (owner-scoped), so
	// the audit can flag where the written guide has fallen behind its sources.
	snapshot := gatherLinkedSourceSnapshot(r.Context(), owner, g)
	var prompt string
	if snapshot != "" {
		prompt = "Audit the following guide for accuracy and CURRENCY as of today. It has LINKED SOURCES — the user's own knowledge collections and connected reference sources — and a current snapshot of that material is included below. Your MOST IMPORTANT job is to compare the written guide against these linked sources. Report:\n" +
			"1. **Contradicted / outdated by the sources** — sections whose claims the linked sources now contradict or supersede. Name the section + the exact change, quoting the source.\n" +
			"2. **Missing from the guide** — material present in the linked sources that the guide should cover but doesn't. Name the section it belongs in (or that a new section is needed).\n" +
			"3. **Web currency** — anything you can verify by web research that changed since the guide was written (renamed commands/flags, deprecations, superseded versions), with sources.\n" +
			"4. **Gaps** — other important things the guide should cover.\n\n" +
			"Be specific: name the SECTION and the exact edit needed. If a section still matches its sources and is current, say so briefly. End with a short prioritized list of recommended edits. If the guide fully reflects its sources and is current, say that plainly.\n\n" +
			"=== CURRENT LINKED SOURCES ===\n\n" + snapshot + "\n\n=== GUIDE ===\n\n" + renderGuideMarkdownPlain(g)
	} else {
		prompt = "Audit the following guide for accuracy and CURRENCY as of today. Research the current state of what it covers and report:\n" +
			"1. **Outdated or now-incorrect** content — anything that has changed (renamed commands/flags, deprecated APIs, changed defaults, superseded versions).\n" +
			"2. **Notable changes / new developments** since it was written that a reader should know.\n" +
			"3. **Gaps** — important things it should cover but doesn't.\n\n" +
			"Be specific: name the SECTION and the exact change needed, and cite sources for any claim that something changed. If a section is still accurate, say so briefly. End with a short prioritized list of recommended edits. If everything is current, say that plainly.\n\n" +
			"GUIDE:\n\n" + renderGuideMarkdownPlain(g)
	}
	report, err := orch.RunAgentSync(r.Context(), user, user, "seed-research", prompt)
	if err != nil {
		http.Error(w, "audit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"report": report})
}

// handleUpdateFromSources runs the Guide Author over the guide to revise its
// sections against the CURRENT linked sources (collections + reference sources),
// applying edits as revisions. Owner/editor only — it changes the document. The
// DOCUMENT is never touched for a guide with no linked sources (nothing to update
// from) or no sections. Returns a markdown summary of what changed.
func (T *Guides) handleUpdateFromSources(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, owner, _, ok := resolveGuide(T.DB, udb, user, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !(CanManageShared(user, owner, RequestIsAdmin(r)) || g.sharedForEdit()) {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
		return
	}
	if len(g.Sections) == 0 {
		writeJSON(w, map[string]string{"report": "_This guide has no sections yet — add some before updating from sources._"})
		return
	}
	if len(g.Collections) == 0 && len(g.References) == 0 {
		writeJSON(w, map[string]string{"report": "_This guide has no linked knowledge collections or reference sources. Attach some with the Knowledge / Sources buttons first, then update._"})
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	report, err := T.runUpdateFromSources(r.Context(), udb, orch, user, id)
	if err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(report) == "" {
		report = "Update complete."
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
func (T *Guides) handleSection(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	sid := strings.TrimSpace(r.URL.Query().Get("section"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, gid)
	if !found {
		http.NotFound(w, r)
		return
	}
	idx := sectionIdx(g, sid)
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && !canEdit {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
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
		saveGuideRev(ownerUDB, g, "Edited section: "+g.Sections[idx].Title)
		writeJSON(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		removed := g.Sections[idx].Title
		g.Sections = append(g.Sections[:idx], g.Sections[idx+1:]...)
		normalizeOrder(&g)
		saveGuideRev(ownerUDB, g, "Removed section: "+removed)
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSectionMove reorders a section one step up/down. POST ?guide=&section=&dir=up|down.
func (T *Guides) handleSectionMove(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	sid := strings.TrimSpace(r.URL.Query().Get("section"))
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, gid)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
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
	saveGuideRev(ownerUDB, g, "Moved section: "+sectionHeading(secs[idx], idx))
	writeJSON(w, map[string]bool{"ok": true})
}

// handleSectionAdd appends a new section. POST ?guide= with {title, markdown}.
func (T *Guides) handleSectionAdd(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gid := strings.TrimSpace(r.URL.Query().Get("guide"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, gid)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
		return
	}
	var body struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	title := firstNonEmpty(strings.TrimSpace(body.Title), "New section")
	g.Sections = append(g.Sections, Section{ID: newID(), Title: title, Markdown: strings.TrimSpace(body.Markdown), Order: g.nextOrder()})
	saveGuideRev(ownerUDB, g, "Added section: "+title)
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
		if g, _, _, _, ok := T.resolve(r, udb, user, gid); ok && g.Collections != nil {
			attached = g.Collections
		}
		writeJSON(w, map[string]any{"available": available, "attached": attached})
	case http.MethodPost:
		g, ownerUDB, canManage, _, found := T.resolve(r, udb, user, gid)
		if !found {
			http.NotFound(w, r)
			return
		}
		if !canManage {
			http.Error(w, "only the owner can change this guide's knowledge", http.StatusForbidden)
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
		saveGuide(ownerUDB, g) // not a content change — no revision snapshot
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
		if g, _, _, _, ok := T.resolve(r, udb, user, gid); ok && g.References != nil {
			attached = g.References
		}
		writeJSON(w, map[string]any{"groups": groups, "attached": attached})
	case http.MethodPost:
		g, ownerUDB, canManage, _, found := T.resolve(r, udb, user, gid)
		if !found {
			http.NotFound(w, r)
			return
		}
		if !canManage {
			http.Error(w, "only the owner can change this guide's sources", http.StatusForbidden)
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
		saveGuide(ownerUDB, g) // attachment is not a content change — no revision snapshot
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
	// Tool access by role on the OPEN guide:
	//   - editor (owner/admin, or a guide shared for edit): the full co-author
	//     kit — it can mutate the document.
	//   - reader (view-only shared guide): a READ-ONLY subset so they can ASK
	//     questions answered from the guide's linked knowledge. search_knowledge
	//     resolves to the OWNER's collections (via the resolve seam in the tools),
	//     so the Guide Author can reach knowledge the reader cannot open directly
	//     — but it can't change someone else's document.
	// Both cases share one tool builder (the closures resolve the open guide to
	// its owner's store); we just hand the reader a filtered slice.
	var tools []AgentToolDef
	if _, _, _, canEdit, found := T.resolve(r, udb, user, activeGuideID(udb)); found {
		all := T.coauthorTools(udb, orch, user, canEdit)
		if canEdit {
			tools = all
		} else {
			tools = readOnlyGuideTools(all)
		}
	}
	orch.PublicHandleSendWithAppTools(w, r, agent, tools)
}

// readOnlyGuideToolNames are the co-author tools safe to hand a READER of a
// view-only shared guide: they only READ the guide's own linked knowledge —
// attached collections (search_knowledge) and attached reference sources
// (list_reference_sources / pull_reference) — all resolved to the guide's OWNER
// through the tool closures (canEdit=false). This is the "the guide agent can
// reach the linked knowledge, the reader can't reach it directly" behavior: the
// reader asks, the agent answers from the owner's linked sources, but every
// section-editing tool and the web-researching `research` tool (which mutates
// the owner's corpus) is withheld.
var readOnlyGuideToolNames = map[string]bool{
	"list_sections":          true,
	"search_knowledge":       true,
	"list_reference_sources": true,
	"pull_reference":         true,
}

// readOnlyGuideTools filters a full co-author tool set down to the reader-safe
// subset. Keeps the shared tool builder as the single source of truth for how
// each tool resolves the open guide.
func readOnlyGuideTools(all []AgentToolDef) []AgentToolDef {
	out := make([]AgentToolDef, 0, len(readOnlyGuideToolNames))
	for _, t := range all {
		if readOnlyGuideToolNames[t.Tool.Name] {
			out = append(out, t)
		}
	}
	return out
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
