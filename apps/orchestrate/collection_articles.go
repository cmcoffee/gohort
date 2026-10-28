package orchestrate

// Articles to fetch: the owner names documents a collection must hold.
//
// Auto-fill on its own searches from the collection's name and description and
// takes what comes back. That covers a topic well and is the wrong way to get
// one particular document: this year's edition, the official text, the paper
// somebody recommended. So the owner can list them, each a link or a
// description to search for, and auto-fill fetches those before it searches on
// its own.
//
// A listed article that has been fetched remembers where from
// (Collection.ArticlesFetched), so a later run skips it and the page shows
// which ones are in. Taking an entry off the list leaves its document.

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxCollectionArticles caps the list. It is a list of documents somebody
// picked by hand; a longer one is a topic, which is what auto-fill's own
// searches are for.
const maxCollectionArticles = 50

// maxArticleChars caps one entry: a link, or a title with an edition.
const maxArticleChars = 300

// maxAutofillDocChars is the most extracted text one fetched document may
// have. 200 KB of plain text holds the biggest legitimate single reference
// (a long RFC, a dense API spec); a page past it is almost always infinite
// scroll, a mailing-list digest or a content farm.
const maxAutofillDocChars = 200 * 1024

// articleSearchTries is how many search results a described article tries,
// in order, before it is reported as not found.
const articleSearchTries = 3

// autofillItem is one candidate's outcome in an auto-fill run's summary.
type autofillItem struct {
	Article string `json:"article,omitempty"` // the listed article it was fetched for
	URL     string `json:"url"`
	Status  string `json:"status"`
	Name    string `json:"name,omitempty"`
	Chunks  int    `json:"chunks,omitempty"`
	Err     string `json:"err,omitempty"`
}

// normalizeArticles trims the list, drops blanks and repeats, and refuses one
// past the caps rather than cutting it, so nothing the owner typed is lost
// without being told.
func normalizeArticles(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range in {
		a = strings.Join(strings.Fields(a), " ")
		if a == "" {
			continue
		}
		key := strings.ToLower(a)
		if isArticleLink(a) {
			key = normalizeIngestURL(a)
		}
		if seen[key] {
			continue
		}
		if len([]rune(a)) > maxArticleChars {
			return nil, fmt.Errorf("an article is a link or a short description, at most %d characters: %.40q… is longer", maxArticleChars, a)
		}
		seen[key] = true
		out = append(out, a)
	}
	if len(out) > maxCollectionArticles {
		return nil, fmt.Errorf("at most %d articles can be listed (%d given): for a whole topic, leave it to auto-fill's own searches", maxCollectionArticles, len(out))
	}
	return out, nil
}

