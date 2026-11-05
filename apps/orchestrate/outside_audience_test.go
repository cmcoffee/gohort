package orchestrate

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A person outside gohort reaches only an agent its owner set to serve them;
// anyone else's run is unaffected by the setting.
func TestAnOutsidePersonReachesOnlyAnAgentSetToServeThem(t *testing.T) {
	person := agenthost.OutsideScope("key1", "cust-7")
	if err := outsideRunRefusal(person, AgentRecord{ID: "a1"}); !errors.Is(err, agenthost.ErrNotForOutsideAudience) {
		t.Errorf("an agent not set to serve people outside ran for one: %v", err)
	}
	if err := outsideRunRefusal(person, AgentRecord{ID: "a1", OutsideAudience: true}); err != nil {
		t.Errorf("an agent set to serve people outside refused one: %v", err)
	}
	for _, user := range []string{"alice", "phantom:991", "app:servitor:x"} {
		if err := outsideRunRefusal(user, AgentRecord{ID: "a1"}); err != nil {
			t.Errorf("%s's run was refused by the outside setting: %v", user, err)
		}
	}
}

// What reaches an outside person from underneath is fixed, whatever the
// agent's share switches say: its saved notes, read-only, so it knows its job;
// never the owner's cortex or what the agent worked out across the owner's
// conversations. Published documents still reach them.
func TestAnOutsidePersonGetsTheAgentsNotesAndNothingOfTheOwners(t *testing.T) {
	person := agenthost.OutsideScope("key1", "cust-7")
	for _, rec := range []AgentRecord{
		{ID: "a1", Cortex: true, OutsideAudience: true},                            // defaults
		{ID: "a1", Cortex: true, OutsideAudience: true, ShareMemoryExplicit: true}, // every switch open
	} {
		turn := sharedTurn("alice", person, rec)
		if !turn.outsideAudience() {
			t.Fatal("the turn does not read as an outside person's")
		}
		if !turn.readsOwnerFacts() {
			t.Error("the agent's saved notes did not come with it")
		}
		if turn.readsOwnerCortex() {
			t.Error("the owner's cortex reached a person outside")
		}
		if turn.readsOwnerReference() || turn.readsOwnerCorpus(ChunkScopeDerivedOnly) || turn.readsOwnerCorpus(ChunkScopeAll) {
			t.Error("what the agent worked out across the owner's conversations reached a person outside")
		}
		if !turn.readsOwnerCorpus(ChunkScopeCuratedOnly) {
			t.Error("the agent's published documents did not reach a person outside")
		}
	}
	// A recipient who is a gohort user is still governed by the switches.
	if sharedTurn("alice", "bob", AgentRecord{ID: "a1"}).readsOwnerFacts() {
		t.Error("the outside rule reached an ordinary recipient")
	}
}

// Their memory is theirs: the prompt carries their notes over the agent's,
// and nothing they leave lands in the owner's store or another person's.
func TestAnOutsidePersonsNotesStayTheirs(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	ns := factsNamespace("a1")
	StoreMemoryFact(UserDB(root, "alice"), ns, "answer billing questions only")
	ann, ben := agenthost.OutsideScope("key1", "ann"), agenthost.OutsideScope("key1", "ben")
	StoreMemoryFact(UserDB(root, ann), ns, "ann's order number is 4471")

	turn := sharedTurn("alice", ann, AgentRecord{ID: "a1", OutsideAudience: true})
	turn.app, turn.udb = app, UserDB(root, ann)
	got := turn.facts()
	if len(got) != 2 || got[0].Note != "ann's order number is 4471" || got[1].Note != "answer billing questions only" {
		t.Fatalf("ann's prompt facts = %+v; want hers over the agent's", got)
	}

	other := sharedTurn("alice", ben, AgentRecord{ID: "a1", OutsideAudience: true})
	other.app, other.udb = app, UserDB(root, ben)
	for _, f := range other.facts() {
		if f.Note == "ann's order number is 4471" {
			t.Error("one outside person's note reached another")
		}
	}
	for _, f := range ListMemoryFacts(UserDB(root, "alice"), ns) {
		if f.Note == "ann's order number is 4471" {
			t.Error("an outside person's note reached the owner's store")
		}
	}
}

