// Streaming run surface for any stored PipelineDef — the piece that lets a
// pipeline BE an app instead of only being callable.
//
// PipelinePanel (core/ui) has rendered "submit a question, watch stages stream,
// browse past runs" since debate needed it. What never existed was a server
// half anyone could point at: debate implements the protocol itself
// (private/debate/chat_endpoints.go), research implements it again, and every
// future app of that shape would implement it a third time. Meanwhile
// /api/pipelines/{id}/run was sync JSON — the right execution, no transcript.
//
// So this serves the panel's protocol for ANY PipelineDef the user owns:
//
//	GET    /api/pipelines/{id}/sessions        → past runs, newest first
//	GET    /api/pipelines/{id}/sessions/{sid}  → one run's stored blocks
//	DELETE /api/pipelines/{id}/sessions/{sid}  → drop a run
//	POST   /api/pipelines/{id}/stream          → run, streaming the transcript
//
// The transcript is PERSISTED as it streams rather than only at the end: a
// pipeline is long-running by nature, and a run that only becomes visible on
// completion is one a user cannot check on, compare, or learn a regression
// from. Persisting per block also means a browser that disconnects mid-run
// still finds the finished transcript waiting.
package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// pipelineRunsTable stores one PipelineRun per execution, keyed
// <pipelineID>:<runID> so a pipeline's history is a prefix scan.
const pipelineRunsTable = "pipeline_runs"

// PipelineRunBlock is one stage's card in a stored transcript. Mirrors the
// panel's block shape so a reload renders identically to the live stream.
type PipelineRunBlock struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// PipelineRun is one execution: what was asked, what each stage produced, and
// how it ended.
type PipelineRun struct {
	ID         string             `json:"id"`
	PipelineID string             `json:"pipeline_id"`
	Title      string             `json:"title"` // the input, trimmed — what the run is ABOUT
	Date       time.Time          `json:"date"`
	Blocks     []PipelineRunBlock `json:"blocks"`
	Output     string             `json:"output,omitempty"`
	Err        string             `json:"error,omitempty"`
	// Running marks a run still in flight. A run that never completes (server
	// restart, cancelled request) stays marked rather than silently reading as
	// finished-with-no-output, which would look like the pipeline produced
	// nothing rather than that it was interrupted.
	Running bool `json:"running,omitempty"`
}

func pipelineRunKey(pipelineID, runID string) string { return pipelineID + ":" + runID }

func (T *OrchestrateApp) savePipelineRun(user string, run PipelineRun) {
	UserDB(T.DB, user).Set(pipelineRunsTable, pipelineRunKey(run.PipelineID, run.ID), run)
}

func (T *OrchestrateApp) loadPipelineRun(user, pipelineID, runID string) (PipelineRun, bool) {
	var run PipelineRun
	ok := UserDB(T.DB, user).Get(pipelineRunsTable, pipelineRunKey(pipelineID, runID), &run)
	return run, ok
}

