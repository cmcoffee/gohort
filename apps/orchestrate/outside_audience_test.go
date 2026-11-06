package orchestrate

import (
	"context"
	"errors"
	"fmt"
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

// What reaches an outside person from underneath: its saved notes only when
// the owner shares them, read-only; never the owner's cortex or what the agent
// worked out across the owner's conversations, whatever the switches say.
// Published documents still reach them.
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
		// The saved notes follow the owner's "Its saved notes" switch, as for
		// anyone the agent is shared with: off by default, so what came up in
		// the owner's own chats does not reach every customer.
		if got, want := turn.readsOwnerFacts(), rec.ShareMemoryExplicit; got != want {
			t.Errorf("saved notes reach a person outside: %v, want %v (switch %v)", got, want, rec.ShareMemoryExplicit)
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

	// The owner shares the agent's notes: hers come first, the agent's under.
	turn := sharedTurn("alice", ann, AgentRecord{ID: "a1", OutsideAudience: true, ShareMemoryExplicit: true})
	turn.app, turn.udb = app, UserDB(root, ann)
	got := turn.facts()
	if len(got) != 2 || got[0].Note != "ann's order number is 4471" || got[1].Note != "answer billing questions only" {
		t.Fatalf("ann's prompt facts = %+v; want hers over the agent's", got)
	}
	// Not shared, the default: hers alone.
	plain := sharedTurn("alice", ann, AgentRecord{ID: "a1", OutsideAudience: true})
	plain.app, plain.udb = app, UserDB(root, ann)
	if got := plain.facts(); len(got) != 1 || got[0].Note != "ann's order number is 4471" {
		t.Fatalf("with the notes not shared, ann's prompt facts = %+v; want hers alone", got)
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
		"cr.loopCfg.SharedBudgets = append(cr.loopCfg.SharedBudgets, outsideBudget(cr.runtimeUser, cr.target), outsideAllBudget(cr.target))",
		"done, free := outsideOneAtATime(cr.runtimeUser, cr.target.ID)",
		"cr.loopCfg.Confirm, cr.loopCfg.ConfirmRefusal = outsideConfirm, outsideConfirmRefusal",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the continuing run no longer does: %s", want)
		}
	}
}

// A transport's person outside runs in their own store when the agent serves
// people outside gohort and they are not the owner. To an agent that does not
// serve them they are refused with a fixed reply, rather than run in the
// owner's own memory; the owner on their own bot runs as themselves.
func TestATransportsOutsiderRunsApartOnlyWhenTheAgentServesThem(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Happy to help.", Repeat: true})
	scope := agenthost.OutsideScope("teams-abc", "aad-ada")
	indexed := func() bool {
		return df.udb.Get(outsidePeopleTable, df.agent.ID+"|"+scope, &outsidePerson{})
	}

	// The agent does not serve outside: refused with the fixed reply, and no
	// turn runs. It used to run as the owner's, in the owner's own memory.
	calls := df.llm.Calls()
	if res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "t1", Message: "hi", OutsideScope: scope}); res.Text != outsiderNotServedReply {
		t.Errorf("someone else on Teams, to an agent that does not serve outside, got %q", res.Text)
	}
	if df.llm.Calls() != calls || indexed() {
		t.Error("a refused Teams sender still ran a turn")
	}
	// The owner on their own bot, the agent not serving outside: their run.
	if res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "t0", Message: "hi", OutsideScope: scope, SenderIsOwner: true}); res.Text == outsiderNotServedReply {
		t.Error("the owner was refused their own agent")
	}

	df.agent.OutsideAudience = true
	a, err := saveAgent(df.udb, df.agent)
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	// The owner, on their own bot: still their own run.
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: "t2", Message: "hi", OutsideScope: scope, SenderIsOwner: true})
	if indexed() {
		t.Error("the owner's own message ran apart from them")
	}
	// Anyone else: their own store.
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: "t3", Message: "hi", OutsideScope: scope})
	if !indexed() {
		t.Error("someone else on Teams did not run in their own store")
	}
}

// A proposal that is not an outside store is never taken as a user name.
func TestAProposedStoreMustBeAnOutsideOne(t *testing.T) {
	a := AgentRecord{ID: "a1", OutsideAudience: true}
	if got := outsideRunAs("alice", AgentSyncRun{OutsideScope: "bob"}, a); got != "alice" {
		t.Errorf("a proposed account name was run as: %q", got)
	}
	if got := outsideRunAs("alice", AgentSyncRun{OutsideScope: "outside:teams-abc:ann"}, a); got != "outside:teams-abc:ann" {
		t.Errorf("a proposed outside store was ignored: %q", got)
	}
}