// offeredTools is every tool name the model was offered across the calls.
func offeredTools(llm *FakeLLM) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < llm.Calls(); i++ {
		for _, tl := range llm.Config(i).Tools {
			out[tl.Name] = true
		}
	}
	return out
}

// A person outside is offered their own memory and the utilities, and of the
// rest only what the owner lent: not the caller's tools, not the owner's
// other agents. The owner's own run of the same agent is unchanged.
func TestAnOutsideTurnReachesOnlyWhatWasLent(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Happy to help.", Repeat: true})
	df.agent.OutsideAudience = true
	a, err := saveAgent(df.udb, df.agent)
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	person := agenthost.OutsideScope("key1", "ann")

	df.run(AgentSyncRun{RuntimeUser: person, SubSessionID: "p1", Message: "where is my order"})
	got := offeredTools(df.llm)
	if got["lookup_order"] || got["agents"] {
		t.Errorf("an outside person was offered what nobody lent: %v", got)
	}
	for name := range got {
		lent := false
		for _, n := range outsideAlwaysTools {
			lent = lent || n == name
		}
		if !lent && name != "ask_user" {
			t.Errorf("an outside person was offered %q, which nobody lent", name)
		}
	}
	if !got["remember"] || !got["recall"] {
		t.Errorf("an outside person lost their own memory: %v", got)
	}

	df.agent.OutsideTools = []string{"lookup_order"}
	if df.agent, err = saveAgent(df.udb, df.agent); err != nil {
		t.Fatal(err)
	}
	df.llm.Turns = []FakeTurn{{Content: "Happy to help.", Repeat: true}}
	before := df.llm.Calls()
	df.run(AgentSyncRun{RuntimeUser: person, SubSessionID: "p2", Message: "and now"})
	lentNow := false
	for i := before; i < df.llm.Calls(); i++ {
		for _, tl := range df.llm.Config(i).Tools {
			lentNow = lentNow || tl.Name == "lookup_order"
		}
	}
	if !lentNow {
		t.Error("a lent tool was not offered to a person outside")
	}

	before = df.llm.Calls()
	df.run(AgentSyncRun{SubSessionID: "own", Message: "owner asking"})
	ownerHas := false
	for i := before; i < df.llm.Calls(); i++ {
		for _, tl := range df.llm.Config(i).Tools {
			ownerHas = ownerHas || tl.Name == "lookup_order"
		}
	}
	if !ownerHas {
		t.Error("the owner's own run lost a tool to the outside rules")
	}
}

// What is lent is still not offered when it would ask first: nobody on the
// other end can say yes.
func TestAnAskFirstToolIsNeverOfferedOutside(t *testing.T) {
	tools := []AgentToolDef{
		{Tool: Tool{Name: "recall"}},
		{Tool: Tool{Name: "lookup_order"}},
		{Tool: Tool{Name: "refund"}},
		{Tool: Tool{Name: "send_message"}, NeedsConfirm: true},
		{Tool: Tool{Name: "web_search"}},
	}
	a := AgentRecord{ID: "a1", OutsideTools: []string{"lookup_order", "refund", "send_message"}}
	kept := outsideKit(tools, a, func(name string) bool { return name == "refund" })
	var names []string
	for _, td := range kept {
		names = append(names, td.Tool.Name)
	}
	if strings.Join(names, ",") != "recall,lookup_order" {
		t.Errorf("kit = %v; want recall and the lent tool that asks nothing", names)
	}
}

// A person outside reaches no credential the owner did not lend, whether it is
// the deployment's or the owner's own.
func TestAnOutsidePersonReachesOnlyLentCredentials(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	for _, c := range []SecureCredential{
		{Name: "shop_api", Type: SecureCredBearer, BaseURL: "https://shop.test"},
		{Name: "mail", Type: SecureCredBearer, BaseURL: "https://mail.test"},
		{Name: "alice_bank", Type: SecureCredBearer, BaseURL: "https://bank.test", Owner: "alice"},
	} {
		if err := Secure().Save(c, "t"); err != nil {
			t.Fatal(err)
		}
	}
	deny := outsideCredentialDeny(map[string]bool{"old": true}, "alice", AgentRecord{ID: "a1", OutsideCredentials: []string{"shop_api"}})
	if deny["shop_api"] {
		t.Error("the lent credential was denied")
	}
	for _, n := range []string{"mail", "alice_bank", "old"} {
		if !deny[n] {
			t.Errorf("%s reached a person outside", n)
		}
	}
}

