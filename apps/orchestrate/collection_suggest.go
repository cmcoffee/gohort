package orchestrate

// Suggested sources: candidates found for a collection, for its owner to tick
// (docs/design/knowledge-curation.md, "Suggest sources, I'll pick").
//
// Auto-fill decides for the person: it searches and takes what comes back. This
// shows them what is out there and lets them choose. The search is Auto-fill's
// own (searches written from the description, the same source-quality filter,
// nothing already in the collection), plus the collection's rules: a site its
// owner said to skip is left out, and one they prefer comes first. Each
// candidate is a title, its site, and the line the search showed from it.
//
// What is ticked goes on the collection's Articles to fetch, the list the
// owner keeps of documents it must hold, and is fetched from there, so the
// page shows which ones are in and a later run does not fetch them twice.
//
//	GET  /api/collections/{id}/suggest           the Find sources row
//	POST /api/collections/{id}/suggest           search, in the background
//	GET  /api/collections/{id}/suggest/progress  {"progress"} or {"outcome"}, for finding and for adding
//	POST /api/collections/{id}/suggest/stop      stop it
//	GET  /api/collections/{id}/suggest/fields    the candidates, as a checklist
//	POST /api/collections/{id}/suggest/add       fetch the ticked ones, in the background
//
// The candidates are kept in memory, not on the collection: they are a
// search's results waiting for a decision, and a restart costs only searching
// again. A run is one per collection at a time, finding or adding.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// suggestQueries is how many searches one Find sources run makes.
const suggestQueries = 6

// maxSuggestedSources caps what is offered: enough to choose from, few enough
// to read down.
const maxSuggestedSources = 30

// suggestedKept is how long a search's candidates wait to be ticked.
const suggestedKept = 24 * time.Hour

// collectionWebSearch and collectionQueriesFor are the web search and the searches
// written from a collection. Variables so a test can stand in for both.
var collectionWebSearch = WebSearch
var collectionQueriesFor = func(ctx context.Context, T *OrchestrateApp, c Collection, n int) []string {
	return generateAutofillQueries(ctx, T, c, n)
}

// suggestedSource is one candidate as the page shows it.
type suggestedSource struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Site  string `json:"site"`
	Line  string `json:"line"`
}

type suggestionSet struct {
	found   time.Time
	sources []suggestedSource
}

var suggested = struct {
	sync.Mutex
	m map[string]suggestionSet
}{m: map[string]suggestionSet{}}

// suggestionsFor is the collection's waiting candidates, less any that have
// gone into it since the search.
func suggestionsFor(c Collection) (suggestionSet, bool) {
	suggested.Lock()
	set, ok := suggested.m[c.ID]
	if ok && time.Since(set.found) > suggestedKept {
		delete(suggested.m, c.ID)
		ok = false
	}
	suggested.Unlock()
	if !ok {
		return suggestionSet{}, false
	}
	have := collectionAddresses(c)
	var left []suggestedSource
	for _, s := range set.sources {
		if !have[normalizeIngestURL(s.URL)] {
			left = append(left, s)
		}
	}
	set.sources = left
	return set, true
}

// collectionAddresses is every address already in the collection or on its
// list to fetch, normalised.
func collectionAddresses(c Collection) map[string]bool {
	have := map[string]bool{}
	for _, u := range c.IngestedURLs {
		have[normalizeIngestURL(u)] = true
	}
	for _, a := range c.Articles {
		if isArticleLink(a) {
			have[normalizeIngestURL(a)] = true
		}
	}
	return have
}

// ruleSites reads the sites a collection's rules prefer and skip, from rule
// lines that begin "Prefer " or "Skip " (what the guided setup writes, and
// what a person writes by hand).
func ruleSites(rules string) (prefer, skip []string) {
	for _, ln := range strings.Split(rules, "\n") {
		ln = strings.TrimSpace(ln)
		low := strings.ToLower(ln)
		switch {
		case strings.HasPrefix(low, "prefer "):
			prefer = append(prefer, strings.TrimSpace(low[len("prefer "):]))
		case strings.HasPrefix(low, "skip "):
			skip = append(skip, strings.TrimSpace(low[len("skip "):]))
		}
	}
	return prefer, skip
}

