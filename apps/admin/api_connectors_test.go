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
	"github.com/cmcoffee/gohort/core/ui"
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
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"credential":"acme_key"},
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
		for _, o := range imageFormFields()[0].Options {
			if o.Value == "acme-images" {
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

// The Contracts section lists every contract from the registry, in full:
// what it is, what it takes, gives and is checked for, and where it is used.
func TestTheContractsSectionExplainsEachContract(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	out, err := a.handleContracts(nil)
	if err != nil {
		t.Fatal(err)
	}
	list := out.(map[string]any)["contracts"].([]service.ContractInfo)
	if len(list) != len(service.Names()) {
		t.Fatalf("%d entries for %d contracts", len(list), len(service.Names()))
	}
	var search service.ContractInfo
	for _, c := range list {
		if c.Name == "web.search" {
			search = c
		}
	}
	if search.Where != `Admin > Capabilities > Web Search: "Search with"` || search.Takes != "query (text, required), count (number)" ||
		search.Gives != "results (json)" || !strings.HasPrefix(search.Checks, "results is a list") || !strings.Contains(search.About, "snippet") {
		t.Errorf("web.search: %+v", search)
	}
	s := contractsSection()
	panel, ok := s.Body.(ui.DisplayPanel)
	if !ok || panel.Source != "api/contracts" || s.Title != "Contracts" || len(panel.Pairs) != 1 || panel.Pairs[0].Field != "contracts" {
		t.Fatalf("the section: %+v", s)
	}
	shown := map[string]bool{}
	for _, it := range panel.Pairs[0].Items {
		shown[it.Field] = true
	}
	for _, f := range []string{"name", "about", "takes", "gives", "checks", "where"} {
		if !shown[f] {
			t.Errorf("the section does not show %s", f)
		}
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
// gohort's own providers sit with the connectors that do the same job.
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
	model := `{"provider":{"transport":"http","base_url":"https://chat.example.test","auth":{"none":true},"operations":{"models":{"path":"/models"}}},"provides":[{"capability":"llm.chat","dialect":"openai"}]}`
	for _, c := range []Connector{
		{Name: "engine", Kind: service.Kind, Spec: json.RawMessage(search)},
		{Name: "papers", Kind: service.Kind, Spec: json.RawMessage(search)},
		{Name: "filings", Kind: service.Kind, Template: "edgar-search", Spec: json.RawMessage(search)},
		{Name: "court", Kind: service.Kind, Template: "edgar-search", Spec: json.RawMessage(search)},
		{Name: "chat", Kind: service.Kind, Spec: json.RawMessage(model)},
	} {
		if err := SaveConnector(RootDB, c); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
	}
	RootDB.Set(SearchTable, "provider", "service:court")
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
	for name, want := range map[string]string{
		"engine": groupSearch, "papers": groupResearch, "filings": groupResearch, "court": groupSearch, "chat": groupLLMs,
		builtinPrefix + "worker-llm": groupLLMs, builtinPrefix + "mail": groupMail,
	} {
		if got[name] != want {
			t.Errorf("%s sits under %q, want %q", name, got[name], want)
		}
	}
}

// A services connector is put to use on the page that uses it, never by an
// Approve on its row; its row says which pages use it, none, or a change
// waiting; and saving the page with it chosen accepts a waiting change.
func TestAServicesConnectorIsPutToUseOnItsPage(t *testing.T) {
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
	inUse := func() bool { c, _ := GetConnector(RootDB, "tunes"); return c.Approved }

	if _, err := a.handleConnectorAction(call("POST", "/api/connectors?name=tunes&action=approve"), nil); err == nil ||
		!strings.Contains(err.Error(), "is put to use where it is used") || inUse() {
		t.Errorf("Approve on its row: %v, in use %v", err, inUse())
	}
	if r := row(); r["state"] != "unused" || r["can_approve"] == true || !strings.HasPrefix(r["status"].(string), "Not in use") {
		t.Errorf("an unused row: %v", r)
	}
	finder := `{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: service.Kind, Spec: json.RawMessage(finder)}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"nothing", "finder"} {
		if _, err := a.handleMusicSave(call("POST", "/api/music"), musicConfig{Agents: []string{n}}); err == nil {
			t.Errorf("Music took %s, which does not make music", n)
		}
	}
	if _, err := a.handleMusicSave(call("POST", "/api/music"), musicConfig{Agents: []string{"tunes"}}); err != nil || !inUse() {
		t.Fatalf("offering it on Music: %v, in use %v", err, inUse())
	}
	if r := row(); r["state"] != "in-use" || !strings.HasPrefix(r["status"].(string), "Used by Music (offered to agents).") {
		t.Errorf("a row in use: %v", r)
	}

	// Changed by someone not an administrator: held, and said so on its row
	// and in the page's choice, until the page is saved with it chosen.
	service.MarkChanged(RootDB, "tunes", "someone")
	service.Settle(RootDB)
	if inUse() {
		t.Fatal("a change waiting left it in use")
	}
	if r := row(); r["state"] != "changed" || !strings.HasPrefix(r["status"].(string), "Changed by someone on ") {
		t.Errorf("a changed row: %v", r)
	}
	if opts := musicOptions(); len(opts) != 1 || opts[0].Label != "tunes (changed, waiting to be accepted)" {
		t.Errorf("the page's choice: %+v", opts)
	}
	if _, err := a.handleMusicSave(call("POST", "/api/music"), musicConfig{Agents: []string{"tunes"}}); err != nil || !inUse() {
		t.Errorf("saving the page with it chosen did not accept it: %v, in use %v", err, inUse())
	}
	if _, waiting := service.PendingChange(RootDB, "tunes"); waiting {
		t.Error("accepted, still waiting")
	}
	if _, err := a.handleMusicSave(call("POST", "/api/music"), musicConfig{}); err != nil || inUse() {
		t.Errorf("taken off Music: %v, in use %v", err, inUse())
	}
}
