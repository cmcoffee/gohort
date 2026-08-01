// Persisted, streamable pipeline RUNS — the half of "a pipeline can be an app"
// that has nothing to do with agents.
//
// pipeline_interp.go executes a PipelineDef and emits typed events. This turns
// one execution into something a person can watch and come back to: a stored
// transcript, a list of past runs, and the SSE protocol core/ui's PipelinePanel
// already speaks. Store a run, stream a run, list them — no concept of an agent
// anywhere in it.
//
// It lived in apps/orchestrate first, because that is where the first caller
// was. The cost showed up the moment a second host wanted a run surface: the
// custom-app host had to import the agent runtime to get a store and an SSE
// bridge. Agent STAGES still belong to that layer and always will — they arrive
// here as the PipelineDispatch hook the interpreter already defined, which is
// the same seam, one level up.
//
// The host supplies its own Database. Runs stay exactly where that host has
// always kept them; this moved the code, not the data.
package core

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
)

// PipelineRunsTable stores one PipelineRun per execution, keyed
// <pipelineID>:<runID> so a pipeline's history is a prefix scan.
const PipelineRunsTable = "pipeline_runs"

// defaultPipelineRunTimeout bounds a run whose host didn't say. Generous: a
// research pipeline that fans out over a dozen sub-questions is legitimately
// slow, and the failure mode of a short ceiling is a transcript that stops
// mid-sentence with no error anyone can act on.
const defaultPipelineRunTimeout = 30 * time.Minute

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

// PipelineSessionRow is one row of the panel's sidebar list — part of the wire
// contract, so it is a named type a test can pin rather than an anonymous
// struct three layers down inside a handler.
type PipelineSessionRow struct {
	ID    string    `json:"ID"`
	Title string    `json:"Title"`
	Date  time.Time `json:"Date"`
}

// PipelineRunKey is the storage key for one run. Exported because the key
// FORMAT is the contract that makes ListPipelineRuns a prefix scan.
func PipelineRunKey(pipelineID, runID string) string { return pipelineID + ":" + runID }

// SavePipelineRun writes one run into the host's store, scoped to user.
func SavePipelineRun(db Database, user string, run PipelineRun) {
	UserDB(db, user).Set(PipelineRunsTable, PipelineRunKey(run.PipelineID, run.ID), run)
}

// LoadPipelineRun reads one stored run.
func LoadPipelineRun(db Database, user, pipelineID, runID string) (PipelineRun, bool) {
	var run PipelineRun
	ok := UserDB(db, user).Get(PipelineRunsTable, PipelineRunKey(pipelineID, runID), &run)
	return run, ok
}

// DeletePipelineRun drops one stored run.
func DeletePipelineRun(db Database, user, pipelineID, runID string) {
	UserDB(db, user).Unset(PipelineRunsTable, PipelineRunKey(pipelineID, runID))
}

