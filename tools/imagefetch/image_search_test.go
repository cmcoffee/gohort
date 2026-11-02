package imagefetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// find_image searches pictures through the connector Web Search is set to,
// and only when that connector finds pictures.
func TestFindImageSearchesThroughWebSearchsConnector(t *testing.T) {
	savedRoot, savedAuth, savedCfg := RootDB, AuthDB, LoadWebSearchConfigFunc
	t.Cleanup(func() { RootDB, AuthDB, LoadWebSearchConfigFunc = savedRoot, savedAuth, savedCfg })
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	provider := "service:pics"
	LoadWebSearchConfigFunc = func() WebSearchConfig { return WebSearchConfig{Provider: provider} }

	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		asked, _ = body["q"].(string)
		w.Write([]byte(`{"images":[{"imageUrl":"https://img.example/1.jpg","title":"A barn","source":"example.org","link":"https://example.org/barn"}]}`))
	}))
	defer srv.Close()
	spec := `{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},
		"operations":{"images":{"method":"POST","path":"/images","body":{"q":"{query}"}},
			"search":{"method":"POST","path":"/search","body":{"q":"{query}"}}}},
		"provides":[{"capability":"web.search","steps":[{"call":"search"}],"output":{"results":{"path":"organic","fields":{"title":"title","url":"link","snippet":"snippet"}}}},
			{"capability":"image.search","steps":[{"call":"images"}],"output":{"results":{"path":"images","fields":{"image_url":"imageUrl","title":"title","source":"source","link":"link"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "pics", Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	name, ok := imageSearchConnector()
	if !ok || name != "pics" {
		t.Fatalf("the connector Web Search is set to: %q %v", name, ok)
	}
	got, err := searchImages(context.Background(), name, "a red barn")
	if err != nil || len(got) != 1 || got[0].ImageURL != "https://img.example/1.jpg" || got[0].Link != "https://example.org/barn" || asked != "a red barn" {
		t.Errorf("the pictures: %+v %v (asked %q)", got, err, asked)
	}
	for _, p := range []string{"duckduckgo", "serper", "service:nothing"} {
		provider = p
		if _, ok := imageSearchConnector(); ok {
			t.Errorf("Web Search set to %s finds pictures", p)
		}
	}
	// A connector that searches the web but not pictures does not.
	SaveConnector(RootDB, Connector{Name: "words", Kind: "service", Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},
		"operations":{"search":{"method":"POST","path":"/search","body":{"q":"{query}"}}}},
		"provides":[{"capability":"web.search","steps":[{"call":"search"}],"output":{"results":{"path":"organic","fields":{"title":"title","url":"link","snippet":"snippet"}}}}]}`)})
	ApproveConnector(RootDB, "words")
	t.Cleanup(func() { UnapproveConnector(RootDB, "words") })
	provider = "service:words"
	if _, ok := imageSearchConnector(); ok {
		t.Error("a connector that only searches the web finds pictures")
	}
}
