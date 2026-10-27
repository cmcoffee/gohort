// HTTP surface for declarative pipelines (core.PipelineDef). Mirrors
// the collections routes: per-user CRUD plus the portable-recipe
// export/import the artifact story calls for. The LLM authors pipelines
// through the `pipeline` grouped tool; these routes are the BROWSER
// surface — the agent-editor attach picker (GET list), the PipelinePanel
// (list / view / run / export / import), and a downloadable recipe.
//
//	GET    /api/pipelines              → {pipelines: [{id, name, description, stages}]}
//	POST   /api/pipelines              → create (body: {name, description, stages})
//	POST   /api/pipelines/import       → import a recipe (body: exported PipelineDef) → saved def
//	GET    /api/pipelines/{id}         → full def
//	PUT    /api/pipelines/{id}         → replace the def's fields/stages
//	DELETE /api/pipelines/{id}         → delete
//	GET    /api/pipelines/{id}/graph   → SVG of the stages and their arrows; ?links=1 anchors each box
//	GET    /api/pipelines/{id}/block?name=map|plan|cost|checklist → one derived block of the editor page, as HTML
//	GET    /api/pipelines/{id}/export  → portable recipe (id/owner/timestamps stripped)
//	POST   /api/pipelines/{id}/run     → run on body {input}; returns {output} (sync)
//	POST   /api/pipelines/{id}/stream          → run, streaming the transcript (SSE)
//	GET    /api/pipelines/{id}/sessions        → past runs, newest first
//	GET    /api/pipelines/{id}/sessions/{sid}  → one run's stored blocks
//	DELETE /api/pipelines/{id}/sessions/{sid}  → drop a run

package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"strconv"
)

// pipelineRow is the trimmed list shape — enough for the attach picker
// (id + name + description) and the panel's list view (stage count),
// without shipping every stage's prompt on a list call.
type pipelineRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Stages      int    `json:"stages"`
	// The rest is for the Extensions table, computed here rather than in
	// the page so a row says the same thing wherever it is shown. Same
	// reasoning as machineRow: "used by nothing" is the single most
	// useful fact about a pipeline, because an unattached one is a tool
	// no agent can call.
	StageNames []string `json:"stage_names,omitempty"`
	UsedBy     []string `json:"used_by,omitempty"`
	UsedByText string   `json:"used_by_text,omitempty"`
	Status     string   `json:"status,omitempty"`
	EditURL    string   `json:"edit_url,omitempty"`
	// Whose it is. Mine is true for the user's own; Owner names the sharer on
	// a row somebody else owns, so one table carries both without a reader
	// having to work out why a row has no Edit button.
	Mine       bool   `json:"mine"`
	Owner      string `json:"owner,omitempty"`
	SharedWith string `json:"shared_with,omitempty"`
}

// pipelineStatusText summarises what a pipeline is made of, and what is
// worth a look in it. Validate refuses anything unrunnable at every
// door, so a stored pipeline has no problems to report — only advice.
func pipelineStatusText(d PipelineDef) string {
	kinds := map[PipelineStageKind]int{}
	for _, s := range d.Stages {
		k := s.Kind
		if strings.TrimSpace(string(k)) == "" {
			k = StageWorker
		}
		kinds[k]++
	}
	var parts []string
	for _, k := range []PipelineStageKind{StageWorker, StageAgent, StageFanout, StageLoop, StageBranch, StageTool} {
		if n := kinds[k]; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+string(k))
		}
	}
	out := strings.Join(parts, ", ")
	// Advice alone, not the whole checklist: this is a per-ROW summary in a
	// list, and the tool-name half walks the agent table. The row says how
	// much the pipeline says about itself; opening it runs the full check.
	if n := len(d.Advice()); n > 0 {
		if out != "" {
			out += " - "
		}
		out += strconv.Itoa(n) + " worth a look"
	}
	return out
}