// siteMatches reports whether the address is on one of the sites named.
func siteMatches(addr string, sites []string) bool {
	host := strings.ToLower(siteOf(addr))
	for _, s := range sites {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		s = strings.TrimPrefix(strings.TrimSuffix(s, "/"), "www.")
		if s != "" && (host == s || strings.HasSuffix(host, "."+s)) {
			return true
		}
	}
	return false
}

// siteOf is an address's host, without "www.".
func siteOf(addr string) string {
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// findSources searches for candidates and keeps them for the page, reporting
// how far it has got through note. It returns the outcome line.
func (T *OrchestrateApp) findSources(ctx context.Context, c Collection, note func(string)) (string, error) {
	note("writing searches")
	queries := collectionQueriesFor(ctx, T, c, suggestQueries)
	if len(queries) > suggestQueries {
		queries = queries[:suggestQueries]
	}
	if len(queries) == 0 {
		if T.LLM == nil {
			return "", fmt.Errorf("no model is set up to write the searches")
		}
		return "", fmt.Errorf("no searches could be written for it: the model did not answer, or the description is too thin to search from")
	}
	prefer, skip := ruleSites(c.FilterRules)
	have := collectionAddresses(c)
	seen := map[string]bool{}
	var found []suggestedSource
	for i, q := range queries {
		if ctx.Err() != nil {
			break
		}
		note(fmt.Sprintf("%d of %d searches, %d found", i, len(queries), len(found)))
		for _, cand := range parseWebSearchResults(collectionWebSearch(q)) {
			nu := normalizeIngestURL(cand.URL)
			if have[nu] || seen[nu] || !looksLikeUsefulSourceURL(cand.URL) || IsWeakSource(cand.URL) || siteMatches(cand.URL, skip) {
				continue
			}
			seen[nu] = true
			found = append(found, suggestedSource{URL: cand.URL, Title: strings.TrimSpace(cand.Title), Site: siteOf(cand.URL), Line: curateCut(cand.Snippet, 220)})
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Preferred sites first, then what extracts cleanly (documents before
	// pages), keeping the search's own order otherwise.
	sort.SliceStable(found, func(i, j int) bool {
		pi, pj := siteMatches(found[i].URL, prefer), siteMatches(found[j].URL, prefer)
		if pi != pj {
			return pi
		}
		return urlExtractScore(found[i].URL) > urlExtractScore(found[j].URL)
	})
	if len(found) > maxSuggestedSources {
		found = found[:maxSuggestedSources]
	}
	suggested.Lock()
	suggested.m[c.ID] = suggestionSet{found: time.Now(), sources: found}
	suggested.Unlock()
	if len(found) == 0 {
		return "Found nothing new to suggest. A fuller description, or different sources to prefer, gives the searches more to go on.", nil
	}
	return fmt.Sprintf("Found %d source%s: tick the ones to add below.", len(found), plural(len(found))), nil
}

// startFindSources starts a search for the collection unless one of its runs
// is going.
func (T *OrchestrateApp) startFindSources(c Collection) bool {
	_, started := curations.start(suggestKey(c.ID), "Finding sources", 0, nil, func(ctx context.Context, note func(string)) (string, error) {
		return T.findSources(ctx, c, note)
	})
	return started
}

func suggestKey(id string) string { return "suggest:" + id }

// suggestAgo is how long ago, in words: "just now", "5 minutes ago".
func suggestAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d / time.Minute)
		return fmt.Sprintf("%d minute%s ago", m, plural(m))
	default:
		h := int(d / time.Hour)
		return fmt.Sprintf("%d hour%s ago", h, plural(h))
	}
}

// collectionSuggestItems is the Find sources row, or none where there is no
// web search to find with.
func (T *OrchestrateApp) collectionSuggestItems(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if !WebSearchAvailable() {
		return []map[string]any{}, nil
	}
	history := "Not searched yet."
	if set, ok := suggestionsFor(c); ok {
		history = fmt.Sprintf("Last searched %s: %d waiting to be ticked.", suggestAgo(time.Since(set.found)), len(set.sources))
	}
	return []map[string]any{{
		"item":    "Find sources",
		"desc":    "Searches the web from this collection's description and rules, and lists what it finds below, each with its site and a line from it, for you to tick.",
		"history": history,
	}}, nil
}

// collectionSuggestStart starts a search.
func (T *OrchestrateApp) collectionSuggestStart(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if !WebSearchAvailable() {
		return nil, jsonapi.BadRequest("web search is not configured for this deployment")
	}
	if !T.startFindSources(c) {
		return map[string]any{"running": true, "message": "Already working on this collection's sources"}, nil
	}
	return map[string]any{"running": true, "message": "Finding sources for " + c.Name}, nil
}

func (T *OrchestrateApp) collectionSuggestProgress(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(suggestKey(c.ID)); ok {
		return run.status(), nil
	}
	return map[string]any{}, nil
}

func (T *OrchestrateApp) collectionSuggestStop(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(suggestKey(c.ID)); ok {
		run.stop()
	}
	return map[string]any{"ok": true}, nil
}

// collectionSuggestFields is the candidates as a checklist, or a line saying
// there are none yet.
func (T *OrchestrateApp) collectionSuggestFields(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	set, ok := suggestionsFor(c)
	if !ok || len(set.sources) == 0 {
		return []ui.FormField{{Field: "nothing", Type: "header", Label: "Nothing to choose from yet",
			Help: "Find sources searches for some. What it finds is listed here, and stays until you add it or search again."}}, nil
	}
	opts := make([]ui.SelectOption, 0, len(set.sources))
	for _, s := range set.sources {
		help := s.Site
		if s.Line != "" {
			help += ": " + s.Line
		}
		opts = append(opts, ui.SelectOption{Value: s.URL, Label: chFirst(s.Title, s.Site, s.URL), Help: help})
	}
	return []ui.FormField{{
		Field: "pick", Type: "checklist", Options: opts,
		Label: fmt.Sprintf("%d found %s", len(set.sources), suggestAgo(time.Since(set.found))),
		Help:  "Tick the ones to add. Each is fetched into the collection and listed under Articles to fetch.",
	}}, nil
}

// collectionSuggestAdd puts the ticked candidates on the collection's list to
// fetch and fetches them, in the background. Only addresses the search
// offered are taken: this is not a way to add any link.
func (T *OrchestrateApp) collectionSuggestAdd(call *jsonapi.Call, body struct {
	Pick json.RawMessage `json:"pick"`
}) (any, error) {
	c, err := T.collectionAt(call, "autofill")
	if err != nil {
		return nil, err
	}
	set, _ := suggestionsFor(c)
	offered := map[string]bool{}
	for _, s := range set.sources {
		offered[s.URL] = true
	}
	var picked []string
	for _, u := range decodeWizardPicks(body.Pick) {
		if offered[strings.TrimSpace(u)] {
			picked = append(picked, strings.TrimSpace(u))
		}
	}
	if len(picked) == 0 {
		return nil, jsonapi.BadRequest("tick at least one of the sources found")
	}
	if run, ok := curations.get(suggestKey(c.ID)); ok {
		if st := run.status(); st["progress"] != nil {
			return nil, jsonapi.BadRequest("wait for the search to finish, then tick what to add")
		}
	}
	list, nerr := normalizeArticles(append(append([]string{}, c.Articles...), picked...))
	if nerr != nil {
		return nil, jsonapi.BadRequest(nerr.Error())
	}
	c.Articles = list
	udb := call.UDB
	// The owner's store, whoever ticked: a contributor's own store is not
	// where the collection lives.
	saveCollection(T.collectionHome(udb, c), c)
	before, _ := collectionStats(VectorDB, c.ID)
	count := func() int { n, _ := collectionStats(VectorDB, c.ID); return n }
	label := fmt.Sprintf("Adding %d source%s", len(picked), plural(len(picked)))
	curations.start(suggestKey(c.ID), label, before, count, func(ctx context.Context, _ func(string)) (string, error) {
		// Only the ticked ones: older entries still waiting on the list are
		// left for Auto-fill, not fetched ahead of what was just chosen.
		only := c
		only.Articles = picked
		added, items := T.fetchArticles(ctx, udb, &only, len(picked))
		failed := 0
		for _, it := range items {
			if it.Status != "added" && it.Status != "already" {
				failed++
			}
		}
		Log("[orchestrate.suggest] collection=%s added=%d failed=%d of %d ticked", c.ID, added, failed, len(picked))
		if failed > 0 {
			return fmt.Sprintf("%d could not be fetched: Articles to fetch shows which.", failed), nil
		}
		return "", nil
	})
	return map[string]any{"ok": true, "message": label + ". Find sources above shows how it goes."}, nil
}
