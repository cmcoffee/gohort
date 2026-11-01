package service

// A few research sources need Go between the request and the results: PubMed
// answers a search with ids and the articles in a second call, OpenAlex gives
// an abstract as a word-position index, and EDGAR cannot parse the field tags
// a model writes into a query. Each is a built-in search an operation names
// ({"builtin": "pubmed"}): the Go makes the requests and reads the replies
// into results, and everything else is the connector's data, as for any
// search: the address its requests go to, the credential that carries its
// key, the topics research asks it on, its tool and its price. Its requests
// go through the connector's own governed path, so the allow list, the audit
// and Private mode hold as for any other.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// fetchFunc makes a GET to path under the connector's address (or to a
// whole address on it), with headers, and gives back the reply's body.
type fetchFunc func(path string, headers map[string]string) ([]byte, error)

// builtinSearch answers query with at most count results.
type builtinSearch func(ctx context.Context, get fetchFunc, query string, count int) ([]SearchResult, error)

// builtinSearches are the built-in searches an operation may name.
var builtinSearches = map[string]builtinSearch{
	"pubmed":   searchPubMed,
	"openalex": searchOpenAlex,
	"edgar":    searchEDGAR,
}

// builtinSearchNames are the built-in searches, sorted.
func builtinSearchNames() []string {
	var out []string
	for n := range builtinSearches {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SourceGet makes a GET to path under the address of the search connector
// named name, through its own credential and governed path: for an app that
// reads more of a research source than its search gives (a court opinion's
// full text), within what the connector's credential allows.
func SourceGet(ctx context.Context, name, path string) ([]byte, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind || !c.Approved {
		return nil, fmt.Errorf("no search connector in use named %q", name)
	}
	s, err := specOf(c)
	if err == nil {
		s, err = settledFor(ctx, name, s)
	}
	if err != nil {
		return nil, err
	}
	t := s.transport(nil)
	return t.getter(ctx, Operation{})(path, nil)
}

// builtin runs the built-in search op names: its requests go to the
// provider's address through the connector's own governed path, and its
// results come back as the reply the spec reads, {"results": [...]}. A
// comfyui op runs its workflow instead (comfyui.go).
func (t transport) builtin(ctx context.Context, name string, op Operation, vars map[string]any) (reply, error) {
	if op.Builtin == comfyBuiltin {
		return t.comfy(name, op, vars)
	}
	search := builtinSearches[op.Builtin]
	query, _ := vars["query"].(string)
	results, err := search(ctx, t.getter(ctx, op), strings.TrimSpace(query), number(vars["count"], 10))
	if err != nil {
		return reply{}, fmt.Errorf("%s: %w", name, err)
	}
	if results == nil {
		results = []SearchResult{}
	}
	b, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return reply{}, err
	}
	r := reply{status: 200, text: string(b)}
	err = json.Unmarshal(b, &r.node)
	return r, err
}

// getter makes GETs to paths under the provider's address (or whole
// addresses on it) through the connector's own governed path.
func (t transport) getter(ctx context.Context, op Operation) fetchFunc {
	return func(path string, headers map[string]string) ([]byte, error) {
		addr := path
		if !strings.HasPrefix(path, "https://") && !strings.HasPrefix(path, "http://") {
			addr = strings.TrimRight(t.p.BaseURL, "/") + path
		}
		if err := media.WaitPause(ctx, t.p.BaseURL); err != nil {
			return nil, err
		}
		h := map[string]string{}
		for k, v := range t.p.Headers {
			h[k] = v
		}
		for k, v := range headers {
			h[k] = v
		}
		req := ToolCallRequest{URL: addr, Method: "GET", Headers: h, PipeFollowing: true, TimeoutSecs: t.timeout(op)}
		raw, err := t.send(req)
		if err != nil {
			return nil, err
		}
		status, text := splitReply(raw)
		if status == 429 {
			ra := ""
			if m := retryAfterRE.FindStringSubmatch(text); m != nil {
				ra = m[1]
			}
			media.NotePause(t.p.BaseURL, ra)
		}
		switch {
		case status < 200 || status >= 300:
			return nil, fmt.Errorf("the provider answered HTTP %d: %s", status, clip(text, 400))
		case strings.Contains(text, "... [TRUNCATED"):
			return nil, fmt.Errorf("the reply was larger than the size limit and was cut off")
		}
		return []byte(strings.TrimSpace(text)), nil
	}
}

// searchPubMed asks PubMed's E-utilities: esearch for the ids most relevant
// to query, then esummary for those articles, kept in esearch's order.
func searchPubMed(ctx context.Context, get fetchFunc, query string, count int) ([]SearchResult, error) {
	body, err := get(fmt.Sprintf("/entrez/eutils/esearch.fcgi?db=pubmed&retmode=json&retmax=%d&sort=relevance&term=%s", count, url.QueryEscape(query)), nil)
	if err != nil {
		return nil, fmt.Errorf("esearch: %w", err)
	}
	var found struct {
		Result struct {
			IDs []string `json:"idlist"`
		} `json:"esearchresult"`
	}
	if err := json.Unmarshal(body, &found); err != nil {
		return nil, fmt.Errorf("esearch: the reply is not JSON: %w", err)
	}
	if len(found.Result.IDs) == 0 {
		return nil, nil
	}
	body, err = get("/entrez/eutils/esummary.fcgi?db=pubmed&retmode=json&id="+strings.Join(found.Result.IDs, ","), nil)
	if err != nil {
		return nil, fmt.Errorf("esummary: %w", err)
	}
	var summary struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, fmt.Errorf("esummary: the reply is not JSON: %w", err)
	}
	var out []SearchResult
	for _, id := range found.Result.IDs {
		var a struct {
			Title   string `json:"title"`
			Source  string `json:"source"`
			PubDate string `json:"pubdate"`
		}
		if json.Unmarshal(summary.Result[id], &a) != nil || strings.TrimSpace(a.Title) == "" {
			continue
		}
		r := SearchResult{Title: a.Title, URL: "https://pubmed.ncbi.nlm.nih.gov/" + id + "/", Published: a.PubDate}
		if a.Source != "" {
			r.Snippet = "Published in: " + a.Source
		}
		out = append(out, r)
	}
	return out, nil
}

