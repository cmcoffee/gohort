// HTTP surface for phase machines (core.MachineDef). Mirrors the
// pipeline routes exactly — per-user CRUD plus the portable-recipe
// export/import — because a machine is the same kind of object: a
// serializable recipe with no runtime state in it.
//
//	GET    /api/machines              → {machines: [{id, name, description, phases, start}]}
//	POST   /api/machines              → create (body: {name, description, start, phases})
//	POST   /api/machines/import       → import a recipe (body: exported MachineDef)
//	GET    /api/machines/{id}         → full def
//	PUT    /api/machines/{id}         → replace the def's fields/phases
//	DELETE /api/machines/{id}         → delete
//	GET    /api/machines/{id}/export  → portable recipe (id/owner/timestamps stripped)
//	GET    /api/machines/{id}/block?name=map|advice|checklist → one derived block of the editor page, as HTML
//	GET    /api/machines/{id}/graph   → SVG of the phases and transitions;
//	                                    ?agent=&session= overlays a live conversation
//
// There is no /run: a machine has nowhere to run OUTSIDE a session. It
// runs when a chat turn on an agent that points at it comes in. That
// asymmetry with pipelines is the whole difference between the two
// primitives, and it is why this file is half the length of its twin.
//
// Pointing an agent at a machine happens in three places, all writing
// the same AgentRecord.Machine field: the machine page's "Assign to
// agents", the `machine` tool's attach_to_agents, and the partial-update
// path POST /api/agents/{agentID} {"machine": "<id>"} (`""` detaches).
// The agent editor no longer sends the field, so handleAgentList
// distinguishes a body that OMITTED it from one that cleared it: see the
// key probe there.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// handleSessionStatus feeds the chat toolbar's status pill
// (AgentLoopPanel.StatusURL) — for a session running a machine, which
// phase it is sitting in.
//
//	GET /api/session-status?agent=<id>&session=<id> → {label, title, tone}
//
// An empty label renders nothing, which is every session that isn't
// running a machine — i.e. all of them until someone attaches one. The
// panel contract is generic on purpose; this is orchestrate's answer to
// it, and another app's would say something else entirely.
func (T *OrchestrateApp) handleSessionStatus(c *jsonapi.Call) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if agentID == "" || sessionID == "" {
		return map[string]any{}, nil
	}
	sess, ok := loadChatSession(udb, agentID, sessionID)
	if !ok {
		return map[string]any{}, nil
	}
	if sess.Phase == "" || sess.MachineID == "" {
		// No machine: the pill is the thread's CONTEXT readout instead, when
		// the thread has folded — the one moment its shape changed under the
		// owner (session_context.go). Silent until then.
		return contextStatus(udb, sess), nil
	}
	def, ok := LoadMachineDef(udb, user, sess.MachineID)
	if !ok {
		// The machine was deleted under a live session. Say so rather than
		// showing a phase name from a workflow that no longer exists —
		// the next turn will run as an ordinary agent turn, and the pill
		// is where someone would look to understand why.
		return map[string]any{
			"label": "phase: " + sess.Phase + " (machine gone)",
			"title": "The machine this conversation was running has been deleted. Turns now run as ordinary agent turns.",
		}, nil
	}
	title := "Phase " + sess.Phase + " of " + def.Name + "."
	if ph, found := def.Phase(sess.Phase); found && strings.TrimSpace(ph.Desc) != "" {
		title += " " + strings.TrimSpace(ph.Desc)
	}
	if n := len(sess.MachineState); n > 0 {
		title += " " + strconv.Itoa(n) + " earlier phase result(s) pinned to this conversation."
	}
	// The pill is a door now: detail_url opens the state drawer (blackboard,
	// opening message, transition log) and actions are the owner's two
	// levers. See machine_session_state.go.
	detail, actions := machineStatusActions(agentID, sessionID)
	return map[string]any{"label": sess.Phase, "title": title + " Click for the blackboard and transitions.", "tone": "active",
		"detail_url": detail, "actions": actions}, nil
}