// Through the channel runner: an inbound's proposed store reaches the run.
func TestAChannelInboundCarriesItsOutsideStore(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Happy to help.", Repeat: true})
	df.agent.OutsideAudience = true
	a, err := saveAgent(df.udb, df.agent)
	if err != nil {
		t.Fatal(err)
	}
	registerChannelAgentRunner(df.app)
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })
	scope := agenthost.OutsideScope("teams-abc", "conv-123")
	if _, err := RunChannelAgent(t.Context(), ChannelInbound{Owner: "u", AgentID: a.ID, SessionID: "teams:conv", ChatID: "19:conv", Handle: "aad-ada",
		SenderName: "Ada", Text: "hi", OutsideScope: scope}); err != nil {
		t.Fatal(err)
	}
	if !df.udb.Get(outsidePeopleTable, a.ID+"|"+scope, &outsidePerson{}) {
		t.Error("the channel runner dropped the inbound's outside store")
	}
}

// A person outside gohort never runs the agent's machine. Its steps run with
// the agent's whole catalog, the owner's credentials and an approval gate that
// reads the person's empty store, none of it the set lent to outside turns, so
// an end user's message reaching a step could steer it to the owner's Sources
// and tools. The owner's own turn on the same agent still gets its machine.
func TestAnOutsidePersonNeverRunsTheMachine(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	agent := AgentRecord{ID: "support", Name: "Support", Machine: "m-1", OutsideAudience: true}
	outside := &chatTurn{agent: agent, udb: udb, ctx: context.Background(),
		user: agenthost.OutsideScope("widget", "visitor-1"), session: &ChatSession{ID: "conv-1", AgentID: "support"}}
	if m := outside.enterMachine("hello"); m.on {
		t.Fatal("an outside turn entered the machine")
	}
	found := false
	for _, d := range parentTrailOf(udb, "support", "conv-1") {
		found = found || d.Kind == "machine_outside"
	}
	if !found {
		t.Errorf("the turn did not say why it answered without its machine: %+v", parentTrailOf(udb, "support", "conv-1"))
	}
	if got := outside.machineCatalog(MachinePhase{Name: "route"}); got != nil {
		t.Errorf("a machine step reached on an outside turn has %d tools", len(got))
	}

	owner := &chatTurn{agent: agent, udb: udb, ctx: context.Background(), user: "owner", session: &ChatSession{ID: "conv-2", AgentID: "support"}}
	owner.enterMachine("hello")
	for _, d := range parentTrailOf(udb, "support", "conv-2") {
		if d.Kind == "machine_outside" {
			t.Error("the owner's own turn was refused its machine")
		}
	}
}

// The bulletin boards an agent follows are the owner's, posted to by the
// owner's other agents: a person outside gohort is not shown them, as the
// cortex is not. Notes already on the turn pass through untouched.
func TestAnOutsidePersonIsNotShownTheOwnersBulletins(t *testing.T) {
	owner := &DBase{Store: kvlite.MemStore()}
	if err := saveBulletin(owner, bulletinBoard{Name: "news", TTLHours: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := postBulletin(owner, "news", "Quarter numbers land Friday", "Briefing Bot"); err != nil {
		t.Fatal(err)
	}
	agent := AgentRecord{ID: "support", Bulletins: []string{"news"}, OutsideAudience: true}
	own := &chatTurn{agent: agent, udb: owner, user: "owner"}
	if !strings.Contains(own.withBulletins(""), "Quarter numbers land Friday") {
		t.Fatal("the owner's own turn should carry the board it follows")
	}
	outside := &chatTurn{agent: agent, udb: &DBase{Store: kvlite.MemStore()}, user: agenthost.OutsideScope("widget", "visitor-1"),
		ownerDB: owner, ownerUser: "owner"}
	if got := outside.withBulletins("the turn's own notes"); got != "the turn's own notes" {
		t.Errorf("an outside turn was shown the owner's board: %q", got)
	}
}

// The per-person limits are only as good as the person id, and a platform can
// give every visitor a fresh one. An administrator's ceiling counts every
// outside person on an agent together: past it, a brand-new id is refused too.
func TestFreshIdsDoNotAddUpToAnOpenAgent(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, tuneOutsideAllPerDay, float64(10))
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })

	a := AgentRecord{ID: "a1", OutsideAudience: true}
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		person := agenthost.OutsideScope("widget", fmt.Sprintf("visitor-%d", i))
		if ok, why := outsideAdmit(&DBase{Store: kvlite.MemStore()}, person, a, now); !ok {
			t.Fatalf("visitor %d refused under the ceiling: %s", i, why)
		}
	}
	ok, why := outsideAdmit(&DBase{Store: kvlite.MemStore()}, agenthost.OutsideScope("widget", "visitor-new"), a, now)
	if ok || !strings.Contains(why, "everyone outside together") {
		t.Errorf("a fresh id past the agent's ceiling: ok=%v %q", ok, why)
	}
	if ok, _ := outsideAdmit(&DBase{Store: kvlite.MemStore()}, agenthost.OutsideScope("widget", "visitor-other-agent"), AgentRecord{ID: "a2", OutsideAudience: true}, now); !ok {
		t.Error("one agent's ceiling held another agent")
	}
	if b := outsideAllBudget(a); b.DailySpendUSD <= 0 || b.Key != "outside-all|a1" {
		t.Errorf("the spend ceiling: %+v", b)
	}
}

