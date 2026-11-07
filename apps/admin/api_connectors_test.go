package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The value↔spec mapping (ReadValues/BuildSpec) now lives in core with the
// templates and is tested there; this covers the admin-only Edit-spec transforms.
func TestNestAndStringifyComfyWorkflow(t *testing.T) {
	// A spec whose comfy_workflow is a string holding pretty JSON.
	spec, _ := json.Marshal(RestImageSpec{
		SubmitURL:     "http://x/prompt",
		ComfyWorkflow: "{\n  \"6\": {\n    \"class_type\": \"CLIPTextEncode\"\n  }\n}",
	})

	// Edit-spec view: comfy_workflow becomes a NESTED object, not an escaped string.
	nested, ok := nestComfyWorkflow(spec)
	if !ok {
		t.Fatal("expected nesting to apply")
	}
	if strings.Contains(string(nested), `\"6\"`) || strings.Contains(string(nested), `\n`) {
		t.Errorf("workflow still escaped in Edit-spec view:\n%s", nested)
	}
	if !strings.Contains(string(nested), "\"comfy_workflow\": {") {
		t.Errorf("comfy_workflow not rendered as a nested object:\n%s", nested)
	}

	// Save path: an edited nested object folds back to a string, content survives.
	backToString := stringifyComfyWorkflow(nested)
	var out RestImageSpec
	if err := json.Unmarshal(backToString, &out); err != nil {
		t.Fatalf("stringified spec invalid: %v\n%s", err, backToString)
	}
	if !strings.Contains(out.ComfyWorkflow, "CLIPTextEncode") {
		t.Errorf("workflow content lost on round-trip: %q", out.ComfyWorkflow)
	}
	if !strings.Contains(out.ComfyWorkflow, "\n") {
		t.Errorf("workflow should be pretty (multi-line) after round-trip: %q", out.ComfyWorkflow)
	}

	// A non-image spec (no comfy_workflow) is passed through untouched.
	if _, ok := nestComfyWorkflow([]byte(`{"credential":"no_auth"}`)); ok {
		t.Error("nesting should not apply to a spec without comfy_workflow")
	}
}