// machineRow is the trimmed list shape: enough for a picker and a list
// view without shipping every phase's prompt on a list call.
type machineRow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Phases      int      `json:"phases"`
	PhaseNames  []string `json:"phase_names,omitempty"`
	Start       string   `json:"start,omitempty"`
	// UsedBy names the agents pointing at this machine. Computed here
	// rather than left to the client because it is the single most
	// useful fact about a machine: an unattached one is inert, and
	// "why is nothing happening" is the question this answers.
	UsedBy []string `json:"used_by,omitempty"`
	// Rendered forms for the Extensions table. Computed here rather than
	// in the page because a row should say the same thing wherever it is
	// shown, and "used by nothing" is the single most useful fact about a
	// machine — an unattached one is inert.
	UsedByText string `json:"used_by_text,omitempty"`
	Status     string `json:"status,omitempty"`
	EditURL    string `json:"edit_url,omitempty"`
	// Legend explains the graph the modal renders beside the row —
	// chiefly what is deliberately NOT drawn. Comes from the same
	// adapter as the picture so the two can never disagree.
	Legend []string `json:"legend,omitempty"`
	// Mine / Owner / SharedWith carry the peer-share facts, the same three
	// a pipeline row carries: whether this user owns it, whose it is when
	// they don't, and who they have given it to when they do.
	Mine       bool   `json:"mine,omitempty"`
	Owner      string `json:"owner,omitempty"`
	SharedWith string `json:"shared_with,omitempty"`
}

