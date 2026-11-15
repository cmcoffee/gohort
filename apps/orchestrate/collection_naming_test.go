package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
)

// From what they said the collection is for, the setup offers names to pick
// from (none it offered before) and a drafted description that is asked to
// name the concrete things searches should find. Other fields are refused.
func TestACollectionIsNamedAndDescribedFromTheirWords(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{Content: "Postgres Admin\nDatabase Runbooks\nPG Operations"},
		{Content: "\"The official PostgreSQL documentation on configuration, backups and replication.\""},
	}}
	T.LLM = fake
	ask := func(body map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		r := asUser(httptest.NewRequest(http.MethodPost, "/api/collection-suggest", strings.NewReader(string(raw))), user)
		r.Header.Set("Content-Type", "application/json")
		assist.Handler(T.collectionSuggestSpec())(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	record := map[string]any{"about": "running our postgres servers", "prefer": []string{"postgresql.org"}}
	code, out := ask(map[string]any{"field": "name", "record": record, "offered": []string{"PG Operations"}})
	if code != 200 {
		t.Fatalf("name: %d %v", code, out)
	}
	if vals, _ := out["values"].([]any); len(vals) != 2 || vals[0] != "Postgres Admin" {
		t.Errorf("names offered %v; one already passed over should not come back", out["values"])
	}
	p := fake.Prompt(0)
	for _, want := range []string{"running our postgres servers", "postgresql.org", "five names", "PG Operations"} {
		if !strings.Contains(p, want) {
			t.Errorf("the name prompt is missing %q:\n%s", want, p)
		}
	}
	code, out = ask(map[string]any{"field": "description", "record": record})
	if code != 200 || out["value"] != "The official PostgreSQL documentation on configuration, backups and replication." {
		t.Errorf("description: %d %v", code, out)
	}
	if !strings.Contains(fake.Prompt(1), "Name the concrete things") {
		t.Errorf("the description is not asked to name what searches should find:\n%s", fake.Prompt(1))
	}
	if code, _ := ask(map[string]any{"field": "filter_rules", "record": record}); code == 200 {
		t.Error("a field it does not draft was answered")
	}
}
