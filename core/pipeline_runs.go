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
	"net/url"
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
	// Meta is the run's promoted summary: the stage output fields the def
	// named in SessionMeta, keyed by field name. Filled as the stages that
	// declare them finish, so an interrupted run still carries whatever it had
	// established by the time it stopped.
	Meta map[string]string `json:"meta,omitempty"`
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
	// Meta is the run's promoted summary fields. Marshalled FLAT onto the row
	// (see MarshalJSON), never as a nested object.
	Meta map[string]string `json:"-"`
}

// MarshalJSON writes the row with its promoted fields spread across it.
//
// Flat because that is the shape every reader on the other side already
// expects: SessionMetaField.Field names a key on the row object itself, and so
// do PipelineAction.ShowIfField and the {FieldName} placeholders an action URL
// carries. Nesting them under a container would mean teaching all three about
// it for the benefit of nobody.
//
// The row's own columns are written LAST and win any collision. Validate
// refuses a def that promotes one of them, so this can only fire on a run
// stored before a stage was renamed — and a sidebar whose rows lost their ids
// is a considerably worse failure than one missing a pill.
func (row PipelineSessionRow) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(row.Meta)+3)
	for k, v := range row.Meta {
		out[k] = v
	}
	out["ID"] = row.ID
	out["Title"] = row.Title
	out["Date"] = row.Date
	return json.Marshal(out)
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
	DB       Database              // the host's store — runs live where that host keeps them
	User     string                // whose history this is
	Def      PipelineDef           // what to run
	Dispatch PipelineDispatch      // agent stages; nil = none available
	Machine  PipelineMachineRunner // kind=machine stages; nil = none available
	Tools    []AgentToolDef        // catalog a stage's declared tool names resolve against
	Timeout  time.Duration         // 0 = defaultPipelineRunTimeout
	Live     RunLiveInfo           // where these runs can be watched and stopped; see RunSurface.Live
}

// ServePipelineRuns is ServeRuns with a pipeline as the thing that runs.
func (T *AppCore) ServePipelineRuns(w http.ResponseWriter, r *http.Request, s PipelineRunSurface, sub string) {
	T.ServeRuns(w, r, RunSurface{
		DB: s.DB, User: s.User, OwnerID: s.Def.ID, Timeout: s.Timeout, Live: s.Live,
		Work: func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
			// executePipelineHooks, not an exported Run*: the form's fields are
			// run-scoped template values and this is the only entry point that
			// carries them. Same package, so no public surface grows for it.
			out, _, err := T.executePipelineHooks(ctx, s.Def, input, vars, sink,
				PipelineHooks{Dispatch: s.Dispatch, Machine: s.Machine, Tools: s.Tools})
			return out, err
		},
	}, sub)
}

// RunWork is one execution of whatever the host is running. It reports its
// progress through the sink in PipelineEvents, and returns the final text.
//
// The seam that makes this surface general: a pipeline is one thing that
// runs, and a machine is another. Both produce a transcript of blocks and
// a result, which is all the panel protocol below knows about.
type RunWork func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error)

// RunSurface is the panel protocol's host-supplied half, for any kind of run.
//
// OwnerID keys the history: a pipeline's id, a machine's id, whatever the
// runs belong to. The store does not care which, and calling it anything
// more specific would have meant a second copy of everything below.
type RunSurface struct {
	DB      Database      // the host's store — runs live where that host keeps them
	User    string        // whose history this is
	OwnerID string        // what ran: the id its past runs are listed under
	Work    RunWork       // the run itself
	Timeout time.Duration // 0 = defaultPipelineRunTimeout
	// Live places this surface's runs on the global activity ribbon. Optional:
	// a host that fills nothing still gets its runs LISTED, because a run that
	// outlives its request and appears nowhere is the failure this exists to
	// prevent — it just gets listed without a way back to it.
	Live RunLiveInfo
}

