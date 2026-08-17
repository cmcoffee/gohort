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
	if n := len(d.Advice()); n > 0 {
		if out != "" {
			out += " · "
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

// handlePipelines serves the collection-level routes: GET list, POST
// create.
func (T *OrchestrateApp) handlePipelines(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
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
			})
		}
		writeJSON(w, map[string]any{"pipelines": rows})
	case http.MethodPost:
		var def PipelineDef
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
		saved := SavePipelineDef(udb, def)
		Log("[orchestrate.pipelines] user=%q created pipeline %q (id=%s, %d stages)", user, saved.Name, saved.ID, len(saved.Stages))
		writeJSON(w, saved)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	var recipe PipelineDef
	if err := json.NewDecoder(r.Body).Decode(&recipe); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	saved, err := ImportPipeline(udb, user, recipe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[orchestrate.pipelines] user=%q imported pipeline %q (id=%s)", user, saved.Name, saved.ID)
	writeJSON(w, saved)
}

// handlePipelineOne serves per-pipeline routes: GET / PUT / DELETE, plus
// the /export and /run actions.
func (T *OrchestrateApp) handlePipelineOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/pipelines/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	var id, action string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		id = rest[:slash]
		action = rest[slash+1:]
	} else {
		id = rest
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	def, found := LoadPipelineDef(udb, user, id)
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
			var body PipelineDef
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			// Identity stays the server's: keep id/owner from the loaded
			// record, take name/description/stages from the body.
			body.ID = def.ID
			body.Owner = user
			body.Created = def.Created
			if err := body.Validate(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			saved := SavePipelineDef(udb, body)
			Log("[orchestrate.pipelines] user=%q updated pipeline %q (id=%s)", user, saved.Name, saved.ID)
			writeJSON(w, saved)
		case http.MethodDelete:
			DeletePipelineDef(udb, def.ID)
			Log("[orchestrate.pipelines] user=%q deleted pipeline %q (id=%s)", user, def.Name, def.ID)
			writeJSON(w, map[string]any{"deleted": def.ID})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "agents":
		// Assign to agents. An unattached pipeline is a tool no agent
		// has, which the list already says and nothing could change.
		T.handlePipelineAgents(w, r, udb, user, def)
	case "duplicate":
		// Iterating on a working pipeline in place is how the working
		// one stops working. The copy is where an experiment belongs,
		// and landing in it is the reason anybody clicked.
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dup := def
		dup.ID = ""
		dup.Previous = nil
		dup.Global = false
		dup.Name = copyName(def.Name, pipelineNames(ListPipelineDefs(udb, user)))
		saved := SavePipelineDef(udb, dup)
		Log("[orchestrate.pipelines] user=%q duplicated pipeline %q as %q", user, def.Name, saved.Name)
		writeJSON(w, map[string]any{"id": saved.ID, "name": saved.Name})
	case "revise":
		// Say what should change, against the pipeline on screen
		// (pipeline_revise.go). Undoable, because it can rewrite every
		// prompt in it.
		T.handlePipelineRevise(w, r, udb, user, def)
	case "undo":
		T.handlePipelineUndo(w, r, udb, user, def)
	case "stages":
		// The per-stage form (pipeline_editor.go).
		T.handlePipelineStages(w, r, udb, user, def)
	case "export":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		recipe := ExportPipeline(def)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+SnakeFromDisplay(def.Name)+".pipeline.json\"")
		_ = json.NewEncoder(w).Encode(recipe)
	case "run":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		T.runPipelineHTTP(w, r, user, def)
	case "stream", "sessions":
		// The streaming twin of "run": same execution, transcript instead of a
		// lump. This is what lets a PipelineDef be an APP — core serves the
		// protocol (core/pipeline_runs.go); orchestrate supplies the store and
		// the agent-dispatch hook.
		T.handlePipelineRuns(w, r, user, def, action)
	default:
		if sid, ok := strings.CutPrefix(action, "sessions/"); ok && sid != "" && !strings.Contains(sid, "/") {
			T.handlePipelineRuns(w, r, user, def, action)
			return
		}
		http.NotFound(w, r)
	}
}

// runPipelineHTTP executes a pipeline synchronously for the browser and
// returns {output}. Agent stages dispatch through RunAgentSync as the
// requesting user. Sync (no SSE) for the MVP panel — the request blocks
// until the run finishes; a streaming/async run endpoint can layer on
// later if slow pipelines make the blocking call painful.
func (T *OrchestrateApp) runPipelineHTTP(w http.ResponseWriter, r *http.Request, user string, def PipelineDef) {
	var body struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(body.Input)
	if input == "" {
		http.Error(w, "input required", http.StatusBadRequest)
		return
	}
	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		return T.RunAgentSync(ctx, user, user, agentID, stageInput)
	}
	ctx, cancel := context.WithTimeout(r.Context(), knowledgeIngestTimeout()*8)
	defer cancel()
	out, err := T.RunPipelineDefSync(ctx, def, input, dispatch, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"output": out})
}

// writeJSON is the small response helper these handlers share.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