// handleMachines serves the collection routes: GET list, POST create.
func (T *OrchestrateApp) handleMachines(c *jsonapi.Call, raw json.RawMessage) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	switch r.Method {
	case http.MethodGet:
		// ?starter=1 — a blank machine to edit, rather than an empty box.
		//
		// Served from Go rather than written in the editor's JavaScript so
		// there is ONE copy and a test can prove it validates. A starter
		// that gets rejected on first save teaches the wrong lesson about
		// the whole feature.
		if r.URL.Query().Get("starter") == "1" {
			return StarterMachine(), nil
		}
		defs := ListMachineDefs(udb, user)
		users := map[string][]string{}
		for _, ag := range listAgents(udb, user) {
			if ag.Machine != "" {
				users[ag.Machine] = append(users[ag.Machine], chFirst(ag.Name, ag.ID))
			}
		}
		rows := make([]machineRow, 0, len(defs))
		for _, d := range defs {
			rows = append(rows, machineRow{
				ID: d.ID, Name: d.Name, Description: d.Description,
				Phases: len(d.Phases), PhaseNames: d.PhaseNames(),
				Start: d.StartPhase(), UsedBy: users[d.ID],
				Legend:     d.Graph().Legend,
				UsedByText: usedByText(users[d.ID]),
				Status:     machineStatusText(d),
				EditURL:    "/orchestrate/machine?id=" + d.ID,
				Mine:       true,
				SharedWith: ShareRecipientsText(d.AllowedUsers),
			})
		}
		// Machines other people shared, after the user's own and labelled with
		// whose they are. Listed at all because a share nobody can find is a
		// share that did not happen; listed LAST because your own work is what
		// you came to this page for.
		for _, sm := range sharedMachinesFor(user) {
			rows = append(rows, machineRow{
				ID: sm.Def.ID, Name: sm.Def.Name, Description: sm.Def.Description,
				Phases: len(sm.Def.Phases), PhaseNames: sm.Def.PhaseNames(),
				Start:   sm.Def.StartPhase(),
				Legend:  sm.Def.Graph().Legend,
				Status:  "shared by " + sm.Owner,
				EditURL: "/orchestrate/machine?id=" + sm.Def.ID,
				Owner:   sm.Owner,
			})
		}
		return map[string]any{"machines": rows}, nil
	case http.MethodPost:
		var def MachineDef
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, jsonapi.BadRequest("bad request")
		}
		// Force a fresh, user-owned record — the client doesn't get to
		// assert id/owner on create.
		def.ID = ""
		def.Owner = user
		// Sharing is its own door (/share). A create that could assert a
		// recipient list would be a second way to grant, and the two would
		// eventually disagree about what a grant means.
		def.AllowedUsers = nil
		def.Editors = nil
		def.Published = false
		if err := def.Validate(); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		saved := SaveMachineDef(udb, def)
		Log("[orchestrate.machines] user=%q created machine %q (id=%s, %d phases)", user, saved.Name, saved.ID, len(saved.Phases))
		return saved, nil
	default:
		return nil, jsonapi.Status(http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleMachineImport accepts a portable recipe and saves it as a new
// user-owned machine. Registered as a more-specific route than
// /api/machines/ so it wins the ServeMux longest-prefix match.
func (T *OrchestrateApp) handleMachineImport(w http.ResponseWriter, r *http.Request) {
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
	if importBundleAtDoor(w, user, body, "machine", func(name string) (any, bool) {
		return findMachineByNameOrID(udb, user, name)
	}) {
		return
	}
	recipe, err := decodeMachineRecipe(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, err := ImportMachine(udb, user, recipe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[orchestrate.machines] user=%q imported machine %q (id=%s)", user, saved.Name, saved.ID)
	jsonapi.Write(w, saved)
}

// registerMachineRoutes mounts one machine's routes: the record, its
// actions, and the run sub-protocol.
func (T *OrchestrateApp) registerMachineRoutes() {
	T.HandleFunc("/api/machines/{id}", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.machineGet),
		"PUT":    jsonapi.Put(T.DB, T.machineSave),
		"DELETE": jsonapi.Delete(T.DB, T.machineDelete),
	}.Serve)
	T.HandleFunc("/api/machines/{id}/editor", jsonapi.Get(T.DB, T.machineEditor))
	T.HandleFunc("/api/machines/{id}/meta", jsonapi.Methods{
		"GET":   jsonapi.Get(T.DB, T.machineMeta),
		"POST":  jsonapi.Post(T.DB, T.machineMetaSave),
		"PUT":   jsonapi.Put(T.DB, T.machineMetaSave),
		"PATCH": jsonapi.Patch(T.DB, T.machineMetaSave),
	}.Serve)
	T.HandleFunc("/api/machines/{id}/phases", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.machinePhases),
		"POST":   jsonapi.Post(T.DB, T.machinePhaseSave),
		"PUT":    jsonapi.Put(T.DB, T.machinePhaseSave),
		"PATCH":  jsonapi.Patch(T.DB, T.machinePhaseSave),
		"DELETE": jsonapi.Delete(T.DB, T.machinePhaseDelete),
	}.Serve)
	T.HandleFunc("/api/machines/{id}/try", jsonapi.Post(T.DB, T.machineTry))
	T.HandleFunc("/api/machines/{id}/duplicate", jsonapi.Post(T.DB, T.machineDuplicate))
	T.HandleFunc("/api/machines/{id}/revise", jsonapi.Post(T.DB, T.machineRevise))
	T.HandleFunc("/api/machines/{id}/undo", jsonapi.Post(T.DB, T.machineUndo))
	T.HandleFunc("/api/machines/{id}/repair", jsonapi.Post(T.DB, T.machineRepair))
	T.HandleFunc("/api/machines/{id}/move", jsonapi.Post(T.DB, T.machineMove))
	T.HandleFunc("/api/machines/{id}/agents", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.machineAgents),
		"POST": jsonapi.Post(T.DB, T.machineAgentsSave, jsonapi.MaxBody(maxImportBytes)),
		"PUT":  jsonapi.Put(T.DB, T.machineAgentsSave, jsonapi.MaxBody(maxImportBytes)),
	}.Serve)
	T.HandleFunc("/api/machines/{id}/share", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.machineShare),
		"POST": jsonapi.Post(T.DB, T.machineShareSave),
	}.Serve)
	T.HandleFunc("/api/machines/{id}/publish", jsonapi.Post(T.DB, T.machinePublish, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/machines/{id}/unpublish", jsonapi.Post(T.DB, T.machineUnpublish))
	// Not JSON in and out: the ✨ step draft, an editor page block, the
	// picture, a download, the revisions surface and the run sub-protocol.
	T.HandleFunc("/api/machines/{id}/suggest", T.machineRaw(func(w http.ResponseWriter, r *http.Request, mc *machineCall) {
		// Drafting one step's instructions, grounded in the machine
		// around it (machine_suggest.go).
		if err := mc.editable("draft a step for"); err != nil {
			jsonapi.Fail(w, r, err)
			return
		}
		T.handleMachineSuggest(w, r, mc.ed.db, mc.ed.owner, mc.def)
	}))
	T.HandleFunc("/api/machines/{id}/block", T.machineRaw(T.handleMachineBlock))
	T.HandleFunc("/api/machines/{id}/graph", T.machineRaw(T.handleMachineGraph))
	T.HandleFunc("/api/machines/{id}/export", T.machineRaw(T.handleMachineExport))
	revisions := T.machineRaw(func(w http.ResponseWriter, r *http.Request, mc *machineCall) {
		T.handleMachineRevisions(w, r, mc.call.User, mc.def, mc.owner, mc.ed.ok, r.PathValue("action"))
	})
	T.HandleFunc("/api/machines/{id}/revisions", revisions)
	T.HandleFunc("/api/machines/{id}/revisions/{action}", revisions)
	// runs/... is a sub-protocol rather than a single action: the panel
	// speaks stream / sessions / sessions/<id>, and the tail says which.
	runs := T.machineRaw(func(w http.ResponseWriter, r *http.Request, mc *machineCall) {
		T.handleMachineRuns(w, r, mc.call.UDB, mc.call.User, mc.def, r.PathValue("rest"))
	})
	T.HandleFunc("/api/machines/{id}/runs", runs)
	T.HandleFunc("/api/machines/{id}/runs/{rest...}", runs)
}

// machineCall is one request on one machine: the definition, whose it is,
// whether it is the caller's, and where an edit to it goes (recipeEdit).
type machineCall struct {
	call  *jsonapi.Call
	def   MachineDef
	owner string
	mine  bool
	ed    recipeEdit
}

// machineAt resolves the machine the path names: the caller's own, then one
// somebody shared with them (machine_sharing.go). A recipient reads, runs
// and copies; the definition stays the owner's, and an editor's change goes
// to the owner's copy.
func (T *OrchestrateApp) machineAt(c *jsonapi.Call) (*machineCall, error) {
	def, owner, mine, found := resolveMachineFor(c.User, c.UDB, c.R.PathValue("id"))
	if !found {
		return nil, errNotFound
	}
	return &machineCall{call: c, def: def, owner: owner, mine: mine,
		ed: recipeEditFor(c.User, c.UDB, owner, mine, def.Editors)}, nil
}

// editable refuses a change by somebody it was shared with to run.
func (mc *machineCall) editable(verb string) error {
	return recipeEditRefusal(mc.ed, "machine", mc.owner, verb)
}

// ownerOnly refuses what stays the owner's: deleting, sharing, publishing
// and attaching, in the words that say what to do instead.
func (mc *machineCall) ownerOnly(verb string) error {
	if mc.mine {
		return nil
	}
	return jsonapi.Forbidden("this machine is shared with you by " + mc.owner + ", so you can run and copy it but not " + verb + " it: ask " + mc.owner + " to make the change, or duplicate it and edit your copy")
}

// machineRaw is a raw route on one machine, refused as the JSON ones are.
func (T *OrchestrateApp) machineRaw(h func(w http.ResponseWriter, r *http.Request, mc *machineCall)) http.HandlerFunc {
	return T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, udb Database) {
		mc, err := T.machineAt(&jsonapi.Call{W: w, R: r, User: user, UDB: udb})
		if err != nil {
			jsonapi.Fail(w, r, err)
			return
		}
		h(w, r, mc)
	})
}