// pipelineStageNames lists the stages in order, for the row and the page.
func pipelineStageNames(d PipelineDef) []string {
	out := make([]string, 0, len(d.Stages))
	for _, s := range d.Stages {
		if n := strings.TrimSpace(s.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// pipelinesList and pipelineCreate serve the collection-level routes: GET
// list, POST create.
func (T *OrchestrateApp) pipelinesList(c *jsonapi.Call) (any, error) {
	user, udb := c.User, c.UDB
	defs := ListPipelineDefs(udb, user)
	// Which agents can call what. A pipeline attaches to an agent as
	// a tool (run_<name>), so an unattached one is inert in exactly
	// the way an unattached machine is.
	users := map[string][]string{}
	for _, ag := range listAgents(udb, user) {
		for _, pid := range ag.AttachedPipelines {
			users[pid] = append(users[pid], chFirst(ag.Name, ag.ID))
		}
	}
	rows := make([]pipelineRow, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, pipelineRow{
			ID: d.ID, Name: d.Name, Description: d.Description, Stages: len(d.Stages),
			StageNames: pipelineStageNames(d),
			UsedBy:     users[d.ID],
			UsedByText: usedByText(users[d.ID]),
			Status:     pipelineStatusText(d),
			EditURL:    "/orchestrate/pipeline?id=" + d.ID,
			Mine:       true,
			SharedWith: ShareRecipientsText(d.AllowedUsers),
		})
	}
	// Pipelines other people shared, after the user's own and labelled with
	// whose they are. Listed at all because a share nobody can find is a
	// share that did not happen; listed LAST because your own work is what
	// you came to this page for.
	for _, sp := range sharedPipelinesFor(user) {
		rows = append(rows, pipelineRow{
			ID: sp.Def.ID, Name: sp.Def.Name, Description: sp.Def.Description, Stages: len(sp.Def.Stages),
			StageNames: pipelineStageNames(sp.Def),
			Status:     "shared by " + sp.Owner,
			EditURL:    "/orchestrate/pipeline?id=" + sp.Def.ID,
			Owner:      sp.Owner,
		})
	}
	return map[string]any{"pipelines": rows}, nil
}

// pipelineCreate saves a new pipeline, the caller's own.
func (T *OrchestrateApp) pipelineCreate(c *jsonapi.Call, def PipelineDef) (any, error) {
	user, udb := c.User, c.UDB
	// Force a fresh, user-owned record — the client doesn't get to
	// assert id/owner on create.
	def.ID = ""
	def.Owner = user
	// Sharing is its own door (/share). A create that could assert a
	// recipient list would let one POST make a pipeline and hand it out
	// in the same breath, which is not a thing anybody asked to do.
	def.AllowedUsers = nil
	def.Editors = nil
	def.Published = false
	if err := def.Validate(); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	saved := SavePipelineDef(udb, def)
	Log("[orchestrate.pipelines] user=%q created pipeline %q (id=%s, %d stages)", user, saved.Name, saved.ID, len(saved.Stages))
	return saved, nil
}

// handlePipelineImport accepts a portable recipe (from /export or an
// uploaded JSON file) and saves it as a new user-owned pipeline.
// Registered as a more-specific route than /api/pipelines/ so it wins
// the ServeMux longest-prefix match.
func (T *OrchestrateApp) handlePipelineImport(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, ok := readImportBody(w, r)
	if !ok {
		return
	}
	if importBundleAtDoor(w, user, body, "pipeline", func(name string) (any, bool) {
		return findPipelineByNameOrID(udb, user, name)
	}) {
		return
	}
	// The page's Import form posts the file as TEXT under its field name,
	// {"recipe": "<file>"}; a script posts the recipe itself. Decoding the
	// wrapper as a PipelineDef read every UI import as an empty pipeline.
	var recipe PipelineDef
	if err := json.Unmarshal(UnwrapArtifactUpload(body), &recipe); err != nil {
		http.Error(w, "that does not read as a pipeline recipe ("+err.Error()+")", http.StatusBadRequest)
		return
	}
	saved, err := ImportPipeline(udb, user, recipe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[orchestrate.pipelines] user=%q imported pipeline %q (id=%s)", user, saved.Name, saved.ID)
	jsonapi.Write(w, saved)
}

// registerPipelineRoutes mounts one pipeline's routes: the record, its
// actions, and the run surfaces.
func (T *OrchestrateApp) registerPipelineRoutes() {
	T.HandleFunc("/api/pipelines/{id}", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.pipelineGet),
		"PUT":    jsonapi.Put(T.DB, T.pipelineSave),
		"DELETE": jsonapi.Delete(T.DB, T.pipelineDelete),
	}.Serve)
	T.HandleFunc("/api/pipelines/{id}/agents", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.pipelineAgents),
		"POST": jsonapi.Post(T.DB, T.pipelineAgentsSave, jsonapi.MaxBody(maxImportBytes)),
		"PUT":  jsonapi.Put(T.DB, T.pipelineAgentsSave, jsonapi.MaxBody(maxImportBytes)),
	}.Serve)
	T.HandleFunc("/api/pipelines/{id}/duplicate", jsonapi.Post(T.DB, T.pipelineDuplicate))
	T.HandleFunc("/api/pipelines/{id}/revise", jsonapi.Post(T.DB, T.pipelineRevise))
	T.HandleFunc("/api/pipelines/{id}/undo", jsonapi.Post(T.DB, T.pipelineUndo))
	T.HandleFunc("/api/pipelines/{id}/stages", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.pipelineStage),
		"POST":   jsonapi.Post(T.DB, T.pipelineStageSave),
		"PUT":    jsonapi.Put(T.DB, T.pipelineStageSave),
		"PATCH":  jsonapi.Patch(T.DB, T.pipelineStageSave),
		"DELETE": jsonapi.Delete(T.DB, T.pipelineStageDelete),
	}.Serve)
	T.HandleFunc("/api/pipelines/{id}/share", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.pipelineShare),
		"POST": jsonapi.Post(T.DB, T.pipelineShareSave),
	}.Serve)
	T.HandleFunc("/api/pipelines/{id}/publish", jsonapi.Post(T.DB, T.pipelinePublish))
	T.HandleFunc("/api/pipelines/{id}/unpublish", jsonapi.Post(T.DB, T.pipelineUnpublish))
	T.HandleFunc("/api/pipelines/{id}/run", jsonapi.Post(T.DB, T.pipelineRun))
	// Not JSON: an editor page block, the picture, a download, and the run
	// surfaces core serves (core/pipeline_runs.go).
	T.HandleFunc("/api/pipelines/{id}/block", T.pipelineRaw(T.handlePipelineBlock))
	T.HandleFunc("/api/pipelines/{id}/graph", T.pipelineRaw(T.handlePipelineGraph))
	T.HandleFunc("/api/pipelines/{id}/export", T.pipelineRaw(T.handlePipelineExport))
	revisions := T.pipelineRaw(func(w http.ResponseWriter, r *http.Request, p *pipelineCall) {
		T.handlePipelineRevisions(w, r, p.call.User, p.def, p.owner, p.ed.ok, r.PathValue("action"))
	})
	T.HandleFunc("/api/pipelines/{id}/revisions", revisions)
	T.HandleFunc("/api/pipelines/{id}/revisions/{action}", revisions)
	// The streaming twin of "run": same execution, transcript instead of a
	// lump. This is what lets a PipelineDef be an APP — core serves the
	// protocol (core/pipeline_runs.go); orchestrate supplies the store and
	// the agent-dispatch hook.
	runs := func(sub func(r *http.Request) string) http.HandlerFunc {
		return T.pipelineRaw(func(w http.ResponseWriter, r *http.Request, p *pipelineCall) {
			T.handlePipelineRuns(w, r, p.call.User, p.def, sub(r))
		})
	}
	for _, name := range []string{"stream", "sessions", "cancel", "followups", "suggest"} {
		T.HandleFunc("/api/pipelines/{id}/"+name, runs(func(*http.Request) string { return name }))
	}
	T.HandleFunc("/api/pipelines/{id}/sessions/{sid}", runs(func(r *http.Request) string { return "sessions/" + r.PathValue("sid") }))
	T.HandleFunc("/api/pipelines/{id}/reconnect/{rid}", runs(func(r *http.Request) string { return "reconnect/" + r.PathValue("rid") }))
	// followup/<name>/<run>: put a finished run through a follow-up.
	T.HandleFunc("/api/pipelines/{id}/followup/{name}/{run}", runs(func(r *http.Request) string {
		return "followup/" + r.PathValue("name") + "/" + r.PathValue("run")
	}))
}

