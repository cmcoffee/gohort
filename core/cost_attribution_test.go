package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The ledger priced credential-dispatched calls and could not say WHO. A peer
// borrowing an image backend spends the operator's vendor quota, and the row
// read "cred:openai_images" exactly as it does when the operator renders
// something themselves — so the one question worth asking of a shared resource
// had no answer where cost is recorded.

// TestUnattributedWorkIsUnchanged — every existing caller, byte for byte.
func TestUnattributedWorkIsUnchanged(t *testing.T) {
	if got := costAttribution(context.Background()); got != "" {
		t.Errorf("a plain context claims attribution %q", got)
	}
	if got := costAttribution(nil); got != "" {
		t.Errorf("a nil context claims attribution %q", got)
	}
	// A blank label does not attribute — it would produce a row named after
	// nobody, which is worse than the unattributed row it replaced.
	if got := costAttribution(WithCostAttribution(context.Background(), "   ")); got != "" {
		t.Errorf("a blank label attributed to %q", got)
	}
}

// TestAttributionSurvivesTheContext — it is set in a handler and read several
// layers down, which is the whole reason it rides the context.
func TestAttributionSurvivesTheContext(t *testing.T) {
	ctx := WithCostAttribution(context.Background(), "peer:studio-mac")
	// Through a derived context, as a timeout or a cancel would produce.
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := costAttribution(derived); got != "peer:studio-mac" {
		t.Errorf("attribution lost through a derived context: %q", got)
	}
}

// TestAttributedCostGetsItsOwnRow — folding a peer's spend into the source's
// total would keep the sum right and answer nothing. The reason to record it is
// to separate what a peer spent from what the operator spent, and a combined
// row cannot be split apart afterwards.
func TestAttributedCostGetsItsOwnRow(t *testing.T) {
	src, err := os.ReadFile("cost_attribution.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, `RecordExternalCost(sourceID+" - "+who,`) {
		t.Error("attributed cost no longer gets a distinct source id — a peer's spend would be " +
			"folded into the operator's own and could not be separated again")
	}
	if !strings.Contains(body, "RecordExternalCost(sourceID, label, costPerCall)") {
		t.Error("the unattributed path is gone — every existing caller would change shape")
	}
}

// TestEveryMeteredPeerHandlerAttributes — a source sweep. The mechanism is
// worthless on a handler that forgets to stamp the context, and a handler
// cannot tell that it forgot.
func TestEveryMeteredPeerHandlerAttributes(t *testing.T) {
	// The capabilities that can spend a vendor's meter: a frontier image
	// backend bills per picture, search spends a metered key, and a model
	// request can reach a credentialed api-mode tool.
	for _, f := range []string{"peer_images.go", "peer_web.go", "peer_models.go"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "WithCostAttribution(") {
			t.Errorf("%s serves a metered capability and never attributes its cost — "+
				"a peer's spend records as the operator's own", f)
		}
	}
}

// TestEveryPeerHandlerCountsTheCall — investigate, knowledge and exec never
// touched the key, so they registered as zero activity forever. Exec is the
// widest grant in the system: a key that had run a hundred commands on somebody's
// lab box looked unused in the admin table.
func TestEveryPeerHandlerCountsTheCall(t *testing.T) {
	handler := regexp.MustCompile(`func (HandlePeer\w+)\(`)
	files, _ := os.ReadDir(".")
	counted := 0
	for _, e := range files {
		if !strings.HasPrefix(e.Name(), "peer_") || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		for _, m := range handler.FindAllStringSubmatch(body, -1) {
			name := m[1]
			// The manifest is a read of what is on offer, not use of anything.
			if name == "HandlePeerManifest" || name == "HandlePeerModels" {
				continue
			}
			i := strings.Index(body, "func "+name+"(")
			block := body[i:]
			if j := strings.Index(block[1:], "\nfunc "); j > 0 {
				block = block[:j]
			}
			counted++
			if !strings.Contains(block, "touchPeerKey(k)") {
				t.Errorf("%s does not count the call — this key's activity reads as zero "+
					"however much it does", name)
			}
		}
	}
	if counted == 0 {
		t.Fatal("found no peer handlers — the sweep is no longer looking where they live")
	}
}