func (T *OrchestrateApp) machineGet(c *jsonapi.Call) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	return mc.def, nil
}

// machineSave is the editor's whole-record save. Identity stays the
// server's: id, owner, created, who it is shared with and whether it is
// published.
func (T *OrchestrateApp) machineSave(c *jsonapi.Call, body MachineDef) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.editable("edit"); err != nil {
		return nil, err
	}
	user, def, ed := c.User, mc.def, mc.ed
	// Identity stays the server's: keep id/owner/created from the
	// loaded record, take everything else from the body. The recipient
	// list is identity too — an edit form must not be able to widen or
	// clear who a machine is shared with, which is why /share is its
	// own door.
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
	saved := SaveMachineDef(ed.db, body)
	Log("[orchestrate.machines] user=%q updated machine %q (id=%s, owner %q)", user, saved.Name, saved.ID, ed.owner)
	return saved, nil
}

func (T *OrchestrateApp) machineDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.ownerOnly("delete"); err != nil {
		return nil, err
	}
	udb, user, def := c.UDB, c.User, mc.def
	// Detach from every agent first — same as the tool's delete,
	// through the same function, so an agent is never left
	// pointing at a machine that isn't there.
	//
	// Live SESSIONS are a different case and are deliberately
	// left alone: they keep their pinned MachineID and cursor,
	// and their next turn degrades to an ordinary agent turn with
	// a machine_missing breadcrumb. Broken-dependency posture —
	// surface the break, don't quietly rewrite a conversation
	// somebody is in the middle of.
	detached := detachMachineFromAgents(udb, user, def.ID)
	DeleteMachineDef(udb, def.ID)
	Log("[orchestrate.machines] user=%q deleted machine %q (id=%s, detached from %d agent(s))", user, def.Name, def.ID, len(detached))
	return map[string]any{"deleted": def.ID, "detached": detached}, nil
}