// listPipelineRuns returns a pipeline's runs, newest first.
func (T *OrchestrateApp) listPipelineRuns(user, pipelineID string) []PipelineRun {
	udb := UserDB(T.DB, user)
	prefix := pipelineID + ":"
	var out []PipelineRun
	for _, k := range udb.Keys(pipelineRunsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var run PipelineRun
		if udb.Get(pipelineRunsTable, k, &run) {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out
}

// pipelineSessionRow is one row of the panel's sidebar list.
//
// The keys are CAPITALIZED because three places have to agree on them and the
// other two already were: PipelinePanel reads session_id_field / _title_ /
// _date_ defaulting to "ID" / "Title" / "Date", and handlePipelineSessionOne
// below answers with exactly those. This list alone emitted lowercase, so every
// row came back with an undefined id, title, and date — a sidebar of blank
// entries that could not be clicked, which reads as "it never saved my runs"
// when in fact every run was stored correctly.
type pipelineSessionRow struct {
	ID    string    `json:"ID"`
	Title string    `json:"Title"`
	Date  time.Time `json:"Date"`
}

// handlePipelineSessions serves the panel's sidebar list.
func (T *OrchestrateApp) handlePipelineSessions(w http.ResponseWriter, r *http.Request, user, pipelineID string) {
	runs := T.listPipelineRuns(user, pipelineID)
	out := make([]pipelineSessionRow, 0, len(runs))
	for _, run := range runs {
		out = append(out, pipelineSessionRow{ID: run.ID, Title: run.Title, Date: run.Date})
	}
	writeJSON(w, out)
}

// handlePipelineSessionOne serves one stored transcript, or deletes it.
func (T *OrchestrateApp) handlePipelineSessionOne(w http.ResponseWriter, r *http.Request, user, pipelineID, runID string) {
	if r.Method == http.MethodDelete {
		UserDB(T.DB, user).Unset(pipelineRunsTable, pipelineRunKey(pipelineID, runID))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	run, ok := T.loadPipelineRun(user, pipelineID, runID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"ID": run.ID, "Title": run.Title, "Date": run.Date,
		"Blocks": run.Blocks, "Output": run.Output, "Error": run.Err,
	})
}

// handlePipelineStream runs a pipeline and streams its transcript in the shape
// PipelinePanel speaks. The run is recorded as it goes, so closing the tab
// loses the live view but not the result.
func (T *OrchestrateApp) handlePipelineStream(w http.ResponseWriter, r *http.Request, user string, def PipelineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	input := pipelineRunInput(raw)
	if input == "" {
		http.Error(w, "input required", http.StatusBadRequest)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stopKeepalive := sse.StartKeepalive(20 * time.Second)
	defer stopKeepalive()

	run := PipelineRun{
		ID:         UUIDv4()[:12],
		PipelineID: def.ID,
		Title:      pipelineRunTitle(input),
		Date:       time.Now(),
		Running:    true,
	}
	T.savePipelineRun(user, run)
	_ = sse.SendNamed("session", map[string]any{"id": run.ID, "title": run.Title})

	// One mutex guards both the SSE writer's ordering and the run record:
	// fanout branches emit from parallel goroutines, so blocks would
	// otherwise interleave mid-write and the stored transcript would race.
	var mu sync.Mutex
	blockIdx := map[string]int{}
	sink := func(ev PipelineEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Kind {
		case "status":
			_ = sse.SendNamed("status", map[string]any{"text": ev.Text})
		case "block":
			run.Blocks = append(run.Blocks, PipelineRunBlock{ID: ev.ID, Type: ev.Type, Title: ev.Title})
			blockIdx[ev.ID] = len(run.Blocks) - 1
			_ = sse.SendNamed("block", map[string]any{"id": ev.ID, "type": ev.Type, "title": ev.Title})
		case "chunk":
			if i, ok := blockIdx[ev.ID]; ok {
				run.Blocks[i].Body += ev.Text
			}
			_ = sse.SendNamed("chunk", map[string]any{"id": ev.ID, "text": ev.Text})
		case "block_done":
			_ = sse.SendNamed("block_done", map[string]any{"id": ev.ID})
			// Persist per completed block, not once at the end: a long run
			// should survive the browser going away.
			T.savePipelineRun(user, run)
		}
	}

	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		return T.RunAgentSync(ctx, user, user, agentID, stageInput)
	}
	ctx, cancel := context.WithTimeout(r.Context(), knowledgeIngestTimeout()*8)
	defer cancel()

	out, runErr := T.RunPipelineDefSyncWithSink(ctx, def, input, dispatch, sink, T.pipelineStandaloneTools(ctx, user, def))

	mu.Lock()
	run.Running = false
	run.Output = out
	if runErr != nil {
		run.Err = runErr.Error()
	}
	T.savePipelineRun(user, run)
	mu.Unlock()

	if runErr != nil {
		_ = sse.SendNamed("error", map[string]any{"message": runErr.Error()})
		return
	}
	_ = sse.SendNamed("done", map[string]any{"id": run.ID})
}

// pipelineRunInput picks the run's input out of the submitted form body.
//
// The panel posts exactly what the author declared — {"proposition": "…"} for a
// form whose box is named proposition — and this used to read only "input" and
// "topic". Any other name meant Start returned 400 "input required" against a
// filled-in form, with the app's own field staring back at the user. Naming the
// box is the author's call, not a hidden protocol requirement.
//
// So: input or topic when present (still the documented names), otherwise the
// FIRST field. First in DOCUMENT order, which is the order the panel wrote them
// and therefore the order the author declared them — a Go map would have made
// that a coin flip between two text boxes.
func pipelineRunInput(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil { // opening '{'
		return ""
	}
	var first string
	byName := map[string]string{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := keyTok.(string)
		var val any
		if err := dec.Decode(&val); err != nil {
			break
		}
		s, ok := val.(string)
		if !ok {
			continue // a toggle or a number is never the question being asked
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		byName[strings.ToLower(key)] = s
		if first == "" {
			first = s
		}
	}
	return firstNonEmptyStr(byName["input"], byName["topic"], first)
}

// pipelineStandaloneTools builds the tool catalog for a run with no calling
// agent behind it — a page launching its own pipeline, rather than an agent
// invoking one of its attached ones.
//
// Those two entries used to be very different runs of the same recipe. An
// agent-invoked pipeline inherits that agent's resolved catalog, so a stage
// declaring tools:["web_search"] gets it; a page-launched one inherited nil,
// and resolveStageTools filters the declared names against the inherited pool
// — an empty pool matches nothing. The stage then took runWorkerStage's
// tool-less "cheap path" and answered from the model alone. Nothing failed:
// research came back fluent, sourceless and wrong, and a tool stage reported
// "the caller supplied no tool catalog" for a tool the user plainly owns.
//
// So the DEFINITION supplies the pool here: every name any stage declares
// (including a tool stage's own Tool and the stages nested in a loop Body),
// resolved for this user. That keeps the per-stage contract exactly as
// authored — a stage that declares nothing still gets nothing, deliberately —
// while making a declared name mean the same thing from either entry point.
// Resolution is per-name so one stale name costs its own stage's tool, not the
// whole run's catalog.
func (T *OrchestrateApp) pipelineStandaloneTools(ctx context.Context, user string, def PipelineDef) []AgentToolDef {
	ordered := pipelineDeclaredToolNames(def)
	if len(ordered) == 0 {
		return nil
	}
	// Username + Ctx, mirroring the attached-pipeline path: per-user tools (an
	// OAuth-backed MCP tool, a credentialed fetch) resolve THIS user's token.
	// No live callbacks — there is no conversation to raise a Connect prompt
	// into, so an unauthorized tool fails its stage rather than prompting.
	sess := &ToolSession{Username: user, Ctx: ctx}
	var out []AgentToolDef
	var missing []string
	for _, n := range ordered {
		td, err := GetAgentToolsWithSession(sess, n)
		if err != nil || len(td) == 0 {
			missing = append(missing, n)
			continue
		}
		out = append(out, td[0])
	}
	if len(missing) > 0 {
		Log("[orchestrate.pipelines] run of %q: %d declared tool(s) did not resolve for user=%q: %s",
			def.Name, len(missing), user, strings.Join(missing, ", "))
	}
	return out
}

// pipelineDeclaredToolNames is every tool name a definition asks for, sorted:
// each stage's Tools, a tool stage's own Tool, and the same for the stages
// nested in a loop Body — a loop is where the tool-calling stages of a
// refinement pass usually live, so missing them would leave exactly the
// iterative pipelines tool-less.
//
// Sorted rather than map-ranged so a run's catalog is built in a stable order:
// the same definition should produce the same catalog every time, which is the
// difference between a reproducible run and one that varies by map seed.
func pipelineDeclaredToolNames(def PipelineDef) []string {
	names := map[string]bool{}
	var walk func(stages []PipelineStage)
	walk = func(stages []PipelineStage) {
		for _, s := range stages {
			for _, n := range s.Tools {
				if n = strings.TrimSpace(n); n != "" {
					names[n] = true
				}
			}
			if n := strings.TrimSpace(s.Tool); n != "" {
				names[n] = true
			}
			walk(s.Body)
		}
	}
	walk(def.Stages)
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// pipelineRunTitle trims an input into a sidebar label. The whole question is
// the best title a generic surface can produce — it is what the run is about,
// and asking a model to name it would cost a call per run for a label.
func pipelineRunTitle(input string) string {
	title := strings.Join(strings.Fields(input), " ")
	if len(title) > 90 {
		title = title[:90] + "…"
	}
	return title
}