// ListPipelineRuns returns a pipeline's runs, newest first.
func ListPipelineRuns(db Database, user, pipelineID string) []PipelineRun {
	udb := UserDB(db, user)
	prefix := pipelineID + ":"
	var out []PipelineRun
	for _, k := range udb.Keys(PipelineRunsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var run PipelineRun
		if udb.Get(PipelineRunsTable, k, &run) {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out
}

// LatestPipelineRun returns a user's most recent FINISHED run of a pipeline.
//
// One still in flight is skipped: half a transcript handed to something that
// wanted a result is worse than nothing, because it reads as a complete one.
func LatestPipelineRun(db Database, user, pipelineID string) (PipelineRun, bool) {
	if db == nil || user == "" || pipelineID == "" {
		return PipelineRun{}, false
	}
	for _, run := range ListPipelineRuns(db, user, pipelineID) { // newest first
		if !run.Running {
			return run, true
		}
	}
	return PipelineRun{}, false
}

// PipelineRunTitle trims an input into a sidebar label. The whole question is
// the best title a generic surface can produce — it is what the run is about,
// and asking a model to name it would cost a call per run for a label.
func PipelineRunTitle(input string) string {
	title := strings.Join(strings.Fields(input), " ")
	if len(title) > 90 {
		title = title[:90] + "…"
	}
	return title
}

// PipelineRunInput picks the run's input out of the submitted form body.
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
func PipelineRunInput(body []byte) string {
	input, _ := PipelineRunSubmission(body)
	return input
}

// PipelineRunSubmission reads a submitted form into the run's input and its
// RUN-SCOPED template values.
//
// Every field becomes {name} in the pipeline's prompts — including whichever
// one was taken as the input, so {topic} works alongside {input}. That is the
// difference between a pipeline app that takes a question and one that takes
// PARAMETERS: a debate wants a proposition and two sides, and asking for those
// in three boxes only to drop two of them on the floor produced prompts
// templating {side_a} against a value that never arrived.
//
// Non-strings come through as text via the same renderer declared output fields
// use, so a toggle reads "true" and a count reads "3" rather than "3.0".
// Reserved tokens are skipped: a field named "input" cannot redefine {input}.
func PipelineRunSubmission(body []byte) (string, map[string]string) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil { // opening '{'
		return "", nil
	}
	var first string
	byName := map[string]string{}
	vars := map[string]string{}
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
		lower := strings.ToLower(strings.TrimSpace(key))
		if text := strings.TrimSpace(renderFieldValue(val)); text != "" && !reservedTemplateVars[lower] {
			vars["{"+key+"}"] = text
		}
		s, ok := val.(string)
		if !ok {
			continue // a toggle or a number is never the question being asked
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		byName[lower] = s
		if first == "" {
			first = s
		}
	}
	if len(vars) == 0 {
		vars = nil
	}
	for _, s := range []string{byName["input"], byName["topic"], first} {
		if s != "" {
			return s, vars
		}
	}
	return "", vars
}

// PipelineRunSurface is everything a host supplies to serve the PipelinePanel
// protocol for one pipeline.
//
// The two fields that carry the layering: Dispatch is how an agent stage
// reaches whatever runs agents (nil is fine — a pipeline with no agent stages
// needs no agent runtime), and Tools is the catalog a stage's declared tool
// names resolve against. Core decides neither; it just runs what it is handed.
type PipelineRunSurface struct {
	DB       Database         // the host's store — runs live where that host keeps them
	User     string           // whose history this is
	Def      PipelineDef      // what to run
	Dispatch PipelineDispatch // agent stages; nil = none available
	Tools    []AgentToolDef   // catalog a stage's declared tool names resolve against
	Timeout  time.Duration    // 0 = defaultPipelineRunTimeout
}

// ServePipelineRuns serves the panel's protocol. sub is the path after the
// host's own prefix: "stream" | "sessions" | "sessions/<id>".
//
//	POST   stream        → run it, streaming the transcript
//	GET    sessions      → past runs, newest first
//	GET    sessions/<id> → one run's stored blocks
//	DELETE sessions/<id> → drop a run
//
// The host owns authentication and routing; by the time this is called, User is
// already decided.
func (T *AppCore) ServePipelineRuns(w http.ResponseWriter, r *http.Request, s PipelineRunSurface, sub string) {
	switch {
	case sub == "stream":
		T.streamPipelineRun(w, r, s)
	case sub == "sessions":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// CAPITALIZED, because three places have to agree and the other two
		// already did: PipelinePanel reads session_id_field / _title_ / _date_
		// defaulting to "ID" / "Title" / "Date", and the single-run response
		// below answers with exactly those. This list alone once emitted
		// lowercase, so every row arrived with an undefined id, title and date —
		// a sidebar of blank entries that could not be clicked, which reads as
		// "it never saved my runs" when every run was stored correctly.
		runs := ListPipelineRuns(s.DB, s.User, s.Def.ID)
		out := make([]PipelineSessionRow, 0, len(runs))
		for _, run := range runs {
			out = append(out, PipelineSessionRow{ID: run.ID, Title: run.Title, Date: run.Date})
		}
		writePipelineJSON(w, out)
	case strings.HasPrefix(sub, "sessions/"):
		runID := strings.TrimPrefix(sub, "sessions/")
		if runID == "" || strings.Contains(runID, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			DeletePipelineRun(s.DB, s.User, s.Def.ID, runID)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		run, ok := LoadPipelineRun(s.DB, s.User, s.Def.ID, runID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writePipelineJSON(w, map[string]any{
			"ID": run.ID, "Title": run.Title, "Date": run.Date,
			"Blocks": run.Blocks, "Output": run.Output, "Error": run.Err,
		})
	default:
		http.NotFound(w, r)
	}
}

// streamPipelineRun runs a pipeline and streams its transcript in the shape
// PipelinePanel speaks. The run is recorded as it goes, so closing the tab
// loses the live view but not the result.
func (T *AppCore) streamPipelineRun(w http.ResponseWriter, r *http.Request, s PipelineRunSurface) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	input, vars := PipelineRunSubmission(body)
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
		PipelineID: s.Def.ID,
		Title:      PipelineRunTitle(input),
		Date:       time.Now(),
		Running:    true,
	}
	SavePipelineRun(s.DB, s.User, run)
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
			SavePipelineRun(s.DB, s.User, run)
		}
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultPipelineRunTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// executePipelineDefVars, not the exported RunPipelineDefSyncWithSink: the
	// form's fields are run-scoped template values, and this is the only entry
	// point that carries them. Same package, so no public surface grows for it.
	out, runErr := T.executePipelineDefVars(ctx, s.Def, input, vars, s.Dispatch, sink, s.Tools)

	mu.Lock()
	run.Running = false
	run.Output = out
	if runErr != nil {
		run.Err = runErr.Error()
	}
	SavePipelineRun(s.DB, s.User, run)
	mu.Unlock()

	if runErr != nil {
		_ = sse.SendNamed("error", map[string]any{"message": runErr.Error()})
		return
	}
	_ = sse.SendNamed("done", map[string]any{"id": run.ID})
}

func writePipelineJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