// pipelineCall is one request on one pipeline: the definition, whose it is,
// whether it is the caller's, and where an edit to it goes (recipeEdit).
type pipelineCall struct {
	call  *jsonapi.Call
	def   PipelineDef
	owner string
	mine  bool
	ed    recipeEdit
}

// pipelineAt resolves the pipeline the path names: the caller's own, then one
// somebody shared with them (pipeline_sharing.go). A recipient reads, runs
// and copies; the definition stays the owner's, and an editor's change goes
// to the owner's copy.
func (T *OrchestrateApp) pipelineAt(c *jsonapi.Call) (*pipelineCall, error) {
	def, owner, mine, found := resolvePipelineFor(c.User, c.UDB, c.R.PathValue("id"))
	if !found {
		return nil, errNotFound
	}
	return &pipelineCall{call: c, def: def, owner: owner, mine: mine,
		ed: recipeEditFor(c.User, c.UDB, owner, mine, def.Editors)}, nil
}

// editable refuses a change by somebody it was shared with to run.
func (p *pipelineCall) editable(verb string) error {
	return recipeEditRefusal(p.ed, "pipeline", p.owner, verb)
}

// ownerOnly refuses what stays the owner's: sharing, publishing, deleting
// and attaching to agents.
//
// A share hands over the recipe, not the pen. The refusal names the owner
// because the alternative — a flat 403 on a page that plainly shows the thing —
// reads as a bug, and the reader's actual next step is to go ask that person.
func (p *pipelineCall) ownerOnly(verb string) error {
	if p.mine {
		return nil
	}
	return jsonapi.Forbidden("this pipeline is shared with you by " + p.owner + ", so you can run and copy it but not " + verb + " it: ask " + p.owner + " to make the change, or duplicate it and edit your copy")
}

