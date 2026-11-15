package orchestrate

// Fill it for me: a plan, its size, then the run (docs/design/knowledge-
// curation.md, step 4).
//
// Auto-fill decides its searches and runs them in one press, so the cost is
// known only after it is paid. Here the searches it would run are written
// first, from the collection's description and rules, and shown with the
// number of documents to add and what that comes to ("8 searches, up to 20
// documents"). The person edits the list, changes the count, and runs it,
// or does not. The run is Auto-fill's own search and fetch (autofillPass), in
// the background with progress, within Auto-fill's caps.
//
//	GET  /api/collections/{id}/fill           the Draft a plan row
//	POST /api/collections/{id}/fill           draft one, in the background
//	GET  /api/collections/{id}/fill/progress  {"progress"} or {"outcome"}, drafting and running
//	POST /api/collections/{id}/fill/stop      stop it
//	GET  /api/collections/{id}/fill/fields    the plan's fields, or a line saying there is none
//	GET  /api/collections/{id}/fill/plan      the plan's values
//	POST /api/collections/{id}/fill/run       run the plan as edited
//
// A plan is kept in memory, not on the collection: it is a proposal waiting
// for a yes, and a restart costs only drafting it again.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// fillPlanKept is how long a drafted plan waits to be run.
const fillPlanKept = 24 * time.Hour

// fillDocsDefault is the documents a drafted plan proposes, within the cap.
const fillDocsDefault = 20

// fillPlan is the searches to run and how many documents to add.
type fillPlan struct {
	Searches  []string
	Documents int
	drafted   time.Time
}

var fillPlans = struct {
	sync.Mutex
	m map[string]fillPlan
}{m: map[string]fillPlan{}}

func fillKey(id string) string { return "fill:" + id }

func planFor(id string) (fillPlan, bool) {
	fillPlans.Lock()
	defer fillPlans.Unlock()
	p, ok := fillPlans.m[id]
	if ok && time.Since(p.drafted) > fillPlanKept {
		delete(fillPlans.m, id)
		return fillPlan{}, false
	}
	return p, ok
}

// size is what the plan comes to, in the words the page shows.
func (p fillPlan) size() string {
	return fmt.Sprintf("%d search%s, up to %d document%s. A run stops at %s.",
		len(p.Searches), pluralES(len(p.Searches)), p.Documents, plural(p.Documents), curateLimit(autofillTimeout()))
}