// RunLiveInfo is what a host knows about where its runs live on the web, which
// core cannot work out for itself: the surface is mounted by somebody else,
// under a path it was never told.
type RunLiveInfo struct {
	App       string // app name shown beside the entry
	URL       string // where to send a viewer to watch it; {id} substituted
	CancelURL string // where a POST stops it; {id} substituted
}

// ServeRuns serves the PipelinePanel protocol for any RunSurface. sub is the
// path after the host's own prefix: "stream" | "sessions" | "sessions/<id>".
//
//	POST   stream          → run it, streaming the transcript
//	GET    reconnect/<id>  → attach to a run still going, from the top
//	POST   cancel?id=<id>  → stop one
//	GET    sessions        → past runs, newest first
//	GET    sessions/<id>   → one run's stored blocks
//	DELETE sessions/<id>   → drop a run
//
// The host owns authentication and routing; by the time this is called, User is
// already decided.
func (T *AppCore) ServeRuns(w http.ResponseWriter, r *http.Request, s RunSurface, sub string) {
	switch {
	case sub == "stream":
		T.streamRun(w, r, s)
	case sub == "cancel":
		T.cancelRun(w, r, s)
	case strings.HasPrefix(sub, "reconnect/"):
		T.reconnectRun(w, r, s, strings.TrimPrefix(sub, "reconnect/"))
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
		runs := ListPipelineRuns(s.DB, s.User, s.OwnerID)
		out := make([]PipelineSessionRow, 0, len(runs))
		for _, run := range runs {
			out = append(out, PipelineSessionRow{ID: run.ID, Title: run.Title, Date: run.Date, Meta: run.Meta})
		}
		writePipelineJSON(w, out)
	case strings.HasPrefix(sub, "sessions/"):
		runID := strings.TrimPrefix(sub, "sessions/")
		if runID == "" || strings.Contains(runID, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			DeletePipelineRun(s.DB, s.User, s.OwnerID, runID)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		run, ok := LoadPipelineRun(s.DB, s.User, s.OwnerID, runID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Same flat shape as a row: the panel reads a loaded run through the
		// same field names it reads the sidebar with, so a promoted field that
		// only appeared in the list would show on the row and vanish the
		// moment the run was opened.
		one := map[string]any{}
		for k, v := range run.Meta {
			one[k] = v
		}
		one["ID"] = run.ID
		one["Title"] = run.Title
		one["Date"] = run.Date
		one["Blocks"] = run.Blocks
		one["Output"] = run.Output
		one["Error"] = run.Err
		writePipelineJSON(w, one)
	default:
		http.NotFound(w, r)
	}
}

// --- live runs ---------------------------------------------------------------
//
// A run used to be owned by the request that started it: the work ran inline
// on r.Context(), so closing the tab cancelled it. Every completed stage was
// still persisted, which made the loss quiet — the history showed a run that
// simply stopped, and the reason was somewhere in the reader's browser rather
// than anywhere in the logs.
//
// So the work is detached and the request only WATCHES it. That buys the two
// things a long recipe needs (come back to a run in progress, stop one that is
// going nowhere) and costs one thing worth stating plainly: a run now keeps
// spending on model calls after its reader has gone. That is the point, and it
// is why the same change has to put every run somewhere it can be seen and
// stopped — an invisible run with a budget is strictly worse than one that
// dies with the tab.

// runFrame is one SSE frame, buffered so a client that arrives late is handed
// the same stream from the beginning rather than joining halfway through a
// transcript it cannot make sense of.
type runFrame struct {
	Name string
	Data map[string]any
}

// liveRuns holds every in-flight run of every surface. Keyed by run id, which
// is a UUID slice, so the shared namespace needs no per-host partition — but
// every reader still proves the run is THEIRS against the store before
// touching it, because a shared registry is exactly the shape that turns one
// guessed id into somebody else's transcript.
var liveRuns = NewLiveSessionMap[runFrame](0)

// runLinks remembers where a live run can be watched and stopped. Kept beside
// the registry rather than inside LiveSession so the generic map stays
// ignorant of web paths, which are the host's business.
var (
	runLinkMu sync.Mutex
	runLinks  = map[string]RunLiveInfo{}
)

func init() {
	RegisterLiveProvider(func() []LiveEntry {
		entries := liveRuns.ActiveSessions()
		runLinkMu.Lock()
		defer runLinkMu.Unlock()
		for i := range entries {
			info, ok := runLinks[entries[i].ID]
			if !ok {
				continue
			}
			entries[i].App = info.App
			entries[i].URL = strings.ReplaceAll(info.URL, "{id}", url.QueryEscape(entries[i].ID))
			entries[i].CancelURL = strings.ReplaceAll(info.CancelURL, "{id}", url.QueryEscape(entries[i].ID))
		}
		return entries
	})
}

func setRunLink(id string, info RunLiveInfo) {
	if info == (RunLiveInfo{}) {
		return
	}
	runLinkMu.Lock()
	runLinks[id] = info
	runLinkMu.Unlock()
}

func clearRunLink(id string) {
	runLinkMu.Lock()
	delete(runLinks, id)
	runLinkMu.Unlock()
}

// ownsRun reports whether this surface's user may touch this run id.
//
// The store is the authority, not the live registry: a run is filed under
// (user, pipeline, id), so loading it back proves all three at once. The
// registry could only answer "somebody with this id is running something".
func ownsRun(s RunSurface, runID string) bool {
	if runID == "" || strings.Contains(runID, "/") {
		return false
	}
	_, ok := LoadPipelineRun(s.DB, s.User, s.OwnerID, runID)
	return ok
}

// streamRun starts a run and streams its transcript. The work outlives this
// request; the request is one viewer of it.
func (T *AppCore) streamRun(w http.ResponseWriter, r *http.Request, s RunSurface) {
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

	run := PipelineRun{
		ID:         UUIDv4()[:12],
		PipelineID: s.OwnerID,
		Title:      PipelineRunTitle(input),
		Date:       time.Now(),
		Running:    true,
	}
	SavePipelineRun(s.DB, s.User, run)

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultPipelineRunTimeout
	}
	// context.Background, deliberately: r.Context() dies with the response, and
	// this run is meant to survive it. The timeout is what bounds it now, so it
	// is the only thing standing between a stuck stage and a goroutine that
	// runs until the process does.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	liveRuns.Register(run.ID, run.Title, cancel).SetOwner(s.User)
	setRunLink(run.ID, s.Live)

	emit := func(name string, data map[string]any, done bool) {
		liveRuns.AppendEvent(run.ID, runFrame{Name: name, Data: data}, done)
	}
	emit("session", map[string]any{"id": run.ID, "title": run.Title}, false)

	go func() {
		defer cancel()
		// One mutex over the run record: fanout branches emit from parallel
		// goroutines, so the stored transcript would otherwise race. The SSE
		// ordering it used to also guard is no longer its problem — each
		// viewer writes from its own goroutine, reading a snapshot.
		var mu sync.Mutex
		blockIdx := map[string]int{}
		sink := func(ev PipelineEvent) {
			mu.Lock()
			defer mu.Unlock()
			switch ev.Kind {
			case "status":
				liveRuns.UpdateStatus(run.ID, ev.Text)
				emit("status", map[string]any{"text": ev.Text}, false)
			case "block":
				run.Blocks = append(run.Blocks, PipelineRunBlock{ID: ev.ID, Type: ev.Type, Title: ev.Title})
				blockIdx[ev.ID] = len(run.Blocks) - 1
				emit("block", map[string]any{"id": ev.ID, "type": ev.Type, "title": ev.Title}, false)
			case "chunk":
				if i, ok := blockIdx[ev.ID]; ok {
					run.Blocks[i].Body += ev.Text
				}
				emit("chunk", map[string]any{"id": ev.ID, "text": ev.Text}, false)
			case "meta":
				if run.Meta == nil {
					run.Meta = map[string]string{}
				}
				for k, v := range ev.Meta {
					run.Meta[k] = v
				}
			case "block_done":
				emit("block_done", map[string]any{"id": ev.ID}, false)
				// Persist per completed block, not once at the end: a long run
				// should survive the process going away, not just the browser.
				SavePipelineRun(s.DB, s.User, run)
			}
		}

		out, runErr := s.Work(ctx, input, vars, sink)

		mu.Lock()
		run.Running = false
		run.Output = out
		if runErr != nil {
			run.Err = runErr.Error()
			// A cancel arrives as a context error, which reads to a user as
			// though something broke. It did not; they stopped it.
			if ctx.Err() == context.Canceled {
				run.Err = "stopped"
			}
		}
		SavePipelineRun(s.DB, s.User, run)
		mu.Unlock()

		if runErr != nil {
			emit("error", map[string]any{"message": run.Err}, true)
		} else {
			emit("done", map[string]any{"id": run.ID}, true)
		}
		clearRunLink(run.ID)
		// Kept around briefly so a viewer that reconnects just after the end
		// still gets the transcript rather than a 404 and a blank panel.
		liveRuns.ScheduleCleanup(run.ID)
	}()

	tailRun(w, r, run.ID)
}

// reconnectRun attaches a viewer to a run that is still going.
//
// 404 when it is not live is the CONTRACT, not a failure: the panel reads it
// as "this one has finished" and loads the stored record instead, which is the
// right answer and the reason this needs no way to say "done, look elsewhere".
func (T *AppCore) reconnectRun(w http.ResponseWriter, r *http.Request, s RunSurface, runID string) {
	if !ownsRun(s, runID) {
		http.NotFound(w, r)
		return
	}
	if frames, _ := liveRuns.SnapshotEvents(runID); frames == nil {
		http.NotFound(w, r)
		return
	}
	tailRun(w, r, runID)
}

// cancelRun stops a run. POST, with the id in the query, which is what the
// panel's Cancel button sends.
func (T *AppCore) cancelRun(w http.ResponseWriter, r *http.Request, s RunSurface) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("id"))
	if !ownsRun(s, runID) {
		// 404 rather than 403: whether somebody else is running something is
		// not this viewer's business either.
		http.NotFound(w, r)
		return
	}
	// Not an error when there was nothing to stop. The button fires on a run
	// that may have finished a moment ago, and reporting that as a failure
	// would make a successful outcome look like a broken control.
	liveRuns.CancelSession(runID)
	clearRunLink(runID)
	w.WriteHeader(http.StatusOK)
}

