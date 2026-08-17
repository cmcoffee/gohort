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
//	GET    /api/machines/{id}/graph   → SVG of the phases and transitions;
//	                                    ?agent=&session= overlays a live conversation
//
// There is no /run: a machine has nowhere to run OUTSIDE a session. It
// runs when a chat turn on an agent that points at it comes in. That
// asymmetry with pipelines is the whole difference between the two
// primitives, and it is why this file is half the length of its twin.
//
// Pointing an agent at a machine happens in three places, all writing
// the same AgentRecord.Machine field: the picker on the agent editor
// (machineSelectField), the `machine` tool's attach_to_agents, and the
// partial-update path POST /api/agents/{agentID} {"machine": "<id>"}
// (`""` detaches). The picker hides itself when the user has no
// machines, so handleAgentList distinguishes a body that OMITTED the
// field from one that cleared it — see the key probe there.

package orchestrate

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
func (T *OrchestrateApp) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if agentID == "" || sessionID == "" {
		writeJSON(w, map[string]any{})
		return
	}
	sess, ok := loadChatSession(udb, agentID, sessionID)
	if !ok || sess.Phase == "" || sess.MachineID == "" {
		writeJSON(w, map[string]any{})
		return
	}
	def, ok := LoadMachineDef(udb, user, sess.MachineID)
	if !ok {
		// The machine was deleted under a live session. Say so rather than
		// showing a phase name from a workflow that no longer exists —
		// the next turn will run as an ordinary agent turn, and the pill
		// is where someone would look to understand why.
		writeJSON(w, map[string]any{
			"label": "phase: " + sess.Phase + " (machine gone)",
			"title": "The machine this conversation was running has been deleted. Turns now run as ordinary agent turns.",
		})
		return
	}
	title := "Phase " + sess.Phase + " of " + def.Name + "."
	if ph, found := def.Phase(sess.Phase); found && strings.TrimSpace(ph.Desc) != "" {
		title += " " + strings.TrimSpace(ph.Desc)
	}
	if n := len(sess.MachineState); n > 0 {
		title += " " + strconv.Itoa(n) + " earlier phase result(s) pinned to this conversation."
	}
	writeJSON(w, map[string]any{"label": sess.Phase, "title": title, "tone": "active"})
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
}

// handleMachines serves the collection routes: GET list, POST create.
func (T *OrchestrateApp) handleMachines(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// ?starter=1 — a blank machine to edit, rather than an empty box.
		//
		// Served from Go rather than written in the editor's JavaScript so
		// there is ONE copy and a test can prove it validates. A starter
		// that gets rejected on first save teaches the wrong lesson about
		// the whole feature.
		if r.URL.Query().Get("starter") == "1" {
			writeJSON(w, StarterMachine())
			return
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
			})
		}
		writeJSON(w, map[string]any{"machines": rows})
	case http.MethodPost:
		var def MachineDef
		if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Force a fresh, user-owned record — the client doesn't get to
		// assert id/owner on create.
		def.ID = ""
		def.Owner = user
		if err := def.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved := SaveMachineDef(udb, def)
		Log("[orchestrate.machines] user=%q created machine %q (id=%s, %d phases)", user, saved.Name, saved.ID, len(saved.Phases))
		writeJSON(w, saved)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
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
	writeJSON(w, saved)
}