func pluralES(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

// draftPlan writes the searches for the collection and keeps the plan.
func (T *OrchestrateApp) draftPlan(ctx context.Context, c Collection, note func(string)) (string, error) {
	docs := fillDocsDefault
	if max := autofillMaxDocs(); docs > max {
		docs = max
	}
	note("writing the searches")
	queries := normalizeQueries(collectionQueriesFor(ctx, T, c, queriesForMaxDocs(docs)))
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(queries) == 0 {
		if T.LLM == nil {
			return "", fmt.Errorf("no model is set up to write the searches")
		}
		return "", fmt.Errorf("no searches could be written for it: the model did not answer, or the description is too thin to search from")
	}
	p := fillPlan{Searches: queries, Documents: docs, drafted: time.Now()}
	fillPlans.Lock()
	fillPlans.m[c.ID] = p
	fillPlans.Unlock()
	return "Plan ready: " + p.size() + " Check it below, then run it.", nil
}

// runPlan is the plan run: Auto-fill's search and fetch over its searches.
func (T *OrchestrateApp) runPlan(ctx context.Context, udb Database, c Collection, p fillPlan, note func(string)) (string, error) {
	pass := &autofillPass{ctx: ctx, udb: udb, c: &c, maxDocs: p.Documents, progress: note}
	pass.search(T, p.Searches)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	Log("[orchestrate.fill] collection=%s searches=%d added=%d failed=%d (max=%d)", c.ID, len(p.Searches), pass.added, pass.failed, p.Documents)
	if pass.failed > 0 {
		return fmt.Sprintf("%d page%s could not be used.", pass.failed, plural(pass.failed)), nil
	}
	return "", nil
}

// fillRunning reports whether a draft or a run is going for the collection.
func fillRunning(id string) bool {
	run, ok := curations.get(fillKey(id))
	return ok && run.status()["progress"] != nil
}

// startDraftPlan starts drafting a plan unless something is going for it.
func (T *OrchestrateApp) startDraftPlan(c Collection) bool {
	_, started := curations.start(fillKey(c.ID), "Drafting a plan", 0, nil, func(ctx context.Context, note func(string)) (string, error) {
		return T.draftPlan(ctx, c, note)
	})
	return started
}

func (T *OrchestrateApp) collectionFillItems(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if !WebSearchAvailable() {
		return []map[string]any{}, nil
	}
	history := "No plan yet."
	if p, ok := planFor(c.ID); ok {
		history = "A plan is waiting below: " + p.size()
	}
	return []map[string]any{{
		"item":    "Draft a plan",
		"desc":    "Writes the searches it would run from this collection's description and rules, and how many documents to add, for you to check before anything runs.",
		"history": history,
	}}, nil
}

func (T *OrchestrateApp) collectionFillStart(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if !WebSearchAvailable() {
		return nil, jsonapi.BadRequest("web search is not configured for this deployment")
	}
	if !T.startDraftPlan(c) {
		return map[string]any{"running": true, "message": "Already working on this collection's plan"}, nil
	}
	return map[string]any{"running": true, "message": "Drafting a plan for " + c.Name}, nil
}

func (T *OrchestrateApp) collectionFillProgress(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(fillKey(c.ID)); ok {
		return run.status(), nil
	}
	return map[string]any{}, nil
}

func (T *OrchestrateApp) collectionFillStop(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(fillKey(c.ID)); ok {
		run.stop()
	}
	return map[string]any{"ok": true}, nil
}

// collectionFillFields is the plan as a form: its size, the searches to edit,
// and the count; or a line saying there is none yet.
func (T *OrchestrateApp) collectionFillFields(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	p, ok := planFor(c.ID)
	if !ok {
		return []ui.FormField{{Field: "nothing", Type: "header", Label: "No plan yet",
			Help: "Draft a plan above. It is shown here, with its size, before anything runs."}}, nil
	}
	return []ui.FormField{
		{Field: "size", Type: "header", Label: "The plan", Help: p.size()},
		{Field: "searches", Type: "rules", Label: "Searches",
			Help: fmt.Sprintf("One web search each. Add, change or remove any; up to %d.", autofillMaxQueries())},
		{Field: "documents", Type: "number", Label: "Documents to add",
			Help: fmt.Sprintf("At most this many are added, up to %d.", autofillMaxDocs())},
	}, nil
}

func (T *OrchestrateApp) collectionFillPlan(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	p, _ := planFor(c.ID)
	return map[string]any{"searches": strings.Join(p.Searches, "\n"), "documents": p.Documents}, nil
}

// planSearches reads the edited list: one search per line, or a list.
func planSearches(raw json.RawMessage) []string {
	var out []string
	for _, s := range decodeWizardPicks(raw) {
		out = append(out, strings.Split(s, "\n")...)
	}
	return normalizeQueries(out)
}

// collectionFillRun runs the plan as edited, in the background.
func (T *OrchestrateApp) collectionFillRun(call *jsonapi.Call, body struct {
	Searches  json.RawMessage `json:"searches"`
	Documents json.RawMessage `json:"documents"`
}) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if fillRunning(c.ID) {
		return nil, jsonapi.BadRequest("wait for what is running to finish")
	}
	searches := planSearches(body.Searches)
	if len(searches) == 0 {
		return nil, jsonapi.BadRequest("the plan has no searches: add at least one")
	}
	if max := autofillMaxQueries(); len(searches) > max {
		return nil, jsonapi.BadRequest(fmt.Sprintf("at most %d searches a run (%d given)", max, len(searches)))
	}
	docs, _ := strconv.Atoi(strings.Trim(strings.TrimSpace(string(body.Documents)), `"`))
	if docs <= 0 {
		return nil, jsonapi.BadRequest("say how many documents to add")
	}
	if max := autofillMaxDocs(); docs > max {
		docs = max
	}
	p := fillPlan{Searches: searches, Documents: docs, drafted: time.Now()}
	fillPlans.Lock()
	fillPlans.m[c.ID] = p
	fillPlans.Unlock()
	udb := call.UDB
	before, _ := collectionStats(VectorDB, c.ID)
	count := func() int { n, _ := collectionStats(VectorDB, c.ID); return n }
	curations.start(fillKey(c.ID), "Filling", before, count, func(ctx context.Context, note func(string)) (string, error) {
		return T.runPlan(ctx, udb, c, p, note)
	})
	return map[string]any{"ok": true, "message": "Running the plan: " + p.size() + " Draft a plan above shows how it goes."}, nil
}
