package orchestrate

// A collection's plan, and the run that carries it out
// (docs/design/knowledge-one-curation.md).
//
// A collection is filled and kept current the same way: by its plan. The plan
// is kept on the collection (Collection.SearchTerms, FilterRules, DocsPerRun):
// the web searches a run makes, what to prefer and skip, and the most
// documents one run adds. It is drafted from the description, edited by the
// owner, and the same whether it runs once (Run now) or on a schedule.
//
// A run fetches the articles the owner listed that are not in yet, then the
// links agents reading the collection suggested, then the search terms; every
// page but the owner's own picks goes through the check (collection_vet.go)
// and, for a secondary page, the second source. It is started by /curate
// (collection_curate.go) and by a scheduled refresh (collection_refresh.go).
//
//	GET  /api/collections/{id}/plan                 the plan's values
//	POST /api/collections/{id}/plan                 save the plan as edited
//	GET  /api/collections/{id}/plan/draft           the Draft a plan row
//	POST /api/collections/{id}/plan/draft           draft it, in the background
//	GET  /api/collections/{id}/plan/draft/progress  {"progress"} or {"outcome"}
//	POST /api/collections/{id}/plan/draft/stop      stop it

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// planSearchReady reports whether the plan's search terms can be run or
// drafted. A variable so a test can stand in for the deployment's search.
var planSearchReady = WebSearchAvailable

// planDocsDefault is the documents a run adds when the plan does not say.
const planDocsDefault = 20

func planKey(id string) string { return "plan:" + id }

// planDocs is the most documents one run of c's plan adds.
func planDocs(c Collection) int {
	n := c.DocsPerRun
	if n <= 0 {
		n = planDocsDefault
	}
	if max := autofillMaxDocs(); n > max {
		n = max
	}
	return n
}

// hasPlan reports whether a run of c has anything to do.
func hasPlan(c Collection) bool {
	return len(c.SearchTerms) > 0 || len(pendingArticles(c)) > 0
}

// planSize is what the plan comes to, in the words the page shows.
func planSize(c Collection) string {
	if len(c.SearchTerms) == 0 {
		return "No search terms yet: Draft a plan writes them from the description."
	}
	return fmt.Sprintf("%d search term%s, up to %d document%s a run. A run stops at %s.",
		len(c.SearchTerms), plural(len(c.SearchTerms)), planDocs(c), plural(planDocs(c)), curateLimit(autofillTimeout()))
}

// draftPlan writes the search terms for c from its description and rules and
// keeps them on the collection, in udb's home for it.
func (T *OrchestrateApp) draftPlan(ctx context.Context, udb Database, c Collection, note func(string)) (string, error) {
	note("writing the search terms")
	queries := normalizeQueries(collectionQueriesFor(ctx, T, c, queriesForMaxDocs(planDocs(c))))
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(queries) == 0 {
		if T.LLM == nil {
			return "", fmt.Errorf("no model is set up to write the search terms")
		}
		return "", fmt.Errorf("no search terms could be written for it: the model did not answer, or the description is too thin to search from")
	}
	home := T.collectionHome(udb, c)
	fresh, ok := loadCollection(home, chFirst(c.Owner, ""), c.ID)
	if !ok {
		return "", fmt.Errorf("the collection is gone")
	}
	fresh.SearchTerms = queries
	if fresh.DocsPerRun <= 0 {
		fresh.DocsPerRun = planDocs(fresh)
	}
	saveCollection(home, fresh)
	return "Plan ready: " + planSize(fresh) + " Check it below; Run now carries it out.", nil
}

// startDraftPlan drafts c's plan in the background; false when a draft is
// already running.
func (T *OrchestrateApp) startDraftPlan(udb Database, c Collection) bool {
	_, started := curations.start(planKey(c.ID), "Drafting a plan", 0, nil, func(ctx context.Context, note func(string)) (string, error) {
		return T.draftPlan(ctx, udb, c, note)
	})
	return started
}

