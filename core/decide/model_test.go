package decide

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// privateWorld gives the test its own stores and models, put back after.
func privateWorld(t *testing.T) Database {
	t.Helper()
	root, auth := RootDB, AuthDB
	worker, lead := SharedWorkerLLM(), SharedLeadLLM()
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() {
		RootDB, AuthDB = root, auth
		SetSharedLLMs(worker, lead)
		SetSharedJudgeLLM(nil, false, "", "")
	})
	return adb
}

// systemOneConnector saves a connector giving decisions at base, the shape
// the vLLM System One template installs.
func systemOneConnector(t *testing.T, name, base string) {
	t.Helper()
	spec := `{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},
		"operations":{"decide":{"method":"POST","path":"/systemone","body":{"model":"{model}","instructions":"{instructions}","state":"{state}",
		"questions":{"d":{"type":"choice","instructions":"{ask}","criteria":"{criteria}"}}}}}},
		"provides":[{"capability":"decide.choice","steps":[{"call":"decide"}],
		"output":{"probabilities":{"path":"answers.d.probabilities","as":"json"},"label_mass":{"path":"diagnostics.d.label_mass"}},
		"defaults":{"model":"judge"}}]}`
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
}

// A connection answers decisions with the options keyed by where they are
// shown, so the reversed second ask means what it says even through a
// template that writes them as an object: a server that always answers the
// option shown first has no lean left, both ways. It is chosen by its name
// and answers nothing but decisions.
func TestAConnectionAnswersByPosition(t *testing.T) {
	privateWorld(t)
	var mu sync.Mutex
	var crits []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		q := req["questions"].(map[string]any)["d"].(map[string]any)
		mu.Lock()
		crits = append(crits, q["criteria"].(map[string]any))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answers":{"d":{"probabilities":{"o00":0.8,"o01":0.2}}},"diagnostics":{"d":{"label_mass":0.9}}}`))
	}))
	defer srv.Close()
	systemOneConnector(t, "local-system-one", srv.URL+"/v1")
	if Model() != nil {
		t.Fatal("System One answers while off")
	}
	RootDB.Set(ConfigTable, "provider", "local-system-one")
	m := Model()
	if m == nil {
		t.Fatal("a private connection that gives decisions does not answer them")
	}
	if _, err := m.Chat(context.Background(), nil); err == nil {
		t.Error("a System One connection answered a chat")
	}
	a, err := Ask(context.Background(), m, Question{Ask: "Take it up?", Options: []string{"yes", "no"}, Both: true})
	if err != nil || !a.OK || !a.Native || len(crits) != 2 {
		t.Fatalf("the decision: %+v %v after %d asks", a, err, len(crits))
	}
	if crits[0]["o00"] != "yes" || crits[0]["o01"] != "no" || crits[1]["o00"] != "no" || crits[1]["o01"] != "yes" {
		t.Errorf("the options were not keyed by where they are shown: %v", crits)
	}
	if math.Abs(a.Probs[0]-0.5) > 1e-9 || a.Confidence > 1e-9 || math.Abs(a.LabelMass-0.9) > 1e-9 {
		t.Errorf("a server answering whatever is shown first read as %v, confidence %v, mass %v", a.Probs, a.Confidence, a.LabelMass)
	}
}

// Private first: a hosted Lead, or a connection on a public address or one
// whose address cannot be read, does not answer decisions; once every model
// is declared private it does, and says it is allowed rather than private.
// A local worker is private in its own right.
func TestOnlyAPrivateChoiceAnswersDecisions(t *testing.T) {
	adb := privateWorld(t)
	adb.Set(LLMTable, "provider", "llama.cpp")
	adb.Set(LLMTable, "endpoint", "http://127.0.0.1:8080")
	adb.Set(LeadLLMTable, "provider", "anthropic")
	worker, lead := &FakeLLM{}, &FakeLLM{}
	SetSharedLLMs(worker, lead)
	systemOneConnector(t, "far-away", "http://203.0.113.5/v1")
	if ok, own, why := Privacy(ProviderWorker); !ok || !own || For(ProviderWorker) == nil {
		t.Errorf("a local worker: ok %v own %v (%s)", ok, own, why)
	}
	for _, p := range []string{ProviderLead, "far-away", "no-such-connection"} {
		if ok, _, why := Privacy(p); ok || For(p) != nil {
			t.Errorf("%s answers decisions while not private (%s)", p, why)
		}
	}
	SetAllLLMsPrivate(true)
	for _, p := range []string{ProviderLead, "far-away"} {
		ok, own, why := Privacy(p)
		if !ok || own || For(p) == nil || !strings.Contains(why, "every model is declared private") {
			t.Errorf("%s with every model declared private: ok %v own %v (%s)", p, ok, own, why)
		}
	}
	if For("no-such-connection") != nil {
		t.Error("a connection that does not exist answers decisions")
	}
}
