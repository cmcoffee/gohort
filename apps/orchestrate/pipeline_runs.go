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
	"context"
	"encoding/json"
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

// handlePipelineSessions serves the panel's sidebar list.
func (T *OrchestrateApp) handlePipelineSessions(w http.ResponseWriter, r *http.Request, user, pipelineID string) {
	type row struct {
		ID    string    `json:"id"`
		Title string    `json:"title"`
		Date  time.Time `json:"date"`
	}
	runs := T.listPipelineRuns(user, pipelineID)
	out := make([]row, 0, len(runs))
	for _, run := range runs {
		out = append(out, row{ID: run.ID, Title: run.Title, Date: run.Date})
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
	var body struct {
		Input string `json:"input"`
		Topic string `json:"topic"` // the panel's first field is often named topic/question
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	input := strings.TrimSpace(firstNonEmptyStr(body.Input, body.Topic))
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

	out, runErr := T.RunPipelineDefSyncWithSink(ctx, def, input, dispatch, sink, nil)

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