// machineDuplicate copies the machine into the caller's own store.
// Iterating on a working machine in place is how the working one stops
// working. A copy makes the experiment safe, and landing in the copy's
// editor is the reason anybody clicked.
func (T *OrchestrateApp) machineDuplicate(c *jsonapi.Call, _ struct{}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	udb, user, def := c.UDB, c.User, mc.def
	dup := def
	dup.ID = ""
	// A copy of somebody else's machine is YOURS: re-owned, and shared with
	// nobody until you say so. Carrying their recipient list into your
	// store would re-share their work under your name. Previous goes too —
	// an undo of an edit you did not make is not an undo.
	dup.Owner = user
	dup.AllowedUsers = nil
	dup.Editors = nil
	// And not published. An administrator agreed that THAT record should
	// reach everybody, not every copy anyone makes of it.
	dup.Published = false
	dup.Previous = nil
	dup.Name = copyName(def.Name, machineNames(ListMachineDefs(udb, user)))
	saved := SaveMachineDef(udb, dup)
	Log("[orchestrate.machines] user=%q duplicated machine %q as %q (id=%s)", user, def.Name, saved.Name, saved.ID)
	return map[string]any{"id": saved.ID, "name": saved.Name}, nil
}

// machineRepair settles the findings that have exactly one right answer. The
// class this exists for is a reference to a step that is gone:
// the picker no longer offers the name, so the finding names
// something there is no longer any way to open or clear.
//
// Scoped by the panel that asked, because a button in one list
// that quietly rewrote the other's findings would be its own
// surprise. Nothing here guesses at intent — see core's
// machine_repair.go for what it refuses.
func (T *OrchestrateApp) machineRepair(c *jsonapi.Call, _ struct{}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	r, user, def, ed := c.R, c.User, mc.def, mc.ed
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	switch kind {
	case RepairAll, RepairProblems, RepairAdvice:
	default:
		return nil, jsonapi.BadRequest("unknown repair kind")
	}
	if err := mc.editable("repair"); err != nil {
		return nil, err
	}
	fixed := def.Repair(kind)
	if len(fixed) == 0 {
		return map[string]any{"fixed": []string{}}, nil
	}
	saved := SaveMachineDef(ed.db, def)
	Log("[orchestrate.machines] user=%q repaired machine %q: %s", user, saved.Name,
		strings.Join(RepairLines(fixed), "; "))
	return map[string]any{"fixed": RepairLines(fixed), "id": saved.ID}, nil
}

// machineMove moves a step up or down. Reordering is cosmetic to the DRIVER (routing is by name) but
// not to the person: the order is the rail, the reading order,
// and the first-resident fallback.
func (T *OrchestrateApp) machineMove(c *jsonapi.Call, mv struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.editable("reorder"); err != nil {
		return nil, err
	}
	def, ed := mc.def, mc.ed
	idx := -1
	for i, p := range def.Phases {
		if p.Name == strings.TrimSpace(mv.Name) {
			idx = i
		}
	}
	to := idx - 1
	if mv.Dir == "down" {
		to = idx + 1
	}
	if idx < 0 || to < 0 || to >= len(def.Phases) {
		return nil, jsonapi.BadRequest("nowhere to move it")
	}
	// Pin the entry point before moving anything. An unset start
	// RESOLVES to the first step (StartPhase), and the editor shows
	// that resolved value — so a machine that never chose one looks
	// settled while actually being positional, and reordering would
	// move where conversations begin without anyone touching the
	// control that claims to say so. Writing it down first makes the
	// displayed answer the stored one and this move a no-op for it.
	if strings.TrimSpace(def.Start) == "" {
		def.Start = def.StartPhase()
	}
	def.Phases[idx], def.Phases[to] = def.Phases[to], def.Phases[idx]
	SaveMachineDef(ed.db, def)
	return map[string]any{"ok": true, "start": def.Start}, nil
}

// machineShareOf is the share door's list of who can see the machine: the
// recipient list, and ONLY the recipient list. The ACLPicker posts the whole
// record back (record mode), and this reads one field out of it — so a
// picker save cannot rewrite a step, and the ordinary edit
// path cannot rewrite the ACL.
//
// Everyone rides in the same list (recipeShare): the publish steps,
// requested rather than applied unless an administrator is asking.
func (T *OrchestrateApp) machineShareOf(c *jsonapi.Call) (recipeShare, error) {
	mc, err := T.machineAt(c)
	if err != nil {
		return recipeShare{}, err
	}
	if err := mc.ownerOnly("change who can see"); err != nil {
		return recipeShare{}, err
	}
	user, udb, def := c.User, c.UDB, mc.def
	rs := recipeShare{
		kind: machinePromotionKind, owner: user, name: def.Name,
		published: def.Published, before: def.AllowedUsers, editors: def.Editors,
		publish:   func() error { return publishMachineForDeployment(user, def.ID) },
		unpublish: func() error { return unpublishMachine(user, def.ID) },
	}
	rs.save = func(names, editors []string) any {
		cur, err := findOwnedMachine(user, def.ID)
		if err != nil {
			cur = def
		}
		before := cur.AllowedUsers
		cur.Owner = user
		cur.AllowedUsers = normalizeShareList(names, user)
		cur.Editors = editors
		saved := SaveMachineDef(udb, cur)
		// Anybody dropped from the list loses it now, including any
		// schedule they had armed against it: a share taken back has to
		// take back everything it enabled, not just the next page load.
		breakMachineSchedulesForLostRecipients(saved, before)
		Log("[orchestrate.machines] user=%q shared machine %q with %d user(s)", user, saved.Name, len(saved.AllowedUsers))
		saved.AllowedUsers = withEveryone(saved.AllowedUsers, saved.Published || rs.pending())
		return saved
	}
	return rs, nil
}

