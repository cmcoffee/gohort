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

// An older image backend opens in Fix as a service spec, and saving it from
// the Fix converts it into a service connector, off until approved again.
// The assistant is told the grammar, the contract and the rules, and its
// check refuses a draft that changes a price.
func TestFixingAnOlderImageBackendConvertsIt(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerConnectorsRoutes(mux)
	a.registerConnectorFixRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	old, err := ApplyRestImagePreset("a1111", RestImageSpec{Credential: "no_auth"}, map[string]string{"base_url": "http://sd.lan.test:7860"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(old)
	if err := SaveConnector(RootDB, Connector{Name: "sd", Kind: RestImageConnectorKind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "sd")
	conn, _ := GetConnector(RootDB, "sd")
	if !service.Fixable(conn) {
		t.Fatal("an A1111 backend is not offered a Fix")
	}

	w := call("GET", "/api/connectors/fix-form?name=sd", "")
	var form struct{ Spec, Note string }
	json.Unmarshal(w.Body.Bytes(), &form)
	if w.Code != 200 || !strings.Contains(form.Spec, `"capability": "image.generate"`) || !strings.Contains(form.Note, "converts it") {
		t.Fatalf("fix form: %d %s", w.Code, w.Body.String())
	}

	r := httptest.NewRequest("POST", "/api/connectors/fix?name=sd", nil)
	about, err := connectorFixAbout(r)
	if err != nil || !strings.Contains(about, "list[field=value]") || !strings.Contains(about, "image.generate takes prompt") || !strings.Contains(about, "keeps what the connector provides") {
		t.Errorf("what the assistant is told: %v\n%s", err, about)
	}
	tools := map[string]AgentToolDef{}
	for _, tl := range connectorFixTools(r) {
		tools[tl.Tool.Name] = tl
	}
	var m map[string]any
	json.Unmarshal([]byte(form.Spec), &m)
	m["provides"].([]any)[0].(map[string]any)["cost"] = map[string]any{"per_call": 1}
	pricey, _ := json.Marshal(m)
	if out, _ := tools["service_check"].Handler(context.Background(), map[string]any{"spec": string(pricey)}); !strings.Contains(out, "Not valid") || !strings.Contains(out, "price") {
		t.Errorf("a draft changing a price: %q", out)
	}
	if out, _ := tools["service_check"].Handler(context.Background(), map[string]any{"spec": form.Spec}); !strings.Contains(out, "Valid") {
		t.Errorf("the translated spec: %q", out)
	}

	body, _ := json.Marshal(map[string]string{"spec": form.Spec})
	if w := call("POST", "/api/connectors/fix-save?name=sd", string(body)); w.Code != 200 || !strings.Contains(w.Body.String(), "approve it") {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	conn, _ = GetConnector(RootDB, "sd")
	if conn.Kind != service.Kind || conn.Approved {
		t.Errorf("after the save: kind %s, approved %v", conn.Kind, conn.Approved)
	}
	if ImageBackendRegistered("sd") {
		t.Error("the old backend is still live after the conversion")
	}
	if w := call("POST", "/api/connectors/fix-save?name=sd", `{"spec":"{\"provider\":{}}"}`); w.Code != 400 {
		t.Errorf("an invalid spec saved: %d", w.Code)
	}
}

// The Templates list starts empty: the built-ins are offered under Add, a
// picked one joins the list, and Remove takes it off again, back under Add.
func TestTheTemplatesListHoldsWhatWasAdded(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerRecipeRoutes(mux)
	list := func(path string) []map[string]any {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var rows []map[string]any
		json.Unmarshal(w.Body.Bytes(), &rows)
		return rows
	}
	has := func(rows []map[string]any, id string) bool {
		for _, r := range rows {
			if r["id"] == id {
				return true
			}
		}
		return false
	}
	if rows := list("/api/templates"); len(rows) != 0 {
		t.Fatalf("the list starts with %d templates", len(rows))
	}
	if !has(list("/api/templates/builtins"), "gemini-images") {
		t.Fatal("the Gemini template is not offered under Add")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/templates/pick?id=gemini-images", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatalf("pick: %d %s", w.Code, w.Body.String())
	}
	rows := list("/api/templates")
	if !has(rows, "gemini-images") || rows[0]["_picked"] != true || has(list("/api/templates/builtins"), "gemini-images") {
		t.Errorf("after picking: list %v", rows)
	}
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/templates/unpick?id=gemini-images", strings.NewReader("{}")))
	if has(list("/api/templates"), "gemini-images") || !has(list("/api/templates/builtins"), "gemini-images") {
		t.Error("after removing, it is not back under Add")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/templates/pick?id=nope", strings.NewReader("{}")))
	if w.Code != 404 {
		t.Errorf("picking a template that is not built in: %d", w.Code)
	}
}
