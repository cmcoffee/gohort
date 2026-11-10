package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An image backend kept as a connection (A1111) opens in Fix as its own
// spec. The assistant is told the grammar, the contract and the rules, and
// its check refuses a draft that changes a price.
func TestFixingAnImageConnection(t *testing.T) {
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

	old, err := ApplyRestImagePreset("a1111", RestImageSpec{}, map[string]string{"base_url": "http://sd.lan.test:7860"})
	if err != nil {
		t.Fatal(err)
	}
	kind, raw, err := old.AsService("")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "sd", Kind: kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "sd")
	conn, _ := GetConnector(RootDB, "sd")
	if !service.Fixable(conn) {
		t.Fatal("an A1111 connection is not offered a Fix")
	}

	w := call("GET", "/api/connectors/fix-form?name=sd", "")
	var form struct{ Spec, Note string }
	json.Unmarshal(w.Body.Bytes(), &form)
	if w.Code != 200 || !strings.Contains(form.Spec, `"capability": "image.generate"`) || !strings.Contains(form.Spec, `"builtin": "a1111"`) {
		t.Fatalf("fix form: %d %s", w.Code, w.Body.String())
	}

	r := httptest.NewRequest("POST", "/api/connectors/fix?name=sd", nil)
	about, err := connectorFixAbout(r)
	if err != nil || !strings.Contains(about, "list[field=value]") || !strings.Contains(about, "image.generate: A picture made from a prompt. It takes prompt (text, required)") || !strings.Contains(about, "keeps what the connector provides") {
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
	if w := call("POST", "/api/connectors/fix-save?name=sd", string(body)); w.Code != 200 || !strings.Contains(w.Body.String(), "Saved.") {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	conn, _ = GetConnector(RootDB, "sd")
	if conn.Kind != service.Kind || !ImageBackendRegistered("sd") {
		t.Errorf("after the save: kind %s, served %v", conn.Kind, ImageBackendRegistered("sd"))
	}
	if w := call("POST", "/api/connectors/fix-save?name=sd", `{"spec":"{\"provider\":{}}"}`); w.Code != 400 {
		t.Errorf("an invalid spec saved: %d", w.Code)
	}
}

// Templates are browsed, not installed: the list holds every template, by
// category, each added from its row; there is no list to put one on first.
// A page's "+ Add another…" lists the templates for its kind: those filed
// under its category whose connector does its job.
func TestTemplatesAreBrowsedByCategory(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerRecipeRoutes(mux)
	get := func(path string) (int, []map[string]any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var rows []map[string]any
		json.Unmarshal(w.Body.Bytes(), &rows)
		return w.Code, rows
	}
	ids := func(rows []map[string]any) string {
		var out []string
		for _, r := range rows {
			out = append(out, r["id"].(string))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	_, all := get("/api/templates")
	if len(all) < 40 || all[0]["_recipe"] != true {
		t.Fatalf("every template, each added from its row: %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1]["category"].(string) > all[i]["category"].(string) {
			t.Fatalf("not in category order at %v", all[i]["id"])
		}
	}
	for need, want := range map[string]string{
		"web.search":        "brave-search,google-search,searxng-search,serper-search,tavily-search",
		"speech.synthesize": "elevenlabs-speech,openai-compatible-speech",
		"speech.transcribe": "openai-compatible-transcription,openai-transcription",
		"mail.send":         "mailgun-mail,postmark-mail,resend-mail",
		"decide.choice":     "vllm-system-one",
	} {
		if code, rows := get("/api/templates/for?need=" + need); code != 200 || ids(rows) != want {
			t.Errorf("%s: %d %s", need, code, ids(rows))
		}
	}
	if _, rows := get("/api/templates/for?need=media"); !strings.Contains(ids(rows), "comfyui") || !strings.Contains(ids(rows), "a1111") {
		t.Errorf("images, a ComfyUI one among them though its job is known only once added: %s", ids(rows))
	}
	if code, _ := get("/api/templates/for?need=nothing"); code != http.StatusBadRequest {
		t.Errorf("an unknown need: %d", code)
	}
	if code, _ := get("/api/templates/builtins"); code != http.StatusNotFound {
		t.Errorf("the list to add templates to is still there: %d", code)
	}
}

// A connector made from the Gemini template before the template learned to
// edit is offered Update from template on its row. Updating gives it what the
// template writes now, editing included, and keeps its key, which lives apart
// from the spec; after it, the row offers nothing, and a second update is
// refused.
func TestAConnectorIsUpdatedFromItsTemplate(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	now, ok := recipes.UpdatableConnectorSpec(RootDB, "gemini-images")
	if !ok {
		t.Fatal("the Gemini template is not updatable")
	}
	// The template as it was: generation only.
	var old map[string]any
	json.Unmarshal(now, &old)
	old["provides"] = old["provides"].([]any)[:1]
	oldSpec, _ := json.Marshal(old)
	if err := SaveConnector(RootDB, Connector{Name: "gemini-images", Kind: service.Kind, Template: "gemini-images", Spec: oldSpec}); err != nil {
		t.Fatal(err)
	}
	conn, _ := GetConnector(RootDB, "gemini-images")
	if err := SetConnectionSecret(RootDB, conn, "api_key", "AIza-kept-key", []string{"https://generativelanguage.googleapis.com"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := templateUpdate(conn); !ok {
		t.Fatal("a connector behind its template is not offered the update")
	}

	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerConnectorFixRoutes(sub)
	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest("POST", "/api/connectors/update-template?name=gemini-images", nil))
		return w
	}
	if w := post(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "settings and key are kept") {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	conn, _ = GetConnector(RootDB, "gemini-images")
	if !service.Provides(conn, "image.edit") || !service.Provides(conn, "image.generate") {
		t.Errorf("after the update it does not both generate and edit: %s", conn.Spec)
	}
	if sec, _ := GetConnectionSecret(RootDB, conn, "api_key"); sec.Value != "AIza-kept-key" {
		t.Errorf("the update lost the key: %+v", sec)
	}
	if _, ok := templateUpdate(conn); ok {
		t.Error("after the update the row still offers it")
	}
	if w := post(); w.Code != http.StatusBadRequest {
		t.Errorf("a second update: %d", w.Code)
	}
}