// runPlan carries out c's plan once, saving to udb's home for it, and says
// what it held, what it proposes removing and what it could not use. What it
// added the run's own line counts.
func (T *OrchestrateApp) runPlan(ctx context.Context, udb Database, c Collection, note func(string)) (string, error) {
	home := T.collectionHome(udb, c)
	budget := planDocs(c)
	var held, failed int
	var proposed, turnedAway []string

	// The articles the owner listed: their own picks, so not checked.
	added := 0
	if len(pendingArticles(c)) > 0 {
		note("fetching the articles you listed")
		n, items := T.fetchArticles(ctx, home, &c, budget)
		added += n
		for _, it := range items {
			if it.Status != "added" && it.Status != "already" {
				failed++
			}
		}
	}

	// Links agents reading the collection suggested: checked like any page a
	// machine picked, then closed. One without a link waits for the owner.
	for _, s := range T.listSuggestions(c) {
		if added >= budget || ctx.Err() != nil {
			break
		}
		if !isArticleLink(s.Source) {
			continue
		}
		if collectionHasURL(c, s.Source) {
			T.dropSuggestion(c, s.ID)
			continue
		}
		note("checking a link an agent suggested")
		name, text, raw, mime, err := ingestFetch(ctx, s.Source)
		if err != nil {
			continue // left for the owner, who can see why on the page
		}
		cand := vetCandidate{URL: s.Source, Title: name, Text: text}
		vctx, cancel := context.WithTimeout(ctx, vetTimeout)
		res := T.checkPage(vctx, c, cand, nil)
		cancel()
		switch {
		case res.addsIt():
			T.ingestAutofillDoc(ctx, c, name, text, raw, mime)
			T.recordIngest(home, &c, s.Source, "")
			added++
			if res.Answer == vetSupersedes {
				proposed = append(proposed, res.AgainstTitle)
			}
		case heldFor(res) != "" && T.holdPage(c, cand, res) == nil:
			held++
		default:
			turnedAway = append(turnedAway, chFirst(name, s.Source)+" ("+res.note()+")")
		}
		T.dropSuggestion(c, s.ID)
	}

	// The search terms.
	if len(c.SearchTerms) > 0 && added < budget && ctx.Err() == nil {
		pass := &autofillPass{ctx: ctx, udb: home, c: &c, maxDocs: budget - added, progress: note}
		pass.search(T, c.SearchTerms)
		held += pass.held
		failed += pass.failed
		proposed = append(proposed, pass.proposed...)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var said []string
	if held > 0 {
		verb := "are"
		if held == 1 {
			verb = "is"
		}
		said = append(said, fmt.Sprintf("%d page%s did not pass the check and %s held for you below.", held, plural(held), verb))
	}
	if len(proposed) > 0 {
		said = append(said, "Newer versions were found of "+quoteList(proposed)+": consider removing the old.")
	}
	if len(turnedAway) > 0 {
		said = append(said, "Suggested links turned away: "+strings.Join(turnedAway, "; ")+".")
	}
	if failed > 0 {
		said = append(said, fmt.Sprintf("%d page%s could not be used.", failed, plural(failed)))
	}
	Log("[orchestrate.plan] collection=%s added=%d held=%d failed=%d proposed=%d", c.ID, added, held, failed, len(proposed))
	return strings.Join(said, " "), nil
}

// quoteList is names quoted and joined: "A", "B" and "C".
func quoteList(names []string) string {
	q := make([]string, 0, len(names))
	for _, n := range names {
		q = append(q, strconv.Quote(n))
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

// --- The page ----------------------------------------------------------------

func (T *OrchestrateApp) collectionPlan(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"search_terms": strings.Join(c.SearchTerms, "\n"),
		"filter_rules": c.FilterRules,
		"docs_per_run": planDocs(c),
	}, nil
}

// planLines reads an edited list: one entry per line, or a list.
func planLines(raw json.RawMessage) []string {
	var out []string
	for _, s := range decodeWizardPicks(raw) {
		for _, ln := range strings.Split(s, "\n") {
			if ln = strings.TrimSpace(ln); ln != "" {
				out = append(out, ln)
			}
		}
	}
	return out
}

// collectionPlanSave keeps the plan as edited. The form sends it whole.
func (T *OrchestrateApp) collectionPlanSave(call *jsonapi.Call, body struct {
	SearchTerms json.RawMessage `json:"search_terms"`
	FilterRules json.RawMessage `json:"filter_rules"`
	DocsPerRun  json.RawMessage `json:"docs_per_run"`
}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	terms := normalizeQueries(planLines(body.SearchTerms))
	if max := autofillMaxQueries(); len(terms) > max {
		return nil, jsonapi.BadRequest(fmt.Sprintf("at most %d search terms (%d given)", max, len(terms)))
	}
	docs, _ := strconv.Atoi(strings.Trim(strings.TrimSpace(string(body.DocsPerRun)), `"`))
	if docs <= 0 {
		docs = planDocsDefault
	}
	if max := autofillMaxDocs(); docs > max {
		docs = max
	}
	c.SearchTerms, c.DocsPerRun = terms, docs
	if body.FilterRules != nil {
		c.FilterRules = strings.Join(planLines(body.FilterRules), "\n")
	}
	saveCollection(T.collectionHome(call.UDB, c), c)
	return map[string]any{"ok": true, "size": planSize(c)}, nil
}

func (T *OrchestrateApp) collectionPlanDraftItems(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if !planSearchReady() {
		return []map[string]any{}, nil
	}
	item, desc := "Draft a plan", "Writes search terms from this collection's description and rules, for you to check and edit below before anything runs."
	if len(c.SearchTerms) > 0 {
		item, desc = "Redraft", "Writes the search terms again from the description and rules, in place of the ones below."
	}
	return []map[string]any{{"item": item, "desc": desc, "history": planSize(c)}}, nil
}

func (T *OrchestrateApp) collectionPlanDraftStart(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if !planSearchReady() {
		return nil, jsonapi.BadRequest("web search is not configured for this deployment")
	}
	if !T.startDraftPlan(call.UDB, c) {
		return map[string]any{"running": true, "message": "Already drafting this collection's plan"}, nil
	}
	return map[string]any{"running": true, "message": "Drafting a plan for " + c.Name}, nil
}

func (T *OrchestrateApp) collectionPlanDraftProgress(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(planKey(c.ID)); ok {
		return run.status(), nil
	}
	return map[string]any{}, nil
}

func (T *OrchestrateApp) collectionPlanDraftStop(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(planKey(c.ID)); ok {
		run.stop()
	}
	return map[string]any{"ok": true}, nil
}