// pipelineRaw is a raw route on one pipeline, refused as the JSON ones are.
func (T *OrchestrateApp) pipelineRaw(h func(w http.ResponseWriter, r *http.Request, p *pipelineCall)) http.HandlerFunc {
	return T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, udb Database) {
		p, err := T.pipelineAt(&jsonapi.Call{W: w, R: r, User: user, UDB: udb})
		if err != nil {
			jsonapi.Fail(w, r, err)
			return
		}
		h(w, r, p)
	})
}

func (T *OrchestrateApp) pipelineGet(c *jsonapi.Call) (any, error) {
	p, err := T.pipelineAt(c)
	if err != nil {
		return nil, err
	}
	return p.def, nil
}

// pipelineSave is the editor's whole-record save. Identity stays the
// server's: id, owner, who it is shared with and whether it is published.
func (T *OrchestrateApp) pipelineSave(c *jsonapi.Call, body PipelineDef) (any, error) {
	p, err := T.pipelineAt(c)
	if err != nil {
		return nil, err
	}
	if err := p.editable("edit"); err != nil {
		return nil, err
	}
	user, def, ed := c.User, p.def, p.ed
	// Identity stays the server's: keep id/owner from the loaded
	// record, take name/description/stages from the body. The
	// recipient list is identity too — an edit form must not be able
	// to widen or clear who a pipeline is shared with, the same way
	// an agent's guardrails survive a whole-record save.
	body.ID = def.ID
	body.Owner = ed.owner
	body.AllowedUsers = def.AllowedUsers
	body.Editors = def.Editors
	// Published is an administrator's approval, reached only through
	// the promotion queue; an edit form must not be able to assert it.
	body.Published = def.Published
	body.Created = def.Created
	if err := body.Validate(); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	saved := SavePipelineDef(ed.db, body)
	Log("[orchestrate.pipelines] user=%q updated pipeline %q (id=%s, owner %q)", user, saved.Name, saved.ID, ed.owner)
	return saved, nil
}

func (T *OrchestrateApp) pipelineDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	p, err := T.pipelineAt(c)
	if err != nil {
		return nil, err
	}
	if err := p.ownerOnly("delete"); err != nil {
		return nil, err
	}
	DeletePipelineDef(c.UDB, p.def.ID)
	Log("[orchestrate.pipelines] user=%q deleted pipeline %q (id=%s)", c.User, p.def.Name, p.def.ID)
	return map[string]any{"deleted": p.def.ID}, nil
}