// One turn at a time each: a person with a turn running is told so, and the
// turn is theirs again once it ends, however often it is given back.
func TestAPersonOutsideHasOneTurnAtATime(t *testing.T) {
	done, ok := outsideOneAtATime("outside:k:ann", "a1")
	if !ok {
		t.Fatal("the first turn was refused")
	}
	if _, ok := outsideOneAtATime("outside:k:ann", "a1"); ok {
		t.Error("a second turn ran beside the first")
	}
	if other, ok := outsideOneAtATime("outside:k:ben", "a1"); !ok {
		t.Error("one person's turn held another")
	} else {
		other()
	}
	done()
	done()
	again, ok := outsideOneAtATime("outside:k:ann", "a1")
	if !ok {
		t.Fatal("the turn was not given back")
	}
	again()
}

// An outside turn is not told what else the owner runs: the trigger hints that
// name the owner's other agents, and the owner's custom tools listed for
// Builder, are left out of it. Pinned at their one site each.
func TestAnOutsideTurnIsNotShownTheOwnersFleetOrTools(t *testing.T) {
	src, err := os.ReadFile("dispatch_catalog.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"if !t.outsideAudience() {\n\t\tout += t.renderAgentTriggerHints(message)",
		"func (t *chatTurn) dispatchCatalogPrompt() string {\n\t// Not for a person outside gohort",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("dispatch_catalog.go no longer guards: %q", want)
		}
	}
	if got := (&chatTurn{agent: AgentRecord{ID: "a1"}, user: agenthost.OutsideScope("w", "v")}).dispatchCatalogPrompt(); got != "" {
		t.Errorf("an outside turn was given the Builder tools block: %q", got)
	}
}

// On an outside turn a call that would need the owner's go-ahead is refused,
// never queued for the owner with a stranger's arguments, and the model is
// told it is not available rather than that it was denied.
func TestAnOutsideTurnAsksTheOwnerNothing(t *testing.T) {
	if outsideConfirm("send_email", `{"to":"anyone"}`) {
		t.Error("an outside turn's call needing approval was let through")
	}
	if why := outsideConfirmRefusal("send_email"); !strings.Contains(why, "send_email") || !strings.Contains(why, "not available") {
		t.Errorf("refusal: %q", why)
	}
}

// A save lends only what the Access page offers: a hand-made PATCH naming
// one of the framework's own tools (which write to the owner's stores) is
// dropped, while a name lent before stays until it is unticked.
func TestASaveLendsOnlyWhatThePickerOffers(t *testing.T) {
	offered := ""
	for _, o := range outsideToolOptions("u", AgentRecord{}) {
		offered = o.Value
		break
	}
	if offered == "" {
		t.Skip("no worker tools registered in this build")
	}
	a := AgentRecord{OutsideTools: []string{offered, "knowledge_save", "store_fact", offered}}
	got := lendableOutsideTools("u", a, nil)
	if len(got) != 1 || got[0] != offered {
		t.Fatalf("lent %v, want only %q", got, offered)
	}
	got = lendableOutsideTools("u", a, []string{"store_fact"})
	if len(got) != 2 || got[1] != "store_fact" {
		t.Fatalf("a name lent before was dropped: %v", got)
	}
	src, err := os.ReadFile("agents_http.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"req.OutsideTools = lendableOutsideTools(user, req, prev.OutsideTools)",
		"rec.OutsideTools = lendableOutsideTools(user, rec, existing.OutsideTools)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("agents_http.go no longer holds a save to the picker: missing %q", want)
		}
	}
}
