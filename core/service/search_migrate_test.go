package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
)

// searchSettings stores Web Search as the admin page did before the move.
func searchSettings(provider, key, endpoint, source string) {
	RootDB.Set(SearchTable, "provider", provider)
	RootDB.Set(SearchTable, "api_key", key)
	RootDB.Set(SearchTable, "endpoint", endpoint)
	RootDB.Set(SearchTable, "source", source)
}

// searchAPI answers a search engine's requests, keeping each one's address,
// key header and body: Serper's web and image search, Google's
// items, SearXNG's results (ten, two of them dated).
func searchAPI(t *testing.T, header string) (*httptest.Server, *[]string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get(header)+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/images":
			w.Write([]byte(`{"images":[{"imageUrl":"https://img.example/1.jpg","title":"A lighthouse","source":"example.org","link":"https://example.org/p"}]}`))
		case r.URL.Path == "/search" && r.Method == "POST":
			w.Write([]byte(`{"organic":[{"title":"Gohort","link":"https://a.example/1","snippet":"First.","date":"Sep 28, 2026"}]}`))
		case strings.HasSuffix(r.URL.Path, "/customsearch/v1"):
			w.Write([]byte(`{"items":[{"title":"Gohort","link":"https://a.example/1","snippet":"First."}]}`))
		default:
			var rs []string
			for i := 1; i <= 10; i++ {
				date := "null"
				if i == 1 {
					date = `"2026-09-27T00:00:00"`
				}
				rs = append(rs, fmt.Sprintf(`{"title":"R%d","url":"https://a.example/%d","content":"Snippet %d.","publishedDate":%s}`, i, i, i, date))
			}
			w.Write([]byte(`{"results":[` + strings.Join(rs, ",") + `]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func forgetSearchCredentials(t *testing.T) {
	forgetCredentials(t, "serper_search", "google_search", "brave_search", "searxng_search_key")
}

// Web Search set to Serper with a key becomes the Serper template,
// installed and approved with that key in its credential; the setting names
// it, the key row is gone, and searches (web and image) go through it with
// the key. Run again, it changes nothing.
func TestSerperSearchBecomesAConnector(t *testing.T) {
	world(t)
	forgetSearchCredentials(t)
	searchSettings("serper", "sk-serper", "", "local")

	MigrateWebSearch(RootDB)

	var provider, key, endpoint string
	RootDB.Get(SearchTable, "provider", &provider)
	if provider != "service:serper-search" || RootDB.Get(SearchTable, "api_key", &key) || RootDB.Get(SearchTable, "endpoint", &endpoint) {
		t.Fatalf("the setting after: %q, key %q, endpoint %q", provider, key, endpoint)
	}
	c, ok := GetConnector(RootDB, "serper-search")
	if !ok || !c.Approved || !Provides(c, "web.search") || !Provides(c, "image.search") {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	if cred, ok := Secure().Load("serper_search"); !ok || cred.Disabled || cred.ParamName != "X-API-KEY" {
		t.Fatalf("the credential: %+v %v", cred, ok)
	}
	srv, got := searchAPI(t, "X-API-KEY")
	pointAt(t, "serper-search", srv.URL)
	out, err := Search(context.Background(), "serper-search", "rust & go")
	if err != nil || !strings.HasPrefix(out, "1. Gohort\n   https://a.example/1\n   [Published 2026-09-28] First.") {
		t.Errorf("a search: %q %v", out, err)
	}
	pics, err := ImageSearch(context.Background(), "serper-search", "lighthouse")
	if err != nil || len(pics) != 1 || pics[0] != (ImageResult{ImageURL: "https://img.example/1.jpg", Title: "A lighthouse", Source: "example.org", Link: "https://example.org/p"}) {
		t.Errorf("an image search: %+v %v", pics, err)
	}
	if len(*got) != 2 || !strings.HasPrefix((*got)[0], "POST /search sk-serper ") || !strings.HasPrefix((*got)[1], `POST /images sk-serper {"num":10,"q":"lighthouse"}`) {
		t.Errorf("the requests: %q", *got)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:web_search_to_connector:v1:", &m) || !m.Done || m.Error != "" || m.Changed != 3 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}

	searchSettings("serper", "sk-serper", "", "local")
	MigrateWebSearch(RootDB)
	if _, again := GetConnector(RootDB, "serper-search-2"); again {
		t.Error("the migration ran twice")
	}
}

// Google's key was kept as key:cx: the key goes to the credential, sent as
// ?key=, and the search engine's ID into the request.
func TestGoogleSearchBecomesAConnector(t *testing.T) {
	world(t)
	forgetSearchCredentials(t)
	searchSettings("google", "g-key:engine-7", "", "")
	if changed, err := migrateWebSearch(RootDB); err != nil || changed != 3 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	if cred, _ := Secure().Load("google_search"); cred.Type != SecureCredQuery || cred.ParamName != "key" || cred.Disabled {
		t.Errorf("the credential: %+v", cred)
	}
	srv, got := searchAPI(t, "")
	pointAt(t, "google-search", srv.URL)
	if out, err := Search(context.Background(), "google-search", "rust"); err != nil || !strings.HasPrefix(out, "1. Gohort") {
		t.Fatalf("a search: %q %v", out, err)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "cx=engine-7") || !strings.Contains((*got)[0], "key=g-key") || !strings.Contains((*got)[0], "q=rust") {
		t.Errorf("the request: %q", *got)
	}
}

// SearXNG's address becomes the connector's, a path it is under kept; a key
// it was sent becomes a bearer credential. Its results are cut to eight, as
// they were, and a date it gives is tagged.
func TestSearXNGBecomesAConnector(t *testing.T) {
	world(t)
	forgetSearchCredentials(t)
	srv, got := searchAPI(t, "Authorization")
	searchSettings("searxng", "proxy-key", srv.URL+"/searx/", "local")
	if changed, err := migrateWebSearch(RootDB); err != nil || changed != 3 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	c, _ := GetConnector(RootDB, "searxng-search")
	if !strings.Contains(string(c.Spec), `"base_url":"`+srv.URL+`/searx/"`) || !strings.Contains(string(c.Spec), `"credential":"searxng_search_key"`) {
		t.Fatalf("the spec: %s", c.Spec)
	}
	cred, _ := Secure().Load("searxng_search_key")
	cred.BaseURL = srv.URL
	Secure().Save(cred, "")
	out, err := Search(context.Background(), "searxng-search", "rust & go")
	if err != nil || len(resultBlocks(out)) != 8 || !strings.HasPrefix(out, "1. R1\n   https://a.example/1\n   [Published 2026-09-27] Snippet 1.") || !strings.Contains(out, "\n   Snippet 2.") {
		t.Errorf("a search:\n%s\n%v", out, err)
	}
	if len(*got) != 1 || !strings.HasPrefix((*got)[0], "GET /searx/search?q=rust+%26+go&format=json&categories=general Bearer proxy-key") {
		t.Errorf("the request: %q", *got)
	}
}

// resultBlocks counts a search's numbered results.
func resultBlocks(out string) []string {
	var n []string
	for _, block := range strings.Split(out, "\n\n") {
		if strings.TrimSpace(block) != "" {
			n = append(n, block)
		}
	}
	return n
}

// An engine missing what it needs never made a search, and is left as it
// is, as is a peer's search, DuckDuckGo, a Source Hook and a connector.
func TestWebSearchWithNothingToMove(t *testing.T) {
	world(t)
	for _, s := range [][4]string{
		{"", "", "", ""},
		{"brave", "", "", "local"},
		{"google", "g-key", "", "local"},
		{"searxng", "", "", "local"},
		{"searxng", "peer-key", "https://den.example/api/peer/v1", "peer:den"},
		{"duckduckgo", "", "", ""},
		{"hook", "", "", ""},
		{"service:finder", "", "", ""},
	} {
		searchSettings(s[0], s[1], s[2], s[3])
		if changed, err := migrateWebSearch(RootDB); err != nil || changed != 0 {
			t.Errorf("%v was moved: %d %v", s, changed, err)
		}
		var provider string
		if RootDB.Get(SearchTable, "provider", &provider); provider != s[0] {
			t.Errorf("%v became %q", s, provider)
		}
	}
	if n := len(ListConnectors(RootDB)); n != 0 {
		t.Errorf("%d connectors added", n)
	}
}

// Brave gives a page's age as page_age, or only as age: a field reads the
// first of its paths that has something.
func TestBraveReadsEitherAge(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"web":{"results":[{"title":"A","url":"https://a.example/1","description":"One.","page_age":"2026-09-20T10:00:00","age":"September 18, 2026"},{"title":"B","url":"https://a.example/2","description":"Two.","age":"3 days ago"},{"title":"C","url":"https://a.example/3","description":"Three."}]}}`))
	}))
	defer srv.Close()
	install(t, "brave-search", templateSpec(t, "brave-search", srv.URL))
	out, err := Search(context.Background(), "brave-search", "x")
	if err != nil || !strings.Contains(out, "[Published 2026-09-20] One.") || !strings.Contains(out, "\n   [Published ") || !strings.Contains(out, "\n   Three.") {
		t.Errorf("results:\n%s\n%v", out, err)
	}
	if strings.Count(out, "[Published") != 2 {
		t.Errorf("want two dated results:\n%s", out)
	}
	bad := templateSpec(t, "brave-search", srv.URL)
	bad["provides"].([]any)[0].(map[string]any)["output"].(map[string]any)["results"].(map[string]any)["fields"].(map[string]any)["published"] = "page_age|"
	if err := validateSpec(t, bad); err == nil || !strings.Contains(err.Error(), "field published") {
		t.Errorf("an empty alternative: %v", err)
	}
}

// validateSpec parses and checks a spec as saving it does.
func validateSpec(t *testing.T, spec map[string]any) error {
	s, err := Parse([]byte(mustJSON(t, spec)))
	if err != nil {
		return err
	}
	return s.Validate()
}

// The image search contract reads a list of pictures, and its check
// refuses one with no picture address: the spec reads the wrong field.
func TestImageSearchIsChecked(t *testing.T) {
	ct, _ := Lookup("image.search")
	for list, ok := range map[string]bool{
		`[{"image_url":"https://img.example/1.jpg","title":"A"}]`: true,
		`[]`:                             true,
		`[{"image_url":"","title":"A"}]`: false,
		`[{"image_url":"/local.jpg","title":"A"}]`: false,
		`{"images":[]}`: false,
	} {
		if err := ct.Check(Result{"results": Value{Text: list}}); (err == nil) != ok {
			t.Errorf("%s: %v", list, err)
		}
	}
}