// A service connector's row says how it is doing and asks before a test what
// the test should cost, and an image service is offered as the image
// provider once approved.
func TestAServiceConnectorRowAndThePicker(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"submit":{"method":"POST","path":"/v1/images","body":{"prompt":"{prompt}"}},"models":{"path":"/v1/models"}},
		"health":{"call":"models","expect":"data"}},
		"provides":[{"capability":"image.generate","steps":[{"call":"submit"}],
		"output":{"image":{"path":"data.0.b64_json","as":"base64"}},"cost":{"per_call":0.04}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "acme-images", Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	if q := testConfirm("acme-images"); !strings.Contains(q, "$0.0400") || !strings.Contains(q, "daily test budget") {
		t.Errorf("the question before a test: %q", q)
	}
	conn, _ := GetConnector(RootDB, "acme-images")
	if status, hasHealth := connectorStatus(conn); !hasHealth || status != "not checked yet" {
		t.Errorf("status %q, has health %v", status, hasHealth)
	}
	offered := func() bool {
		for _, r := range (&AdminApp{db: RootDB}).imageRows() {
			if r.Name == "acme-images" {
				return true
			}
		}
		return false
	}
	if !offered() {
		t.Error("an image service no page uses yet is not offered: choosing it is what puts it to use")
	}
	if err := ApproveConnector(RootDB, "acme-images"); err != nil {
		t.Fatal(err)
	}

	// Its Endpoints: each one as it is sent and read, and what uses it.
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerConnectorsRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/connectors/endpoints?name=acme-images", nil))
	var eps []service.Endpoint
	json.Unmarshal(w.Body.Bytes(), &eps)
	if len(eps) != 2 || eps[0].Address != "POST /v1/images" || eps[0].Sends != "json: prompt ← {prompt}" ||
		eps[0].Reads != "data.0.b64_json → image (a file, from base64)" || eps[1].UsedBy != "health check" {
		t.Errorf("endpoints: %d %s", w.Code, w.Body.String())
	}
	rows, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
	if b, _ := json.Marshal(rows); !strings.Contains(string(b), `"name":"acme-images"`) || !strings.Contains(string(b), `"endpoints":true`) {
		t.Errorf("a service row offers its Endpoints: %s", b)
	}
}

// The Contracts page is gone. A connector's editor shows the job each
// capability it gives must do (what it takes, gives and is checked for),
// and opens as Fix when its spec does not validate. The connectors and the
// integrations of other kinds are two lists, each section its own.
func TestAConnectorsEditorShowsItsJob(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	finder := `{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: service.Kind, Spec: json.RawMessage(finder)}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set("connectors", "broken", Connector{Name: "broken", Kind: service.Kind, ID: "c_broken", Spec: json.RawMessage(`{"provider":{}}`)})
	tpl, _ := GetConnectorTemplate("a1111")
	art, _, err := tpl.BuildSpec(map[string]any{"base_url": "http://sd.example:7860"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "art", Kind: service.Kind, Template: "a1111", Spec: art}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set("connectors", "relay", Connector{Name: "relay", Kind: PeerRelayConnectorKind, ID: "c_relay", Spec: json.RawMessage(`{}`)})
	a := &AdminApp{db: RootDB}
	out, err := a.handleConnectorFixForm(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors/fix-form?name=finder", nil)})
	if err != nil {
		t.Fatal(err)
	}
	if job := out.(map[string]any)["contract"].(string); !strings.HasPrefix(job, "web.search: ") ||
		!strings.Contains(job, "It takes query (text, required), count (number) and gives results (json)") {
		t.Errorf("its job: %q", job)
	}

	rows := func(h func(*jsonapi.Call) (any, error)) map[string]map[string]any {
		out, _ := h(&jsonapi.Call{R: httptest.NewRequest("GET", "/", nil)})
		b, _ := json.Marshal(out)
		var list []map[string]any
		json.Unmarshal(b, &list)
		got := map[string]map[string]any{}
		for _, r := range list {
			got[r["name"].(string)] = r
		}
		return got
	}
	connectors, others := rows(a.handleConnectors), rows(a.handleOtherConnectors)
	if len(connectors) != 3 || connectors["art"]["edit_form"] != true || connectors["art"]["edit_spec"] == true ||
		connectors["finder"]["edit_spec"] != true || connectors["finder"]["edit_form"] == true || connectors["finder"]["needs_fix"] == true ||
		connectors["broken"]["needs_fix"] != true || connectors["broken"]["edit_spec"] == true {
		t.Errorf("the connectors: %v", connectors)
	}
	if len(others) != 1 || others["relay"]["can_approve"] != true || others["relay"]["edit_spec"] == true {
		t.Errorf("the other integrations: %v", others)
	}
	var titles []string
	for _, s := range a.extensionsSections() {
		titles = append(titles, s.Title)
	}
	got := strings.Join(titles, ", ")
	if strings.Contains(got, "Contracts") || !strings.Contains(got, "Connectors, Other integrations") {
		t.Errorf("the sections: %s", got)
	}
}

// The Research Sources form offers every search connector, in use or not
// (adding one is what puts it to use), and nothing that does not search.
func TestResearchSourcesOfferOnlySearchConnectors(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	search := `{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`
	speech := `{"provider":{"transport":"http","base_url":"https://say.example.test","auth":{"none":true},"operations":{"say":{"method":"POST","path":"/say","body":{"text":"{text}"},"read":"file"}}},"provides":[{"capability":"speech.synthesize","steps":[{"call":"say"}],"output":{"audio":{"from":"body"}}}]}`
	for name, spec := range map[string]string{"finder": search, "voice": speech} {
		if err := SaveConnector(RootDB, Connector{Name: name, Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	opts := researchSourceOptions()
	if len(opts) != 1 || opts[0].Value != "finder" {
		t.Errorf("offered: %+v", opts)
	}
}

// The Connectors table bands each connector by what it does: every contract
// has a band, a search a research source names (or one from a research
// source's template) is a research source unless Web Search uses it, and
// only connectors with a spec are listed: nothing gohort does in Go, no
// peer and no program.
func TestConnectorsAreBandedByWhatTheyDo(t *testing.T) {
	for _, name := range service.Names() {
		if capabilityGroups[name] == "" {
			t.Errorf("the contract %s has no band on the Connectors page", name)
		}
	}
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	search := `{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`
	for _, c := range []Connector{
		{Name: "engine", Kind: service.Kind, Spec: json.RawMessage(search)},
		{Name: "papers", Kind: service.Kind, Spec: json.RawMessage(search)},
		{Name: "filings", Kind: service.Kind, Template: "edgar-search", Spec: json.RawMessage(search)},
		{Name: "court", Kind: service.Kind, Template: "edgar-search", Spec: json.RawMessage(search)},
	} {
		if err := SaveConnector(RootDB, c); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
	}
	RootDB.Set(SearchTable, "provider", "court")
	if err := service.SaveResearchSource(RootDB, service.ResearchSource{Connector: "papers", Always: true, Off: true}); err != nil {
		t.Fatal(err)
	}
	a := &AdminApp{db: RootDB}
	out, err := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var rows []map[string]any
	json.Unmarshal(b, &rows)
	got := map[string]string{}
	for _, r := range rows {
		got[r["name"].(string)] = r["group"].(string)
	}
	for name := range got {
		if strings.Contains(name, ":") {
			t.Errorf("a row with no spec to edit is listed: %s", name)
		}
	}
	for name, want := range map[string]string{
		"engine": groupSearch, "papers": groupResearch, "filings": groupResearch, "court": groupSearch,
	} {
		if got[name] != want {
			t.Errorf("%s sits under %q, want %q", name, got[name], want)
		}
	}
}

// A services connector has no approval: Approve on its row is refused; its
// row says which pages reference it, none, or a change waiting; and saving
// the page with it chosen accepts a waiting change.
func TestAServicesConnectorIsReferencedFromItsPage(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://song.example.test","auth":{"none":true},
		"operations":{"song":{"method":"POST","path":"/song","body":{"prompt":"{prompt}"},"read":"file"}}},
		"provides":[{"capability":"music.generate","steps":[{"call":"song"}],"output":{"audio":{"from":"body"}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "tunes", Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	a := &AdminApp{db: RootDB}
	call := func(method, target string) *jsonapi.Call {
		return &jsonapi.Call{R: httptest.NewRequest(method, target, nil)}
	}
	row := func() map[string]any {
		out, _ := a.handleConnectors(call("GET", "/api/connectors"))
		b, _ := json.Marshal(out)
		var rows []map[string]any
		json.Unmarshal(b, &rows)
		for _, r := range rows {
			if r["name"] == "tunes" {
				return r
			}
		}
		t.Fatal("no row for tunes")
		return nil
	}
	referenced := func() bool { return service.Referenced(RootDB, "tunes") }

	if _, err := a.handleConnectorAction(call("POST", "/api/connectors?name=tunes&action=approve"), nil); err == nil ||
		!strings.Contains(err.Error(), "has no approval") {
		t.Errorf("Approve on its row: %v", err)
	}
	if r := row(); r["state"] != "unreferenced" || r["can_approve"] == true || !strings.HasPrefix(r["status"].(string), "Not referenced") {
		t.Errorf("an unreferenced row: %v", r)
	}
	finder := `{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: service.Kind, Spec: json.RawMessage(finder)}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"nothing", "finder"} {
		if _, err := a.handleMediaBackendRole(call("POST", "/api/media-backends/role?name="+n+"&role=offered"), struct{}{}); err == nil {
			t.Errorf("Media Generation offered %s, which does not make music", n)
		}
	}
	if _, err := a.handleMediaBackendRole(call("POST", "/api/media-backends/role?name=tunes&role=offered"), struct{}{}); err != nil || !referenced() {
		t.Fatalf("offering it on Media Generation: %v, referenced %v", err, referenced())
	}
	if r := row(); r["state"] != "referenced" || !strings.HasPrefix(r["status"].(string), "Referenced by Media Generation (offered to agents).") {
		t.Errorf("a referenced row: %v", r)
	}

	// Changed by someone not an administrator: held, and said so on its row
	// and in the page's choice, until the page is saved with it chosen.
	service.MarkChanged(RootDB, "tunes", "someone")
	if r := row(); r["state"] != "changed" || !strings.HasPrefix(r["status"].(string), "Changed by someone on ") {
		t.Errorf("a changed row: %v", r)
	}
	if rows, _ := a.handleMediaBackends(call("GET", "/api/media-backends")); !strings.Contains(func() string { b, _ := json.Marshal(rows); return string(b) }(), "tunes (changed, waiting to be accepted)") {
		t.Errorf("its row on Media Generation: %+v", rows)
	}
	if _, err := a.handleMediaBackendRole(call("POST", "/api/media-backends/role?name=tunes&role=offered"), struct{}{}); err != nil {
		t.Errorf("saving the page with it chosen: %v", err)
	}
	if _, waiting := service.PendingChange(RootDB, "tunes"); waiting {
		t.Error("accepted, still waiting")
	}
	if _, err := a.handleMediaBackendRole(call("POST", "/api/media-backends/role?name=tunes&role=off"), struct{}{}); err != nil || referenced() {
		t.Errorf("taken off Media Generation: %v, referenced %v", err, referenced())
	}
}