// pipelineDuplicate copies the pipeline into the caller's own store.
// Iterating on a working pipeline in place is how the working one stops
// working. The copy is where an experiment belongs, and landing in it is the
// reason anybody clicked.
func (T *OrchestrateApp) pipelineDuplicate(c *jsonapi.Call, _ struct{}) (any, error) {
	p, err := T.pipelineAt(c)
	if err != nil {
		return nil, err
	}
	user, udb, def := c.User, c.UDB, p.def
	dup := def
	dup.ID = ""
	dup.Previous = nil
	dup.Global = false
	// A copy of somebody else's pipeline is YOURS: re-owned, and shared
	// with nobody until you say so. Carrying their recipient list into
	// your store would re-share their work under your name.
	dup.Owner = user
	dup.AllowedUsers = nil
	dup.Editors = nil
	// And not published either. An administrator agreed that THAT record
	// should reach everybody, not every copy anyone makes of it.
	dup.Published = false
	dup.Name = copyName(def.Name, pipelineNames(ListPipelineDefs(udb, user)))
	saved := SavePipelineDef(udb, dup)
	Log("[orchestrate.pipelines] user=%q duplicated pipeline %q as %q", user, def.Name, saved.Name)
	return map[string]any{"id": saved.ID, "name": saved.Name}, nil
}

// pipelineShareOf is the share door's list of who can see the pipeline: the
// recipient list, and ONLY the recipient list. The ACLPicker posts
// the whole record back (record mode), and this reads one field out of
// it — so a picker save cannot rewrite a stage, and the ordinary edit
// path cannot rewrite the ACL. Each door opens one thing, which is the
// same split an agent's guardrails have.
//
// Everyone rides in the same list (recipeShare): the publish steps,
// requested rather than applied unless an administrator is asking.
func (T *OrchestrateApp) pipelineShareOf(c *jsonapi.Call) (recipeShare, error) {
	p, err := T.pipelineAt(c)
	if err != nil {
		return recipeShare{}, err
	}
	if err := p.ownerOnly("change who can see"); err != nil {
		return recipeShare{}, err
	}
	user, udb, def := c.User, c.UDB, p.def
	rs := recipeShare{
		kind: pipelinePromotionKind, owner: user, name: def.Name,
		published: def.Published, before: def.AllowedUsers, editors: def.Editors,
		publish:   func() error { return publishPipelineForDeployment(user, def.ID) },
		unpublish: func() error { return unpublishPipeline(user, def.ID) },
	}
	rs.save = func(names, editors []string) any {
		cur, err := findOwnedPipeline(user, def.ID)
		if err != nil {
			cur = def
		}
		before := cur.AllowedUsers
		cur.Owner = user
		cur.AllowedUsers = normalizeShareList(names, user)
		cur.Editors = editors
		saved := SavePipelineDef(udb, cur)
		// Anybody dropped from the list loses it now, including any
		// schedule they had armed against it: a share taken back has to
		// take back everything it enabled, not just the next page load.
		breakSchedulesForLostRecipients(saved, before)
		Log("[orchestrate.pipelines] user=%q shared pipeline %q with %d user(s)", user, saved.Name, len(saved.AllowedUsers))
		saved.AllowedUsers = withEveryone(saved.AllowedUsers, saved.Published || rs.pending())
		return saved
	}
	return rs, nil
}

func (T *OrchestrateApp) pipelineShare(c *jsonapi.Call) (any, error) {
	rs, err := T.pipelineShareOf(c)
	if err != nil {
		return nil, err
	}
	return rs.list(), nil
}

func (T *OrchestrateApp) pipelineShareSave(c *jsonapi.Call, body struct {
	AllowedUsers []string  `json:"allowed_users"`
	Editors      *[]string `json:"editors"`
}) (any, error) {
	rs, err := T.pipelineShareOf(c)
	if err != nil {
		return nil, err
	}
	r := c.R
	out, err := rs.apply(r, body.AllowedUsers, body.Editors)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return out, nil
}

// pipelinePublishBody is a publish request: an optional note for the administrator.
type pipelinePublishBody struct {
	Note string `json:"note"` // optional
}