func (T *OrchestrateApp) machineShare(c *jsonapi.Call) (any, error) {
	rs, err := T.machineShareOf(c)
	if err != nil {
		return nil, err
	}
	return rs.list(), nil
}

func (T *OrchestrateApp) machineShareSave(c *jsonapi.Call, body struct {
	AllowedUsers []string  `json:"allowed_users"`
	Editors      *[]string `json:"editors"`
}) (any, error) {
	rs, err := T.machineShareOf(c)
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

// machinePublish asks an administrator to let EVERY user of the deployment
// run this. The narrower grant beside it is the owner's own to make; this one
// is not, which is why the door only files a request.
func (T *OrchestrateApp) machinePublish(c *jsonapi.Call, body struct {
	Note string `json:"note"` // optional
}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.ownerOnly("publish"); err != nil {
		return nil, err
	}
	if mc.def.Published {
		return nil, jsonapi.BadRequest("this machine is already published")
	}
	if err := CreatePromotionRequest(AuthDB(), c.User, machinePromotionKind, mc.def.Name, body.Note); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[orchestrate.machines] user=%q asked to publish machine %q", c.User, mc.def.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// machineUnpublish is owner-direct, exactly as revoking a share is.
func (T *OrchestrateApp) machineUnpublish(c *jsonapi.Call, _ struct{}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.ownerOnly("take back"); err != nil {
		return nil, err
	}
	if err := unpublishMachine(c.User, mc.def.ID); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMachineBlock serves one derived block of the editor page, by name, as the HTML the
// page itself renders (machine_page.go). A ui.Card with a Source
// fetches this when a step is saved, which is what keeps the map
// and the two findings lists describing the machine as it is
// rather than as it was when the page was opened.
func (T *OrchestrateApp) handleMachineBlock(w http.ResponseWriter, r *http.Request, mc *machineCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	html, ok := machinePageBlock(mc.call.UDB, mc.call.User, mc.def, strings.TrimSpace(r.URL.Query().Get("name")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}

// handleMachineGraph serves the picture (docs/design/workflow-graph.md). With ?session=<id> it
// carries the overlay: where that conversation is sitting and
// which edges it has actually taken. Without one it is the plain
// structure.
//
// Served as SVG so it can be linked, saved, or opened on its own;
// the modal fetches the text and injects it inline, which is what
// lets the page's CSS variables theme it. Every dynamic string in
// the document is escaped at the renderer (see xmlEscape) for
// exactly that reason.
func (T *OrchestrateApp) handleMachineGraph(w http.ResponseWriter, r *http.Request, mc *machineCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var overlay *WorkflowOverlay
	if sid := strings.TrimSpace(r.URL.Query().Get("session")); sid != "" {
		agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
		if sess, ok := loadChatSession(mc.call.UDB, agentID, sid); ok && sess.MachineID == mc.def.ID {
			cur := MachineCursor{Phase: sess.Phase, State: sess.MachineState, Log: sess.MachineLog}
			overlay = cur.Overlay()
		}
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// ?links=1 draws it as the editor's MAP: every box a door into
	// that step's section. Same picture either way — the difference
	// is anchors, which only mean something inside the page that has
	// those sections.
	if r.URL.Query().Get("links") == "1" {
		_, _ = w.Write([]byte(machineGraphSVG(mc.def)))
		return
	}
	_, _ = w.Write([]byte(mc.def.Graph().SVG(overlay)))
}

// handleMachineExport downloads the recipe.
func (T *OrchestrateApp) handleMachineExport(w http.ResponseWriter, r *http.Request, mc *machineCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	recipe := ExportMachine(mc.def)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+SnakeFromDisplay(mc.def.Name)+".machine.json\"")
	_ = json.NewEncoder(w).Encode(recipe)
}

// usedByText says who runs a machine, or says plainly that nobody does.
// An unattached machine is inert, and "why is nothing happening" is the
// question this answers before it gets asked.
func usedByText(agents []string) string {
	if len(agents) == 0 {
		return "nobody yet"
	}
	return strings.Join(agents, ", ")
}

// machineStatusText is the checklist in one phrase: ready, or how much is
// left. The detail lives on the editor page; this is the reason to open
// it.
func machineStatusText(d MachineDef) string {
	probs := d.Problems()
	switch len(probs) {
	case 0:
		return "ready"
	case 1:
		return "1 thing to fix"
	}
	return strconv.Itoa(len(probs)) + " things to fix"
}

// machineAgents reads and writes the set of agents running this
// machine.
//
//	GET  → {"agents": [ids]}
//	POST {"agents": [ids]} → attach those, detach the rest
//
// The POST is the whole set, matching what a checklist saves: an agent
// in the list gets this machine (moving it off another one is legal and
// the form warns per option), an agent that WAS on this machine and is
// not in the list is detached. Agents on other machines that were never
// in the set are left alone.
func (T *OrchestrateApp) machineAgents(c *jsonapi.Call) (any, error) {
	// Owner-only, and not merely for symmetry: AgentRecord.Machine is
	// resolved in the AGENT owner's store on every turn, so pointing your
	// agent's brain at somebody else's machine would be a pointer their
	// session cannot follow. Duplicate it and attach the copy.
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.ownerOnly("attach"); err != nil {
		return nil, err
	}
	udb, user, def := c.UDB, c.User, mc.def
	r := c.R
	// ?pills=1 — the shape uiRenderScopePills wants, so assignment
	// can happen from the LIST as well as from inside the machine.
	// Somebody deciding which agents run what is looking at all of
	// them at once, and opening each machine to answer that is the
	// navigation the tools table already avoids.
	if r.URL.Query().Get("pills") == "1" {
		return machineAgentPills(udb, user, def), nil
	}
	var ids []string
	for _, ag := range listAgents(udb, user) {
		if ag.Machine == def.ID {
			ids = append(ids, ag.ID)
		}
	}
	return map[string]any{"agents": ids}, nil
}

// machineAgentsSave attaches and detaches: one toggle as the pills send it,
// or the whole checklist.
func (T *OrchestrateApp) machineAgentsSave(c *jsonapi.Call, raw json.RawMessage) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.ownerOnly("attach"); err != nil {
		return nil, err
	}
	udb, user, def := c.UDB, c.User, mc.def
	// A single toggle, as the pills send it. The whole-set form
	// below is the page's checklist and stays exactly as it was.
	var one struct {
		Target string `json:"target"`
		On     *bool  `json:"on"`
	}
	if json.Unmarshal(raw, &one) == nil && one.On != nil && strings.TrimSpace(one.Target) != "" {
		ag, ok := loadAgent(udb, strings.TrimSpace(one.Target))
		if !ok || ag.Owner != user {
			return nil, jsonapi.NotFound("no such agent")
		}
		switch {
		case *one.On:
			ag.Machine = def.ID
		case ag.Machine == def.ID:
			ag.Machine = ""
		}
		if _, err := saveAgent(udb, ag); err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
		}
		Log("[orchestrate.machines] user=%q machine %q: %s %q", user, def.Name,
			chIf(*one.On, "attached to", "detached from"), chFirst(ag.Name, ag.ID))
		// Said on the way IN only: detaching cannot leave a gap, and a
		// warning attached to the act of removing something reads as an
		// objection to removing it.
		gaps := []string{}
		if *one.On {
			gaps = machineAttachGaps(udb, user, def, ag)
			for _, g := range gaps {
				Log("[orchestrate.machines] preflight: %s", g)
			}
		}
		return map[string]any{"ok": true, "gaps": gaps}, nil
	}
	var body struct {
		Agents []string `json:"agents"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, jsonapi.BadRequest("bad request")
	}
	want := make(map[string]bool, len(body.Agents))
	for _, id := range body.Agents {
		want[strings.TrimSpace(id)] = true
	}
	var attached, detached []string
	for _, ag := range listAgents(udb, user) {
		// The checklist never shows app agents or hidden ones, so
		// this save must never touch them either — a whole-set POST
		// detaches whatever is unchecked, and an agent that was
		// never ON the list would be silently unplugged by every
		// save of the agents that are.
		if isAppAgent(ag.ID) || ag.Hidden {
			continue
		}
		switch {
		case want[ag.ID] && ag.Machine != def.ID:
			ag.Machine = def.ID
			if _, err := saveAgent(udb, ag); err == nil {
				attached = append(attached, chFirst(ag.Name, ag.ID))
			}
		case !want[ag.ID] && ag.Machine == def.ID:
			ag.Machine = ""
			if _, err := saveAgent(udb, ag); err == nil {
				detached = append(detached, chFirst(ag.Name, ag.ID))
			}
		}
	}
	if len(attached)+len(detached) > 0 {
		Log("[orchestrate.machines] user=%q machine %q attached=%v detached=%v", user, def.Name, attached, detached)
	}
	return map[string]any{"ok": true, "attached": attached, "detached": detached}, nil
}

// copyName picks the duplicate's name: "X (copy)", counting up until it
// is not already taken — two unlabelled "X"s in a list is a coin flip
// every time somebody attaches one.
//
// Takes NAMES rather than definitions: pipelines duplicate too, and one
// of the two would otherwise grow its own copy of this and drift.
func copyName(base string, existing []string) string {
	taken := make(map[string]bool, len(existing))
	for _, n := range existing {
		taken[n] = true
	}
	name := base + " (copy)"
	for n := 2; taken[name]; n++ {
		name = base + " (copy " + strconv.Itoa(n) + ")"
	}
	return name
}

// decodeMachineRecipe reads a recipe in either of the two shapes it
// legitimately arrives in.
//
// A tool or a script POSTs the recipe itself. The browser posts a FORM,
// and a file field carries the chosen file as TEXT under its own name —
// so the body is {"recipe": "{…the json…}"}. Accepting only the first
// meant the endpoint existed and the page could not reach it, which is
// how it went unreachable for so long.
func decodeMachineRecipe(body []byte) (MachineDef, error) {
	var wrapper struct {
		Recipe string `json:"recipe"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && strings.TrimSpace(wrapper.Recipe) != "" {
		body = []byte(wrapper.Recipe)
	}
	var recipe MachineDef
	if err := json.Unmarshal(body, &recipe); err != nil {
		// The likeliest mistake by far: a file that is not a machine, or
		// not JSON at all. Say which rather than "bad request".
		return MachineDef{}, Error("that does not read as a machine recipe (" + err.Error() + ")")
	}
	return recipe, nil
}

// machineNames and pipelineNames feed copyName. Two lines each, and the
// alternative is a generic that hides which list is which.
func machineNames(defs []MachineDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

func pipelineNames(defs []PipelineDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// machineAgentPills is the scope-pill view of "who runs this machine".
//
// An agent runs at most ONE machine, so a pill that is off may still be
// busy — and turning it on MOVES that agent rather than adding to a
// list. The label says which machine it would leave, because the
// alternative is somebody discovering it afterwards.
func machineAgentPills(udb Database, user string, def MachineDef) map[string]any {
	names := map[string]string{}
	for _, d := range ListMachineDefs(udb, user) {
		names[d.ID] = d.Name
	}
	items := []map[string]any{}
	for _, ag := range listAgents(udb, user) {
		if isAppAgent(ag.ID) || ag.Hidden {
			continue
		}
		label := chFirst(ag.Name, ag.ID)
		if ag.Machine != "" && ag.Machine != def.ID {
			label += " (runs " + chFirst(names[ag.Machine], "another machine") + ")"
		}
		items = append(items, map[string]any{
			"key": ag.ID, "label": label, "on": ag.Machine == def.ID,
		})
	}
	note := "An agent runs one machine at a time, so switching one on moves it off whatever it was running. Sessions already open keep the machine they started with."
	// The preflight. The pills reload after every toggle, so attaching a
	// machine to an agent that cannot reach what its steps name says so
	// immediately — where the decision is being made, by the person making
	// it. The alternative was a turnDiag on the first message, hours later,
	// phrased as a tool that had gone missing.
	if gaps := machineAttachGapsForAll(udb, user, def); len(gaps) > 0 {
		note += "\n\n⚠ " + strings.Join(gaps, "\n⚠ ")
	}
	return map[string]any{"items": items, "note": note}
}

// machineAttachGapsForAll is the preflight over every agent currently
// attached. One line per (step, agent) pair that cannot work, and nothing at
// all when they all can — a warning that appears on healthy configurations is
// one people learn to scroll past.
func machineAttachGapsForAll(udb Database, user string, def MachineDef) []string {
	named := false
	for _, p := range def.Phases {
		if len(p.Tools) > 0 {
			named = true
			break
		}
	}
	if !named {
		return nil // nothing names a tool; nothing can be missing one
	}
	var out []string
	for _, ag := range listAgents(udb, user) {
		if ag.Machine != def.ID {
			continue
		}
		out = append(out, machineAttachGaps(udb, user, def, ag)...)
	}
	return out
}