// Past a limit a person gets the fixed reply and no turn runs; the owner gets
// a notice. The refused turn is not counted against them.
func TestAPersonOverALimitGetsTheFixedReply(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Happy to help.", Repeat: true})
	df.agent.OutsideAudience, df.agent.OutsidePerMinute = true, 2
	a, err := saveAgent(df.udb, df.agent)
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	person := agenthost.OutsideScope("key1", "ann")

	for i := 0; i < 2; i++ {
		if res := df.run(AgentSyncRun{RuntimeUser: person, SubSessionID: "p", Message: "hello"}); res.Text == outsideLimitReply {
			t.Fatalf("turn %d hit the limit early", i+1)
		}
	}
	calls := df.llm.Calls()
	if res := df.run(AgentSyncRun{RuntimeUser: person, SubSessionID: "p", Message: "hello again"}); res.Text != outsideLimitReply {
		t.Errorf("over the limit the person got %q", res.Text)
	}
	if df.llm.Calls() != calls {
		t.Error("a turn over the limit still called the model")
	}
	if len(notices.List(RootDB, "u")) != 1 {
		t.Errorf("the owner was not told once: %+v", notices.List(RootDB, "u"))
	}
	// Someone else is unaffected.
	if res := df.run(AgentSyncRun{RuntimeUser: agenthost.OutsideScope("key1", "ben"), SubSessionID: "q", Message: "hello"}); res.Text == outsideLimitReply {
		t.Error("one person's limit held another")
	}
}

// The day's count is kept in the person's store and rolls: turns older than a
// day stop counting, and a refused turn is not one.
func TestTheDayLimitRolls(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	a := AgentRecord{ID: "a1", OutsideAudience: true, OutsidePerDay: 2, OutsidePerMinute: 100}
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{start, start.Add(time.Hour)} {
		if ok, why := outsideAdmit(db, "outside:k:ann", a, at); !ok {
			t.Fatalf("turn %d refused: %s", i+1, why)
		}
	}
	if ok, _ := outsideAdmit(db, "outside:k:ann", a, start.Add(2*time.Hour)); ok {
		t.Error("a third turn in a day was let through")
	}
	if ok, why := outsideAdmit(db, "outside:k:ann", a, start.Add(24*time.Hour+time.Minute)); !ok {
		t.Errorf("a day on, the first turn has aged out, yet: %s", why)
	}
	if ok, _ := outsideAdmit(db, "outside:k:ann", a, start.Add(24*time.Hour+2*time.Minute)); ok {
		t.Error("the refused turn was counted as room, or the second has aged out early")
	}
	// Zero limits are the defaults, not unlimited.
	if outsidePerMinute(AgentRecord{}) != outsideDefaultPerMinute || outsidePerDay(AgentRecord{}) != outsideDefaultPerDay || outsideSpendUSD(AgentRecord{}) != outsideDefaultSpendUSD {
		t.Error("an unset limit is not the default")
	}
}

// Three parts of an outside turn no whole-turn test here reaches: the
// credentials it may not use, no custom tool found mid-turn, and the person's
// spend counted under the key outsideAdmit checks. Pinned at their one site.
func TestAnOutsideRunIsWiredForCredentialsToolsAndSpend(t *testing.T) {
	src, err := os.ReadFile("agent_dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cr.subSess.DeniedCredentials = outsideCredentialDeny(cr.subSess.DeniedCredentials, cr.agentOwner, cr.target)",
		"cr.loopCfg.ToolFallbackResolver, cr.loopCfg.DynamicTools = nil, nil",
		"cr.loopCfg.SharedBudgets = append(cr.loopCfg.SharedBudgets, outsideBudget(cr.runtimeUser, cr.target))",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the continuing run no longer does: %s", want)
		}
	}
}