// pipelinePublish asks an administrator to let EVERY user of the deployment
// run this. The narrower grant beside it is the owner's own to make; this one
// is not, which is why the door only files a request. A separate door from
// share for the same reason share is separate from the editor: each one
// opens exactly one thing.
func (T *OrchestrateApp) pipelinePublish(c *jsonapi.Call, body *pipelinePublishBody) (any, error) {
	if body == nil {
		body = &pipelinePublishBody{}
	}
	p, err := T.pipelineAt(c)
	if err != nil {
		return nil, err
	}
	if err := p.ownerOnly("publish"); err != nil {
		return nil, err
	}
	if p.def.Published {
		return nil, jsonapi.BadRequest("this pipeline is already published")
	}
	if err := CreatePromotionRequest(AuthDB(), c.User, pipelinePromotionKind, p.def.Name, body.Note); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[orchestrate.pipelines] user=%q asked to publish pipeline %q", c.User, p.def.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// pipelineUnpublish is owner-direct, exactly as revoking a share is: nobody
// needs permission to stop publishing something they wrote.
func (T *OrchestrateApp) pipelineUnpublish(c *jsonapi.Call, _ struct{}) (any, error) {
	p, err := T.pipelineAt(c)
	if err != nil {
		return nil, err
	}
	if err := p.ownerOnly("take back"); err != nil {
		return nil, err
	}
	if err := unpublishPipeline(c.User, p.def.ID); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handlePipelineBlock serves one derived block of the editor page, by name, as the HTML the
// page itself renders (pipeline_page.go). A ui.Card with a Source
// fetches this when a stage is saved, which is what keeps the
// plan, the price and the findings describing the pipeline as it
// is rather than as it was when the page was opened.
func (T *OrchestrateApp) handlePipelineBlock(w http.ResponseWriter, r *http.Request, p *pipelineCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	html, ok := pipelinePageBlock(p.call.UDB, p.call.User, p.def, strings.TrimSpace(r.URL.Query().Get("name")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}

// handlePipelineGraph serves the picture (docs/design/workflow-graph.md), so the editor can redraw
// it after an edit that changed the SHAPE — ticking a branch's
// destination adds an arrow, and the map is rendered server-side.
//
// Served as SVG so it can be linked or saved on its own; the page
// fetches the text and injects it inline, which is what lets the
// page's CSS variables theme it. Every dynamic string in the
// document is escaped at the renderer (see xmlEscape).
func (T *OrchestrateApp) handlePipelineGraph(w http.ResponseWriter, r *http.Request, p *pipelineCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// ?links=1 draws it as the editor's MAP: every box a door into
	// that stage's section. Same picture either way — the difference
	// is anchors, which only mean something inside the page that has
	// those sections. Mirrors the machines route.
	if r.URL.Query().Get("links") == "1" {
		_, _ = w.Write([]byte(pipelineGraphSVG(p.def)))
		return
	}
	_, _ = w.Write([]byte(p.def.Graph().SVG(nil)))
}

// handlePipelineExport downloads the recipe.
func (T *OrchestrateApp) handlePipelineExport(w http.ResponseWriter, r *http.Request, p *pipelineCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	recipe := ExportPipeline(p.def)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+SnakeFromDisplay(p.def.Name)+".pipeline.json\"")
	_ = json.NewEncoder(w).Encode(recipe)
}

// pipelineRun executes a pipeline synchronously for the browser and
// returns {output}. Agent stages dispatch through RunAgentSync as the
// requesting user. Sync (no SSE) for the MVP panel — the request blocks
// until the run finishes; a streaming/async run endpoint can layer on
// later if slow pipelines make the blocking call painful.
func (T *OrchestrateApp) pipelineRun(c *jsonapi.Call, body struct {
	Input string `json:"input"`
}) (any, error) {
	p, perr := T.pipelineAt(c)
	if perr != nil {
		return nil, perr
	}
	r, user, def := c.R, c.User, p.def
	input := strings.TrimSpace(body.Input)
	if input == "" {
		return nil, jsonapi.BadRequest("input required")
	}
	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		return T.RunAgentSync(ctx, user, user, agentID, stageInput)
	}
	ctx, cancel := context.WithTimeout(r.Context(), knowledgeIngestTimeout()*8)
	defer cancel()
	out, _, err := T.RunPipelineDefHooks(ctx, def, input, PipelineHooks{
		Dispatch: dispatch,
		Machine:  T.pipelineMachineRunner(user),
	})
	if err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return map[string]any{"output": out}, nil
}
