package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/decide"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// System One is off until chosen under Services. Its choices are found, not
// set: a model is offered once it answers the test question right, and only
// while it is private; a connection that gives decisions is offered while
// its address is private; with every model declared private the rest are
// offered too, after, and marked; + Add another... ends the list. A finding is
// kept against what it was made on, asked again once that changes or a
// failure has stood a while, and a choice that is not set up or not private
// is refused.
func TestSystemOneIsChosenUnderServices(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	savedWorker, savedLead := SharedWorkerLLM(), SharedLeadLLM()
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() {
		RootDB, AuthDB = savedRoot, savedAuth
		SetSharedLLMs(savedWorker, savedLead)
		SetSharedJudgeLLM(nil, false, "", "")
	})
	SetSharedJudgeLLM(nil, false, "", "")
	// A worker on llama.cpp (no decisions) and a hosted Lead.
	adb.Set(LLMTable, "provider", "llama.cpp")
	adb.Set(LLMTable, "endpoint", "http://127.0.0.1:8080")
	adb.Set(LeadLLMTable, "provider", "anthropic")
	worker, lead := &FakeLLM{}, &FakeLLM{}
	SetSharedLLMs(worker, lead)

	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerSystemOneRoutes(sub)
	call := func(method, path, body string) map[string]any {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if out == nil {
			out = map[string]any{"_code": float64(w.Code)}
		}
		return out
	}
	values := func() []string {
		var out []string
		for _, o := range systemOneChoices() {
			out = append(out, o.Value+"="+o.Label)
		}
		return out
	}
	if got := call("GET", "/"+systemOneSource, ""); got["provider"] != "" || got["_status"] != nil {
		t.Errorf("System One before it is chosen: %v", got)
	}
	for _, p := range []string{"jev", decide.ProviderJudge, decide.ProviderLead} {
		if got := call("POST", "/"+systemOneSource, `{"provider":"`+p+`"}`); got["_code"] != float64(http.StatusBadRequest) {
			t.Errorf("%s (not set up, or not private) was not refused: %v", p, got)
		}
	}
	if systemOneProvider() != "" {
		t.Error("a refused choice was stored")
	}
	if got := strings.Join(values(), " | "); got != "=Off | decide.choice=+ Add another..." {
		t.Errorf("with no model that answers and no connection: %s", got)
	}
	if worker.Calls() == 0 || lead.Calls() != 0 {
		t.Errorf("the private worker must be asked and the hosted Lead never (%d, %d asks)", worker.Calls(), lead.Calls())
	}

	// A Judge model on a vLLM with its own System One, and connections on a
	// private and on a public address.
	native := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		native++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"answers":{"d":{"probabilities":{"o00":0.9,"o01":0.1}}},"diagnostics":{"d":{"label_mass":1}}}`)
	}))
	defer srv.Close()
	judge, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL + "/v1", SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	SetSharedJudgeLLM(judge, false, "judge", "")
	conn := func(name, base string) {
		spec := `{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},
			"operations":{"decide":{"method":"POST","path":"/systemone","body":{"state":"{state}","questions":{"d":{"type":"choice","instructions":"{ask}","criteria":"{criteria}"}}}}}},
			"provides":[{"capability":"decide.choice","steps":[{"call":"decide"}],
			"output":{"probabilities":{"path":"answers.d.probabilities","as":"json"},"label_mass":{"path":"diagnostics.d.label_mass"}}}]}`
		if err := SaveConnector(RootDB, Connector{Name: name, Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
			t.Fatal(err)
		}
	}
	conn("home-system-one", srv.URL+"/v1")
	conn("far-system-one", "http://203.0.113.5/v1")
	got := strings.Join(values(), " | ")
	if !strings.HasPrefix(got, "=Off | judge=Use the Offload model | home-system-one=") || strings.Contains(got, "far-system-one") ||
		strings.Contains(got, "worker=") || strings.Contains(got, "lead=") || !strings.HasSuffix(got, "decide.choice=+ Add another...") {
		t.Errorf("the private choices: %s", got)
	}
	SetAllLLMsPrivate(true)
	got = strings.Join(values(), " | ")
	if !strings.Contains(got, "home-system-one=") || !strings.Contains(got, "far-system-one=") ||
		strings.Index(got, "far-system-one") < strings.Index(got, "home-system-one") || !strings.Contains(got, "(allowed: every model declared private)") {
		t.Errorf("with every model declared private, the rest after the private ones and marked: %s", got)
	}
	SetAllLLMsPrivate(false)

	call("POST", "/"+systemOneSource, `{"provider":"judge"}`)
	got2 := call("GET", "/"+systemOneSource, "")
	if got2["provider"] != systemOneJudge || !strings.Contains(asText(got2["_status"]), "Working: native") {
		t.Errorf("the Judge model chosen: %v", got2)
	}
	if decide.Model() == nil {
		t.Error("the chosen Judge model does not answer decisions")
	}
	// Kept against what it was made on; a failure is asked again after a
	// while, and a finding about something else at once.
	var st systemOneStatus
	RootDB.Get(systemOneTable, "status:judge", &st)
	st.OK, st.Err = false, "an older finding"
	RootDB.Set(systemOneTable, "status:judge", st)
	if got := call("GET", "/"+systemOneSource, ""); !strings.Contains(asText(got["_status"]), "an older finding") {
		t.Errorf("a recent finding was not kept: %v", got["_status"])
	}
	st.At = time.Now().Add(-2 * systemOneRecheck)
	RootDB.Set(systemOneTable, "status:judge", st)
	if got := call("GET", "/"+systemOneSource, ""); strings.Contains(asText(got["_status"]), "an older finding") {
		t.Error("a failure that has stood a while was not asked again")
	}
	RootDB.Get(systemOneTable, "status:judge", &st)
	st.Of, st.Err, st.OK = "a judge that is no longer set", "an older finding", false
	RootDB.Set(systemOneTable, "status:judge", st)
	if got := call("GET", "/"+systemOneSource, ""); strings.Contains(asText(got["_status"]), "an older finding") {
		t.Error("a finding about something else was shown as current")
	}
	// A connection is chosen and tested the same way.
	call("POST", "/"+systemOneSource, `{"provider":"home-system-one"}`)
	if systemOneProvider() != "home-system-one" {
		t.Fatal("a private connection could not be chosen")
	}
	if test := call("POST", "/"+systemOneSource+"/test", `{"provider":"home-system-one"}`); test["ok"] != true {
		t.Errorf("Test of the connection: %v", test)
	}
	if got := call("POST", "/"+systemOneSource, `{"provider":"far-system-one"}`); got["_code"] != float64(http.StatusBadRequest) {
		t.Errorf("a connection on a public address was not refused: %v", got)
	}

	// Router shadows show once a machine step has been shadowed.
	if got := call("GET", "/"+systemOneSource, ""); got["_shadows"] != nil {
		t.Errorf("router shadows before any: %v", got["_shadows"])
	}
	RootDB.Set(decide.ShadowTable, "m\x00route", decide.ShadowTally{Machine: "m", Step: "route", Agreed: 3})
	if got := call("GET", "/"+systemOneSource, ""); !strings.Contains(asText(got["_shadows"]), "m, step route: sure 3 of 3 times") {
		t.Errorf("router shadows: %v", got["_shadows"])
	}

	found := false
	for _, tab := range adminLayout {
		for _, s := range tab.sections {
			found = found || (tab.tab == "Services" && s == "System One")
		}
	}
	if !found {
		t.Error("System One is not a section of the Services tab")
	}
}