// handleMachineOne serves per-machine routes: GET / PUT / DELETE plus
// /export.
func (T *OrchestrateApp) handleMachineOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/machines/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	var id, action string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		id, action = rest[:slash], rest[slash+1:]
	} else {
		id = rest
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	def, found := LoadMachineDef(udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, def)
		case http.MethodPut:
			var body MachineDef
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			// Identity stays the server's: keep id/owner/created from the
			// loaded record, take everything else from the body.
			body.ID = def.ID
			body.Owner = user
			body.Created = def.Created
			if err := body.Validate(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			saved := SaveMachineDef(udb, body)
			Log("[orchestrate.machines] user=%q updated machine %q (id=%s)", user, saved.Name, saved.ID)
			writeJSON(w, saved)
		case http.MethodDelete:
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
			writeJSON(w, map[string]any{"deleted": def.ID, "detached": detached})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "editor":
		// The structured editor's spec (machine_editor.go): a checklist of
		// what is still missing plus the components that edit it.
		T.handleMachineEditor(w, r, udb, user, def)
	case "meta":
		T.handleMachineMeta(w, r, udb, user, def)
	case "phases":
		T.handleMachinePhases(w, r, udb, user, def)
	case "try":
		T.handleMachineTry(w, r, udb, user, def)
	case "duplicate":
		// Iterating on a working machine in place is how the working one
		// stops working. A copy makes the experiment safe, and landing in
		// the copy's editor is the reason anybody clicked.
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dup := def
		dup.ID = ""
		dup.Name = copyName(def.Name, ListMachineDefs(udb, user))
		saved := SaveMachineDef(udb, dup)
		Log("[orchestrate.machines] user=%q duplicated machine %q as %q (id=%s)", user, def.Name, saved.Name, saved.ID)
		writeJSON(w, map[string]any{"id": saved.ID, "name": saved.Name})
	case "repair":
		// Settle the findings that have exactly one right answer. The
		// class this exists for is a reference to a step that is gone:
		// the picker no longer offers the name, so the finding names
		// something there is no longer any way to open or clear.
		//
		// Scoped by the panel that asked, because a button in one list
		// that quietly rewrote the other's findings would be its own
		// surprise. Nothing here guesses at intent — see core's
		// machine_repair.go for what it refuses.
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		switch kind {
		case RepairAll, RepairProblems, RepairAdvice:
		default:
			http.Error(w, "unknown repair kind", http.StatusBadRequest)
			return
		}
		fixed := def.Repair(kind)
		if len(fixed) == 0 {
			writeJSON(w, map[string]any{"fixed": []string{}})
			return
		}
		saved := SaveMachineDef(udb, def)
		Log("[orchestrate.machines] user=%q repaired machine %q: %s", user, saved.Name,
			strings.Join(RepairLines(fixed), "; "))
		writeJSON(w, map[string]any{"fixed": RepairLines(fixed), "id": saved.ID})
	case "move":
		// Reordering is cosmetic to the DRIVER (routing is by name) but
		// not to the person: the order is the rail, the reading order,
		// and the first-resident fallback.
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var mv struct {
			Name string `json:"name"`
			Dir  string `json:"dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&mv); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
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
			http.Error(w, "nowhere to move it", http.StatusBadRequest)
			return
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
		SaveMachineDef(udb, def)
		writeJSON(w, map[string]any{"ok": true, "start": def.Start})
	case "agents":
		// Who runs this machine — readable and settable from the editor,
		// which is where you are standing when the question comes up. An
		// unattached machine does nothing, and the only place to attach
		// one used to be a different surface (the chat toolbar's
		// Configure → Machines).
		T.handleMachineAgents(w, r, udb, user, def)
	case "suggest":
		// Drafting one step's instructions, grounded in the machine
		// around it (machine_suggest.go).
		T.handleMachineSuggest(w, r, udb, user, def)
	case "graph":
		// The picture (docs/workflow-graph.md). With ?session=<id> it
		// carries the overlay: where that conversation is sitting and
		// which edges it has actually taken. Without one it is the plain
		// structure.
		//
		// Served as SVG so it can be linked, saved, or opened on its own;
		// the modal fetches the text and injects it inline, which is what
		// lets the page's CSS variables theme it. Every dynamic string in
		// the document is escaped at the renderer (see xmlEscape) for
		// exactly that reason.
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var overlay *WorkflowOverlay
		if sid := strings.TrimSpace(r.URL.Query().Get("session")); sid != "" {
			agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
			if sess, ok := loadChatSession(udb, agentID, sid); ok && sess.MachineID == def.ID {
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
			_, _ = w.Write([]byte(machineGraphSVG(def)))
			return
		}
		_, _ = w.Write([]byte(def.Graph().SVG(overlay)))
	case "export":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		recipe := ExportMachine(def)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+SnakeFromDisplay(def.Name)+".machine.json\"")
		_ = json.NewEncoder(w).Encode(recipe)
	default:
		http.NotFound(w, r)
	}
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

// handleMachineAgents reads and writes the set of agents running this
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
func (T *OrchestrateApp) handleMachineAgents(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	switch r.Method {
	case http.MethodGet:
		var ids []string
		for _, ag := range listAgents(udb, user) {
			if ag.Machine == def.ID {
				ids = append(ids, ag.ID)
			}
		}
		writeJSON(w, map[string]any{"agents": ids})
	case http.MethodPost, http.MethodPut:
		var body struct {
			Agents []string `json:"agents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
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
		writeJSON(w, map[string]any{"ok": true, "attached": attached, "detached": detached})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// copyName picks the duplicate's name: "X (copy)", counting up until it
// is not already taken — two unlabelled "X"s in a list is a coin flip
// every time somebody attaches one.
func copyName(base string, existing []MachineDef) string {
	taken := make(map[string]bool, len(existing))
	for _, m := range existing {
		taken[m.Name] = true
	}
	name := base + " (copy)"
	for n := 2; taken[name]; n++ {
		name = base + " (copy " + strconv.Itoa(n) + ")"
	}
	return name
}

// maxImportBytes bounds a pasted or uploaded recipe. A machine is
// prompts and wiring; anything past this is not one.
const maxImportBytes = 1 << 20

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
