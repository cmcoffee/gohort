package core

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestPrivateStagePinIsOnByDefault — the whole safety property. A deployment
// that has never seen this setting must behave exactly as it did before it
// existed: private means worker, always.
func TestPrivateStagePinIsOnByDefault(t *testing.T) {
	withPrivacyDB(t)
	RegisterRouteStage(RouteStage{Key: "privtest.pinned", Label: "Pinned", Default: "worker", Private: true})

	if AllLLMsPrivate() {
		t.Fatal("all-LLMs-private defaults to ON — a fresh deployment would let private stages escalate")
	}
	if !PrivateStageEnforced("privtest.pinned") {
		t.Error("a private stage is not enforced by default")
	}
	if RouteToLead("privtest.pinned") {
		t.Error("a private stage routed to lead with the toggle off")
	}
}

// TestTheToggleIsTheOnlyThingThatLifts — a private stage escalates when, and
// only when, the operator has said every model is private. Tested through
// RouteToLead because that is the single enforcement point; anything that
// bypasses it is a hole this test cannot see, which is why the pin lives there
// and nowhere else.
func TestTheToggleIsTheOnlyThingThatLifts(t *testing.T) {
	db := withPrivacyDB(t)
	RegisterRouteStage(RouteStage{Key: "privtest.lift", Label: "Lift", Default: "lead", Private: true})
	// Route the stage to lead explicitly; the pin is what should override it.
	db.Set(RoutingTable, "privtest.lift", "lead")
	prev := LookupRouteFunc
	LookupRouteFunc = func(key string) string {
		var v string
		db.Get(RoutingTable, key, &v)
		return v
	}
	t.Cleanup(func() { LookupRouteFunc = prev })

	if RouteToLead("privtest.lift") {
		t.Fatal("the pin did not hold with the toggle off")
	}
	SetAllLLMsPrivate(true)
	if !AllLLMsPrivate() {
		t.Fatal("the setting did not persist")
	}
	if !RouteToLead("privtest.lift") {
		t.Error("with every model private, a private stage still could not reach the lead tier — " +
			"which is the entire point of the toggle")
	}
	if PrivateStageEnforced("privtest.lift") {
		t.Error("PrivateStageEnforced still reports the pin as binding, so the UI would keep hiding " +
			"a lead option the runtime now accepts")
	}
	// Registration is a property of the app and must NOT change with the toggle
	// — an app declares its stage private once, and the deployment decides
	// separately whether that pin currently binds.
	if !IsPrivateStage("privtest.lift") {
		t.Error("the stage stopped being registered private — the two questions have been conflated")
	}
	SetAllLLMsPrivate(false)
	if RouteToLead("privtest.lift") {
		t.Error("turning the toggle back off did not restore the pin")
	}
}

// TestNonPrivateStagesAreUnaffected — the toggle must not be a global routing
// change. A stage that was never private keeps whatever the routing table says,
// in both positions.
func TestNonPrivateStagesAreUnaffected(t *testing.T) {
	db := withPrivacyDB(t)
	RegisterRouteStage(RouteStage{Key: "privtest.open", Label: "Open", Default: "worker"})
	prev := LookupRouteFunc
	LookupRouteFunc = func(key string) string {
		var v string
		db.Get(RoutingTable, key, &v)
		return v
	}
	t.Cleanup(func() { LookupRouteFunc = prev })

	for _, on := range []bool{false, true} {
		SetAllLLMsPrivate(on)
		db.Set(RoutingTable, "privtest.open", "worker")
		if RouteToLead("privtest.open") {
			t.Errorf("all_private=%v: a worker-routed open stage went to lead", on)
		}
		db.Set(RoutingTable, "privtest.open", "lead")
		if !RouteToLead("privtest.open") {
			t.Errorf("all_private=%v: a lead-routed open stage did not go to lead", on)
		}
	}
}

// TestHostedProvidersAreNeverJudgedPrivate — the judgement that matters most.
// A hosted provider pointed at a loopback address is a proxy TO a third party,
// not a local model, and calling it private is how credentials would leak.
func TestHostedProvidersAreNeverJudgedPrivate(t *testing.T) {
	for _, p := range []string{"anthropic", "openai", "gemini", "bedrock", "OpenAI", " anthropic "} {
		for _, ep := range []string{"", "http://127.0.0.1:8080/v1", "http://localhost:1234", "https://api.example.com"} {
			if ok, why := ProviderLooksPrivate(p, ep); ok {
				t.Errorf("provider %q at %q was judged private (%s)", p, ep, why)
			}
		}
	}
}

// TestLocalProviderJudgements — where the endpoint decides.
func TestLocalProviderJudgements(t *testing.T) {
	cases := []struct {
		endpoint string
		private  bool
	}{
		{"", true},                             // provider default is loopback
		{"http://localhost:8080/v1", true},     //
		{"http://127.0.0.1:8080/v1", true},     //
		{"127.0.0.1:8080", true},               // no scheme
		{"http://[::1]:8080/v1", true},         //
		{"http://192.168.1.50:8080/v1", true},  // private network
		{"http://10.0.0.4:11434", true},        //
		{"http://den.local:8080/v1", true},     // mDNS name
		{"http://den:8080/v1", true},           // bare hostname on the LAN
		{"https://api.together.xyz/v1", false}, // a public host
		{"https://8.8.8.8/v1", false},          // a public address
	}
	for _, c := range cases {
		got, why := ProviderLooksPrivate("llama.cpp", c.endpoint)
		if got != c.private {
			t.Errorf("llama.cpp at %q: judged private=%v, want %v (%s)", c.endpoint, got, c.private, why)
		}
	}
}

// TestEveryVerdictExplainsItself — the recommendation is advice an operator has
// to weigh, and a bare yes/no gives them nothing to weigh it against.
func TestEveryVerdictExplainsItself(t *testing.T) {
	db := withPrivacyDB(t)
	db.Set(LLMTable, "provider", "llama.cpp")
	db.Set(LLMTable, "endpoint", "http://127.0.0.1:8080/v1")
	db.Set(LeadLLMTable, "provider", "anthropic")

	recommended, verdicts := RecommendAllLLMsPrivate()
	if recommended {
		t.Error("a hosted lead was recommended as all-private")
	}
	if len(verdicts) != 2 {
		t.Fatalf("got %d verdicts, want one per tier", len(verdicts))
	}
	for _, v := range verdicts {
		if strings.TrimSpace(v.Reason) == "" {
			t.Errorf("verdict for %s has no reason", v.Tier)
		}
	}
	// Both local: recommended.
	db.Set(LeadLLMTable, "provider", "llama.cpp")
	db.Set(LeadLLMTable, "endpoint", "http://127.0.0.1:8081/v1")
	if ok, _ := RecommendAllLLMsPrivate(); !ok {
		t.Error("two loopback tiers were not recommended as all-private")
	}
	// An unconfigured lead falls through to the worker and must not be judged
	// as a separate remote endpoint.
	db.Unset(LeadLLMTable, "provider")
	db.Unset(LeadLLMTable, "endpoint")
	ok, verdicts := RecommendAllLLMsPrivate()
	if !ok {
		t.Error("an unconfigured lead broke the recommendation, but it just runs the worker model")
	}
	for _, v := range verdicts {
		if v.Tier == "Lead" && !strings.Contains(v.Reason, "not configured") {
			t.Errorf("an unconfigured lead should say so; got %q", v.Reason)
		}
	}
}

// withPrivacyDB points AuthDB at a scratch store and restores it after.
func withPrivacyDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	return db
}