// tailRun streams one run's buffered frames to one viewer, and keeps streaming
// until the run ends or the viewer leaves.
//
// Everything goes through the buffer, including the frames for the client that
// started the run. Writing live to that one and buffering for the others would
// be faster by the poll interval and would mean two paths that have to agree
// forever about a transcript's contents; the events here are per-STAGE, so the
// interval is well under what anybody notices.
//
// Send before sleeping, so a viewer gets everything that already happened at
// once instead of a beat late.
func tailRun(w http.ResponseWriter, r *http.Request, runID string) {
	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stopKeepalive := sse.StartKeepalive(20 * time.Second)
	defer stopKeepalive()

	sent := 0
	for {
		frames, done := liveRuns.SnapshotEvents(runID)
		if frames == nil {
			return // cleaned up under us
		}
		for ; sent < len(frames); sent++ {
			if err := sse.SendNamed(frames[sent].Name, frames[sent].Data); err != nil {
				return // viewer gone; the run carries on
			}
		}
		if done {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(runTailInterval):
		}
	}
}

// runTailInterval is how often a watching viewer looks for new frames. A
// pipeline emits one block per stage rather than a token at a time, so this is
// well inside the gap between two events rather than a throttle on them.
const runTailInterval = 250 * time.Millisecond

func writePipelineJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
