package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A connector can be added from its spec: the form loads blank, a draft that
// does not read or does not fit is refused with why, Test runs the draft's
// contract test without saving it, and Add saves it as a service connector
// that is off until approved, under a name not taken. The assistant beside
// the form is told the grammar and every contract, and checks a draft.
func TestAConnectorIsAddedFromItsSpec(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })
	var said []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		said = append(said, body["text"])
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...))
	}))
	defer srv.Close()
	spec := `{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},
		"operations":{"speak":{"method":"POST","path":"/v1/speech","body":{"text":"{text}"},"read":"file"}}},
		"provides":[{"capability":"speech.synthesize","steps":[{"call":"speak"}],"output":{"audio":{"from":"body"}}}]}`
	form := func(name, spec string) string {
		b, _ := json.Marshal(connectorAddForm{Name: name, Spec: spec})
		return string(b)
	}
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerConnectorAddRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	if w := call("GET", "/api/connectors/add", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"spec":""`) {
		t.Errorf("the blank form: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/connectors/add", form("voice", `{"provider":{}}`)); w.Code != 400 {
		t.Errorf("a spec that does not fit: %d %s", w.Code, w.Body.String())
	}
	var res map[string]any
	json.Unmarshal(call("POST", "/api/connectors/add/test", form("voice", spec)).Body.Bytes(), &res)
	if res["ok"] != true || !strings.Contains(res["message"].(string), "speech.synthesize: passed") || len(said) != 1 {
		t.Errorf("the draft's test: %v %v", res, said)
	}
	if _, ok := GetConnector(RootDB, "voice"); ok {
		t.Fatal("testing the draft saved it")
	}
	if w := call("POST", "/api/connectors/add", form("voice", spec)); w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if c, ok := GetConnector(RootDB, "voice"); !ok || c.Kind != service.Kind || c.Approved {
		t.Errorf("saved as: %+v %v", c, ok)
	}
	if w := call("POST", "/api/connectors/add", form("voice", spec)); w.Code != 400 || !strings.Contains(w.Body.String(), "already a connector called voice") {
		t.Errorf("a name taken: %d %s", w.Code, w.Body.String())
	}

	r := httptest.NewRequest("POST", "/api/connectors/add/assist", nil)
	about, err := connectorAddAbout(r)
	if err != nil || !strings.Contains(about, "list[field=value]") || !strings.Contains(about, "web.search takes") || !strings.Contains(about, "dialect") {
		t.Errorf("what the assistant is told: %v\n%s", err, about)
	}
	for _, tl := range connectorAddTools(r) {
		if tl.Tool.Name != "service_check" {
			continue
		}
		if out, _ := tl.Handler(context.Background(), map[string]any{"spec": spec}); !strings.HasPrefix(out, "Valid. It reads: Speech ⇐ speak ⇐ ") {
			t.Errorf("checking a good draft: %q", out)
		}
		if out, _ := tl.Handler(context.Background(), map[string]any{"spec": `{"provider":{}}`}); !strings.HasPrefix(out, "Not valid:") {
			t.Errorf("checking a bad draft: %q", out)
		}
	}
}