// searchOpenAlex asks OpenAlex for works, each linked to its open-access
// copy or its DOI, with its abstract rebuilt from the word-position index
// OpenAlex gives it as. The deployment's From address goes with it, which
// OpenAlex asks of callers that want its faster pool.
func searchOpenAlex(ctx context.Context, get fetchFunc, query string, count int) ([]SearchResult, error) {
	query = strings.TrimSpace(strings.NewReplacer(`"`, "", "'", "").Replace(query))
	if query == "" {
		return nil, nil
	}
	path := fmt.Sprintf("/works?per_page=%d&search=%s", count, url.QueryEscape(query))
	if from := strings.TrimSpace(contactEmail()); from != "" {
		path += "&mailto=" + url.QueryEscape(from)
	}
	body, err := get(path, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var data struct {
		Results []struct {
			Title    string           `json:"title"`
			DOI      string           `json:"doi"`
			Year     int              `json:"publication_year"`
			Abstract map[string][]int `json:"abstract_inverted_index"`
			Open     struct {
				URL string `json:"oa_url"`
			} `json:"open_access"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("the reply is not JSON: %w", err)
	}
	var out []SearchResult
	for _, w := range data.Results {
		link := w.Open.URL
		if link == "" {
			link = w.DOI
		}
		if w.Title == "" || link == "" {
			continue
		}
		// A year is not a day, so it goes with the title rather than as a
		// published date.
		r := SearchResult{Title: w.Title, URL: link, Snippet: clipRunes(abstractText(w.Abstract), 300)}
		if w.Year > 0 {
			r.Title = fmt.Sprintf("%s (%d)", w.Title, w.Year)
		}
		out = append(out, r)
	}
	return out, nil
}

// abstractText is an abstract given as each word's positions, in order.
func abstractText(index map[string][]int) string {
	at := map[int]string{}
	var pos []int
	for word, ps := range index {
		for _, p := range ps {
			at[p] = word
			pos = append(pos, p)
		}
	}
	sort.Ints(pos)
	words := make([]string, 0, len(pos))
	for _, p := range pos {
		words = append(words, at[p])
	}
	return strings.Join(words, " ")
}

// searchEDGAR asks the SEC's full-text search for 10-K, 10-Q and 8-K
// filings of the last two years and this one, the query cleared of the
// field tags EDGAR reads as words. The SEC asks callers to say who they are
// in the User-Agent.
func searchEDGAR(ctx context.Context, get fetchFunc, query string, count int) ([]SearchResult, error) {
	query = edgarQuery(query)
	if query == "" {
		return nil, nil
	}
	now := time.Now()
	path := fmt.Sprintf("/LATEST/search-index?q=%s&dateRange=custom&startdt=%d-01-01&enddt=%s&forms=10-K,10-Q,8-K&from=0&size=%d",
		url.QueryEscape(query), now.Year()-2, now.Format("2006-01-02"), count)
	contact := strings.TrimSpace(contactEmail())
	if contact == "" {
		contact = "contact@example.com"
	}
	body, err := get(path, map[string]string{"Accept": "application/json", "User-Agent": fmt.Sprintf("Gohort/%s (%s)", appVersion(), contact)})
	if err != nil {
		return nil, err
	}
	var data struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					Names     []string `json:"display_names"`
					CIKs      []string `json:"ciks"`
					Form      string   `json:"form"`
					Filed     string   `json:"file_date"`
					ADSH      string   `json:"adsh"`
					PeriodEnd string   `json:"period_ending"`
					Desc      string   `json:"file_description"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("the reply is not JSON: %w", err)
	}
	var out []SearchResult
	for _, h := range data.Hits.Hits {
		s := h.Source
		name := "Unknown filer"
		if len(s.Names) > 0 {
			name = s.Names[0]
			if i := strings.Index(name, "(CIK"); i > 0 {
				name = strings.TrimSpace(name[:i])
			}
		}
		cik := "0"
		if len(s.CIKs) > 0 {
			if c := strings.TrimLeft(s.CIKs[0], "0"); c != "" {
				cik = c
			}
		}
		_, file, _ := strings.Cut(h.ID, ":")
		desc := s.Form
		if s.Desc != "" && s.Desc != s.Form {
			desc += " (" + s.Desc + ")"
		}
		r := SearchResult{Title: name + ": " + desc, Published: s.Filed,
			URL: fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s/%s", cik, strings.ReplaceAll(s.ADSH, "-", ""), file)}
		if s.PeriodEnd != "" {
			r.Snippet = "Period ending: " + s.PeriodEnd
		}
		out = append(out, r)
	}
	return out, nil
}

// edgarFieldTags are the field tags a model writes into a query, which
// EDGAR's simple Lucene syntax reads as words: company:"Acme" finds
// nothing, "Acme" finds Acme's filings. Longer tags first, so "form type:"
// goes before "type:" could.
var edgarFieldTags = []string{
	"company:", "form type:", "formtype:", "form:",
	"keyword:", "keywords:",
	"ticker:", "cik:", "sic:",
	"name:", "filer:", "issuer:",
	"title:", "abstract:", "content:",
}

// edgarQuery is query as EDGAR can read it: field tags out, keeping their
// values, boolean words and quotes kept, and no AND, OR or parenthesis left
// dangling at either end.
func edgarQuery(query string) string {
	q := query
	lower := strings.ToLower(q)
	for _, tag := range edgarFieldTags {
		for i := strings.Index(lower, tag); i >= 0; i = strings.Index(lower, tag) {
			q = q[:i] + q[i+len(tag):]
			lower = strings.ToLower(q)
		}
	}
	words := strings.Fields(q)
	for len(words) > 0 && (words[0] == ")" || words[0] == "AND" || words[0] == "OR") {
		words = words[1:]
	}
	for n := len(words); n > 0 && (words[n-1] == "(" || words[n-1] == "AND" || words[n-1] == "OR"); n = len(words) {
		words = words[:n-1]
	}
	return strings.Join(words, " ")
}

// clipRunes is s cut to n runes, marked when cut.
func clipRunes(s string, n int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > n {
		return string(r[:n]) + "..."
	}
	return strings.TrimSpace(s)
}

func contactEmail() string {
	if sourcehooks.ContactEmail == nil {
		return ""
	}
	return sourcehooks.ContactEmail()
}

func appVersion() string {
	if sourcehooks.AppVersion == nil {
		return "dev"
	}
	return sourcehooks.AppVersion()
}