// withSpendLedger wires a fresh ledger for per-person spending.
func withSpendLedger(t *testing.T) {
	t.Helper()
	costledger.SetCostLedgerDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { costledger.SetCostLedgerDB(nil) })
}

// A priced model call and a paid external call are charged to the person the
// work was done for, by its agent and key; an unpriced model (a local one)
// charges nothing.
func TestWhatWorkCostsIsChargedToItsPerson(t *testing.T) {
	withSpendLedger(t)
	withRates(t)
	ctx := costledger.WithSpender(context.Background(), costledger.Spender{Person: "alice", Agent: "writer", Key: "k1"})
	// A dollar per 1k tokens on the lead: 400 in + 100 out is $0.50.
	RecordUsage(ctx, LEAD, &Response{InputTokens: 400, OutputTokens: 100})
	RecordExternalCostFor(ctx, "cred:maps", "maps", 0.25)
	s := costledger.Spent(costledger.ThisMonth(), "alice")
	if s.Total < 0.749 || s.Total > 0.751 || s.Sources["cred:maps"] != 0.25 || s.Agents["writer"] < 0.749 || s.Keys["k1"] < 0.749 {
		t.Errorf("alice's month: %+v", s)
	}
	if lead := s.Sources["model:lead"]; lead < 0.499 || lead > 0.501 {
		t.Errorf("the lead call: %v", lead)
	}
	RecordUsage(ctx, WORKER, &Response{InputTokens: 1000, OutputTokens: 1000})
	if w := costledger.Spent(costledger.ThisMonth(), "alice").Sources["model:worker"]; w < 0.0019 || w > 0.0021 {
		t.Errorf("the worker priced at its own rate: %v", w)
	}

	prev := GetCostRates()
	SetCostRates(CostRates{})
	t.Cleanup(func() { SetCostRates(prev) })
	before := costledger.Spent(costledger.ThisMonth(), "alice").Total
	RecordUsage(ctx, LEAD, &Response{InputTokens: 9_000_000})
	if after := costledger.Spent(costledger.ThisMonth(), "alice").Total; after != before {
		t.Errorf("an unpriced deployment charged %v", after-before)
	}
}

// A request is paid for by the signed-in person, or by the owner of the API
// key that asked, with that key.
func TestARequestIsPaidForByItsPersonOrItsKeysOwner(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	r := asUser(t, "/x", "dana")
	if got := costledger.SpenderFrom(withRequestSpender(r)); got.Person != "dana" || got.Key != "" {
		t.Errorf("a signed-in request: %+v", got)
	}
	prevRoot := RootDB
	RootDB = AuthDB()
	t.Cleanup(func() { RootDB = prevRoot })
	tok := MintAccountTokenScoped("dana", "laptop", &TokenScope{Features: []string{"mcp"}})
	k := httptest.NewRequest("POST", "/mcp/", nil)
	k.Header.Set("X-API-Key", tok.Token)
	if got := costledger.SpenderFrom(withRequestSpender(k)); got.Person != "dana" || got.Key != tok.ID {
		t.Errorf("a key's request: %+v", got)
	}
	if got := costledger.SpenderFrom(withRequestSpender(httptest.NewRequest("GET", "/", nil))); got != (costledger.Spender{}) {
		t.Errorf("nobody's request: %+v", got)
	}
}

// Crossing 80% of the limit puts a notice on the person's bell.
func TestEightyPercentIsOnTheBell(t *testing.T) {
	withRootDB(t)
	costledger.OnWarn("alice", 8, 10)
	got := notices.List(RootDB, "alice")
	if len(got) != 1 || !strings.Contains(got[0].Title, "80%") || !strings.Contains(got[0].Body, "$8.00 of your $10.00") {
		t.Errorf("alice's bell: %+v", got)
	}
}

// Every app's requests carry who pays, from the middleware every app is
// mounted behind.
func TestEveryAppsRequestsCarryWhoPays(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	var got costledger.Spender
	h := UsageReportMiddleware("test")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = costledger.SpenderFrom(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), asUser(t, "/x/api/thing", "dana"))
	if got.Person != "dana" {
		t.Errorf("a request's payer: %+v", got)
	}
	got = costledger.Spender{}
	h.ServeHTTP(httptest.NewRecorder(), asUser(t, "/x/api/events", "dana"))
	if got.Person != "dana" {
		t.Errorf("a streaming request's payer: %+v", got)
	}
}