// isArticleLink reports whether a listed article is an address to fetch as is,
// rather than a description to search for.
func isArticleLink(a string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	return (strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://")) && !strings.ContainsAny(a, " \t")
}

// pendingArticles are the listed articles not fetched yet. A link already in
// the collection by another road counts as fetched.
func pendingArticles(c Collection) []string {
	ingested := map[string]bool{}
	for _, u := range c.IngestedURLs {
		ingested[normalizeIngestURL(u)] = true
	}
	var out []string
	for _, a := range c.Articles {
		if strings.TrimSpace(c.ArticlesFetched[a]) != "" {
			continue
		}
		if isArticleLink(a) && ingested[normalizeIngestURL(a)] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// articleRows is the list as the page shows it: each entry with where it was
// fetched from, or nothing yet.
func articleRows(c Collection) []map[string]string {
	ingested := map[string]string{}
	for _, u := range c.IngestedURLs {
		ingested[normalizeIngestURL(u)] = u
	}
	rows := make([]map[string]string, 0, len(c.Articles))
	for _, a := range c.Articles {
		from := strings.TrimSpace(c.ArticlesFetched[a])
		if from == "" && isArticleLink(a) {
			from = ingested[normalizeIngestURL(a)]
		}
		rows = append(rows, map[string]string{"article": a, "fetched_from": from})
	}
	return rows
}

// searchCandidate is one web search result: its address and its title.
type searchCandidate struct {
	URL   string
	Title string
}

// parseWebSearchResults reads WebSearch's text: blank-line-separated blocks of
// "N. Title", the URL, and a snippet. The title is kept with its URL so a
// fetched document gets a readable name rather than the last part of its path.
func parseWebSearchResults(raw string) []searchCandidate {
	var out []searchCandidate
	for _, block := range strings.Split(raw, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		title := strings.TrimSpace(lines[0])
		if dot := strings.Index(title, ". "); dot >= 0 && dot < 4 {
			title = strings.TrimSpace(title[dot+2:])
		}
		for _, ln := range lines[1:] {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "http://") || strings.HasPrefix(ln, "https://") {
				out = append(out, searchCandidate{URL: ln, Title: title})
				break
			}
		}
	}
	return out
}

// fetchArticles fetches the listed articles not fetched yet, in list order, up
// to budget documents, and returns what it added and each one's outcome.
//
// A link is fetched as given. A description is searched for and the first of
// the top few results that fetches cleanly is taken. The LLM judge is not
// asked: it decides whether a page fits the collection, and the owner already
// decided that by naming it. Where a search found the wrong page, the page
// shows the address it came from, and the document can be removed.
func (T *OrchestrateApp) fetchArticles(ctx context.Context, udb Database, c *Collection, budget int) (added int, items []autofillItem) {
	for _, article := range pendingArticles(*c) {
		if added >= budget || ctx.Err() != nil {
			break
		}
		var tries []searchCandidate
		if isArticleLink(article) {
			tries = []searchCandidate{{URL: article}}
		} else {
			for _, cand := range parseWebSearchResults(WebSearch(article)) {
				if !looksLikeUsefulSourceURL(cand.URL) {
					continue
				}
				tries = append(tries, cand)
				if len(tries) == articleSearchTries {
					break
				}
			}
		}
		if len(tries) == 0 {
			items = append(items, autofillItem{Article: article, Status: "not_found", Err: "the search found nothing to fetch: try a more exact description, or paste its link"})
			continue
		}
		item := autofillItem{Article: article, Status: "unusable"}
		for _, cand := range tries {
			if collectionHasURL(*c, cand.URL) {
				// Already in by another road: the article is satisfied.
				item = autofillItem{Article: article, URL: cand.URL, Status: "already", Name: cand.Title}
				T.recordIngest(udb, c, cand.URL, article)
				break
			}
			name, text, raw, mime, err := fetchAndExtractForIngest(ctx, cand.URL)
			if t := strings.TrimSpace(cand.Title); t != "" {
				name = t
			}
			if err != nil {
				item = autofillItem{Article: article, URL: cand.URL, Status: "unusable", Name: name, Err: err.Error()}
				continue
			}
			if len(text) > maxAutofillDocChars {
				item = autofillItem{Article: article, URL: cand.URL, Status: "too_large", Name: name,
					Err: fmt.Sprintf("%d chars > %d cap (likely junk page or aggregator)", len(text), maxAutofillDocChars)}
				continue
			}
			chunks := T.ingestAutofillDoc(ctx, *c, name, text, raw, mime)
			T.recordIngest(udb, c, cand.URL, article)
			item = autofillItem{Article: article, URL: cand.URL, Status: "added", Name: name, Chunks: chunks}
			added++
			break
		}
		Log("[orchestrate.autofill] collection=%s article %q: %s %s", c.ID, article, item.Status, item.URL)
		items = append(items, item)
	}
	return added, items
}

// collectionHasURL reports whether this address is already in the collection.
func collectionHasURL(c Collection, u string) bool {
	norm := normalizeIngestURL(u)
	for _, have := range c.IngestedURLs {
		if normalizeIngestURL(have) == norm {
			return true
		}
	}
	return false
}

// ingestAutofillDoc adds one fetched document to the collection, with the
// kind-tagged regions of an HTML page (comments, related links, author bios)
// under sibling ids so the chunker's boundaries stay clean and a reader can
// tell them from the body. Returns the chunks written.
func (T *OrchestrateApp) ingestAutofillDoc(ctx context.Context, c Collection, name, text string, raw []byte, mime string) int {
	baseReportID := fmt.Sprintf("autofill-%s-%d", c.ID, time.Now().UnixNano())
	chunkDB := T.collectionDB(c)
	IngestReport(ctx, chunkDB, collectionSource(c.ID), baseReportID, "## "+name+"\n\n"+text)
	chunks := countReportChunks(chunkDB, baseReportID)
	lowMime, lowName := strings.ToLower(mime), strings.ToLower(name)
	if len(raw) == 0 || !(strings.HasPrefix(lowMime, "text/html") || strings.HasSuffix(lowName, ".html") || strings.HasSuffix(lowName, ".htm")) {
		return chunks
	}
	buckets, _ := ExtractHTMLByKind(raw)
	for kind, kindText := range buckets {
		if kind == "" || len(kindText) < 100 {
			continue // the default bucket is the body, already in; tiny regions are noise
		}
		kindReportID := baseReportID + "-" + kind
		IngestReportTagged(ctx, chunkDB, collectionSource(c.ID), kindReportID, "## "+name+" ("+kind+")\n\n"+kindText, kind)
		chunks += countReportChunks(chunkDB, kindReportID)
	}
	return chunks
}

// recordIngest notes on the collection that this address is in, and which
// listed article it satisfied, if any, after every document rather than at the
// end, so a run cut short does not fetch the same pages again next time.
//
// It saves the record as it is NOW with the two marks applied, not the copy
// the run started with: a run takes minutes, and saving its starting copy
// would undo whatever the owner changed meanwhile (an article added to the
// list, a new description). c is updated too, so the run sees its own marks.
func (T *OrchestrateApp) recordIngest(udb Database, c *Collection, u, article string) {
	mark := func(rec *Collection) {
		if !collectionHasURL(*rec, u) {
			rec.IngestedURLs = append(rec.IngestedURLs, u)
		}
		if article == "" {
			return
		}
		for _, a := range rec.Articles {
			if a == article {
				if rec.ArticlesFetched == nil {
					rec.ArticlesFetched = map[string]string{}
				}
				rec.ArticlesFetched[article] = u
				return
			}
		}
	}
	mark(c)
	home := T.collectionHome(udb, *c)
	fresh, ok := loadCollection(home, c.Owner, c.ID)
	if !ok {
		saveCollection(home, *c)
		return
	}
	mark(&fresh)
	saveCollection(home, fresh)
}

// collectionHome is the store a collection's record lives in: its owner's,
// whoever is acting on it. A contributor's own store is the wrong one, and
// saving there leaves a stray copy the owner's record never sees. The caller's
// store stands in only for a record with no owner, which lives in whoever's
// store it was written to; a deployment collection saves to the deployment's
// whatever the handle.
func (T *OrchestrateApp) collectionHome(udb Database, c Collection) Database {
	if c.Owner == "" {
		return udb
	}
	return UserDB(T.DB, c.Owner)
}
