package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Two agents with the same name used to resolve to whichever listAgents
// emitted first, and that order was not even stable. A name that answers to
// two agents must be refused with both ids, never guessed.
func TestSameNamedAgentsAreAmbiguousNotFirstMatch(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	a1, err := saveAgent(db, AgentRecord{Name: "Report Writer", Owner: owner, OrchestratorPrompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := saveAgent(db, AgentRecord{Name: "report writer", Owner: owner, OrchestratorPrompt: "two"})
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := resolveAgentRef(db, owner, "Report Writer")
	if ok || err == nil {
		t.Fatalf("a name two agents answer to resolved (ok=%v err=%v)", ok, err)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("the ambiguity error does not name candidate %s: %v", id, err)
		}
	}
	if _, ok := findAgentByNameOrID(db, owner, "Report Writer"); ok {
		t.Error("findAgentByNameOrID picked one of two same-named agents")
	}
	// Each id still addresses exactly its own agent.
	if got, ok := findAgentByNameOrID(db, owner, a2.ID); !ok || got.ID != a2.ID {
		t.Errorf("lookup by id resolved to %q", got.ID)
	}
}

// Destructive tools must never act on an ambiguous name: delete_agent used to
// delete whichever same-named agent sorted first.
func TestDeleteAndUpdateRefuseAnAmbiguousName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	a1, _ := saveAgent(db, AgentRecord{Name: "Dup", Owner: owner, OrchestratorPrompt: "one"})
	a2, _ := saveAgent(db, AgentRecord{Name: "Dup", Owner: owner, OrchestratorPrompt: "two"})
	sess := &ToolSession{Username: owner, DB: db}

	_, err := (deleteAgentTool{}).RunWithSession(map[string]any{"id": "Dup"}, sess)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("delete_agent on an ambiguous name: err=%v", err)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		if _, ok := loadAgent(db, id); !ok {
			t.Errorf("delete_agent removed %s on an ambiguous name", id)
		}
	}
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "Dup", "description": "x"}, sess); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("update_agent on an ambiguous name: err=%v", err)
	}
	if _, err := (cloneAgentTool{}).RunWithSession(map[string]any{"id": "Dup", "name": "Copy"}, sess); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("clone_agent on an ambiguous name: err=%v", err)
	}
	// By id the same call goes through.
	if _, err := (deleteAgentTool{}).RunWithSession(map[string]any{"id": a1.ID}, sess); err != nil {
		t.Fatalf("delete_agent by id: %v", err)
	}
	if _, ok := loadAgent(db, a2.ID); !ok {
		t.Fatal("deleting one id took the other same-named agent too")
	}
}

// A stronger match in ANY group beats a weaker match in an earlier one. The
// old cascade ran every tier over the user's own agents first, so an own agent
// matching only by prefix beat a shared agent whose name was typed in full.
func TestAnExactSharedNameBeatsAPartialOwnName(t *testing.T) {
	own := []AgentRecord{{ID: "own-pro", Name: "Market Scan Pro", Owner: "u"}}
	shared := []AgentRecord{{ID: "shared-exact", Name: "Market Scan", Owner: "someone"}}
	got, ok, err := resolveAgentName(own, shared, nil, "Market Scan", "u")
	if err != nil || !ok || got.ID != "shared-exact" {
		t.Fatalf("resolved to %q (ok=%v err=%v), want the exact shared match", got.ID, ok, err)
	}
}

// Own and shared are one rank: the same name in both is a choice for the
// caller, and the error says whose each one is.
func TestOwnAndSharedSameNameIsAmbiguous(t *testing.T) {
	own := []AgentRecord{{ID: "mine", Name: "Triage", Owner: "u"}}
	shared := []AgentRecord{{ID: "theirs", Name: "Triage", Owner: "someone"}}
	_, ok, err := resolveAgentName(own, shared, nil, "Triage", "u")
	if ok || err == nil {
		t.Fatalf("own+shared tie resolved (ok=%v err=%v)", ok, err)
	}
	msg := err.Error()
	for _, want := range []string{"id=mine", "id=theirs", "shared by someone"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ambiguity error missing %q: %s", want, msg)
		}
	}
	// The deliberate exception stands: the user's own agent beats a framework
	// agent of the same name.
	fw := []AgentRecord{{ID: "seed-x", Name: "Triage"}}
	got, ok, err := resolveAgentName(own, nil, fw, "Triage", "u")
	if err != nil || !ok || got.ID != "mine" {
		t.Fatalf("own vs framework resolved to %q (ok=%v err=%v), want the user's own", got.ID, ok, err)
	}
}

// listAgents orders same-named agents by id, so every surface that shows
// them shows them the same way on every run.
func TestListAgentsOrdersSameNamesByID(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	for _, id := range []string{"zz-3", "aa-1", "mm-2"} {
		if _, err := saveAgent(db, AgentRecord{ID: id, Name: "Same", Owner: owner, OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, a := range listAgents(db, owner) {
		if a.Name == "Same" {
			got = append(got, a.ID)
		}
	}
	if strings.Join(got, ",") != "aa-1,mm-2,zz-3" {
		t.Fatalf("same-named agents listed as %v, want id order", got)
	}
}

// Agent import used to skip only an exact-case clash, so "research agent"
// landed beside "Research Agent". And a skipped clash must say what it does
// to the bundle's pipelines and machines, which name the agent and will now
// run the importer's own.
func TestAgentImportNameClashIsCaseBlindAndSaysWhatItBinds(t *testing.T) {
	app := pipelineArtifactApp(t)
	udb := UserDB(app.DB, "alice")
	if _, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Research Agent", OrchestratorPrompt: "mine"}); err != nil {
		t.Fatal(err)
	}
	recipe, _ := json.Marshal(agentExport{AgentRecord: AgentRecord{Name: "research agent", OrchestratorPrompt: "theirs"}})
	name, skip, err := (&agentArtifact{app: app}).ImportArtifact(nil, recipe, "alice")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if skip == "" {
		t.Fatalf("%q imported beside an agent differing only in case", name)
	}
	for _, want := range []string{"Research Agent", "pipeline or machine"} {
		if !strings.Contains(skip, want) {
			t.Errorf("skip reason does not mention %q: %s", want, skip)
		}
	}
	n := 0
	for _, a := range listAgents(udb, "alice") {
		if strings.EqualFold(a.Name, "research agent") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one research agent after the skipped import, got %d", n)
	}
	// And the preview's existence probe agrees with the import.
	if _, err := (&agentArtifact{app: app}).ExportArtifact(nil, "research agent", "alice"); err != nil {
		t.Errorf("a case-drifted name does not read as existing, so the preview would predict an import: %v", err)
	}
}

// A pipeline NAME on a dispatch list is pinned to the id it meant when saved,
// so a different pipeline later given that name is not authorized by it.
func TestDispatchTargetNamePinsToIDOnSave(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	orig := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Weekly Digest",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x"}}})
	a, err := saveAgent(db, AgentRecord{Name: "Caller", Owner: owner, OrchestratorPrompt: "p",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"weekly digest"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.AllowedDispatchTargets) != 1 || a.AllowedDispatchTargets[0] != orig.ID {
		t.Fatalf("saved list = %v, want the pipeline id %s", a.AllowedDispatchTargets, orig.ID)
	}
	DeletePipelineDef(db, orig.ID)
	later := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Weekly Digest",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "y"}}})
	got, _ := loadAgent(db, a.ID)
	if dispatchListNames(got.AllowedDispatchTargets, later.ID, later.Name) {
		t.Fatal("a pipeline created later under the same name is authorized by the old grant")
	}
}

// A row saved before pinning existed still names the pipeline. The first read
// pins it to what carries the name then, and a namesake made afterwards is not
// on the list.
func TestLegacyDispatchTargetNamePinsOnRead(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	orig := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x"}}})
	legacy := AgentRecord{ID: "legacy-caller", Name: "Caller", Owner: owner, OrchestratorPrompt: "p",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"Nightly"}}
	db.Set(agentsTable, legacy.ID, legacy) // raw, as an older build wrote it
	got, ok := loadAgent(db, legacy.ID)
	if !ok || len(got.AllowedDispatchTargets) != 1 || got.AllowedDispatchTargets[0] != orig.ID {
		t.Fatalf("legacy list read as %v, want pinned to %s", got.AllowedDispatchTargets, orig.ID)
	}
	var raw AgentRecord
	db.Get(agentsTable, legacy.ID, &raw)
	if len(raw.AllowedDispatchTargets) != 1 || raw.AllowedDispatchTargets[0] != orig.ID {
		t.Fatalf("pin was not persisted: %v", raw.AllowedDispatchTargets)
	}
	DeletePipelineDef(db, orig.ID)
	later := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "y"}}})
	got, _ = loadAgent(db, legacy.ID)
	if dispatchListNames(got.AllowedDispatchTargets, later.ID, later.Name) {
		t.Fatal("a legacy name entry authorized a pipeline created after it was pinned")
	}
	// Agent ids on the list are never touched.
	b, _ := saveAgent(db, AgentRecord{Name: "Target", Owner: owner, OrchestratorPrompt: "p"})
	c, _ := saveAgent(db, AgentRecord{Name: "Caller2", Owner: owner, OrchestratorPrompt: "p",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{b.ID}})
	if len(c.AllowedDispatchTargets) != 1 || c.AllowedDispatchTargets[0] != b.ID {
		t.Fatalf("an agent id on the list was rewritten: %v", c.AllowedDispatchTargets)
	}
}

// A listing that addresses agents by name prints the id beside the ones whose
// names collide, and only those.
func TestAgentListingRefShowsIDOnlyOnCollision(t *testing.T) {
	list := []AgentRecord{
		{ID: "a1", Name: "Scout", Owner: "u"},
		{ID: "a2", Name: "scout", Owner: "someone"},
		{ID: "a3", Name: "Editor", Owner: "u"},
	}
	col := agentNameCollisions(list)
	if got := agentListingRef(list[0], col, "u"); got != "Scout (id=a1)" {
		t.Errorf("own colliding ref = %q", got)
	}
	if got := agentListingRef(list[1], col, "u"); got != "scout (id=a2, shared by someone)" {
		t.Errorf("shared colliding ref = %q", got)
	}
	if got := agentListingRef(list[2], col, "u"); got != "Editor" {
		t.Errorf("a unique name should print bare, got %q", got)
	}
}

// Attaching a machine by agent name took the first of several same-named
// agents. It now refuses and names the ids.
func TestMachineAttachRefusesAnAmbiguousAgentName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	udb := agentUserDB(db, "alice")
	for _, id := range []string{"dup-1", "dup-2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Owner: "alice", Name: "Dup", OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	turn := &chatTurn{udb: udb, user: "alice"}
	if _, why := turn.ownAgentByNameOrID("Dup"); !strings.Contains(why, "dup-1") || !strings.Contains(why, "dup-2") {
		t.Errorf("an ambiguous name was not refused with its ids: %q", why)
	}
	if ag, why := turn.ownAgentByNameOrID("dup-2"); why != "" || ag.ID != "dup-2" {
		t.Errorf("an id should resolve directly: %q %q", ag.ID, why)
	}
}

// Agent names carry display tags in brackets — "Kwik [Cortex]", "Market
// Research [Fleet]" — which is this framework's OWN convention, printed in
// every listing an agent reads. So a caller naturally writes the name without
// the tag, and exact matching then fails on an agent that plainly exists.
//
// Reported live: Builder looked up "Moltbook Conversational Agent", was told
// it was not found, and stopped to ask the user which agent was meant, while
// the agent sat in the survey output it had just read.
func TestANameResolvesWithoutItsDisplayTag(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a1", Name: "Moltbook Conversational Agent [Cortex]"},
		{ID: "a2", Name: "Market Research [Fleet]"},
		{ID: "a3", Name: "Critic"},
	}
	for _, c := range []struct{ key, wantID string }{
		{"Moltbook Conversational Agent", "a1"}, // the reported case
		{"moltbook conversational agent", "a1"}, // case drift
		{"moltbook-conversational-agent", "a1"}, // separator drift, tag absent
		{"Market Research", "a2"},
		{"Critic", "a3"}, // untagged names still work
	} {
		got, ok, err := resolveAgentName(agents, nil, nil, c.key, "")
		if err != nil || !ok || got.ID != c.wantID {
			t.Errorf("%q resolved to %+v, want %s", c.key, got, c.wantID)
		}
	}
}

// Ambiguity is not resolved by guessing. Two agents sharing a base name mean
// the bare name means NEITHER — picking one would silently edit the wrong
// agent, which is worse than saying so.
func TestAnAmbiguousBaseNameDoesNotGuess(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a1", Name: "Research [Cortex]"},
		{ID: "a2", Name: "Research [Fleet]"},
	}
	if _, ok, err := resolveAgentName(agents, nil, nil, "research", ""); ok || err == nil {
		t.Fatal("an ambiguous base name picked one of two agents, or did not say it was ambiguous")
	}
	// And the suggestion names both, so the choice stays with the caller.
	s := suggestAgents(agents, "Research")
	for _, want := range []string{"Research [Cortex]", "Research [Fleet]"} {
		if !strings.Contains(s, want) {
			t.Errorf("the suggestion omits %q: %s", want, s)
		}
	}
}

// An exact name always wins over a tag-stripped collision.
func TestAnExactNameWinsOverTagStripping(t *testing.T) {
	agents := []AgentRecord{
		{ID: "tagged", Name: "Research [Cortex]"},
		{ID: "plain", Name: "Research"},
	}
	// "Research" matches the plain agent exactly; stripping must not make it
	// ambiguous with the tagged one.
	exact, _, err := resolveAgentName(agents, nil, nil, "Research", "")
	if err != nil || exact.ID != "plain" {
		t.Fatalf("exact match resolved to %q", exact.ID)
	}
}

// A dead-end "not found" against a fleet of dozens leaves the caller unable to
// tell a missing agent from a misspelt one.
func TestSuggestionsNameTheNearMisses(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a1", Name: "Moltbook Conversational Agent [Cortex]"},
		{ID: "a2", Name: "Deal Hunter"},
		{ID: "a3", Name: "Comedian"},
	}
	s := suggestAgents(agents, "Moltbook")
	if !strings.Contains(s, "Moltbook Conversational Agent [Cortex]") {
		t.Errorf("a partial name suggested nothing useful: %q", s)
	}
	// An unrelated name suggests nothing rather than listing the fleet.
	if got := suggestAgents(agents, "zzzz"); got != "" {
		t.Errorf("an unrelated name produced suggestions: %q", got)
	}
	if got := suggestAgents(agents, ""); got != "" {
		t.Errorf("an empty key produced suggestions: %q", got)
	}
}

// Callers shorten names. A model reading a fleet listing writes the
// distinctive word, not the four-word title with its bracketed tag. Reported
// live: Builder looked up "moltbook", was told no such agent existed, and
// repeated that to the user as a fact about an agent it had just seen listed.
func TestAUniquePartialNameResolves(t *testing.T) {
	agents := []AgentRecord{
		{ID: "molt", Name: "Moltbook Conversational Agent [Cortex]"},
		{ID: "deal", Name: "Deal Hunter"},
		{ID: "comedian", Name: "Comedian"},
	}
	for _, c := range []struct{ key, wantID string }{
		{"moltbook", "molt"}, // the reported case
		{"Moltbook", "molt"}, // case drift
		{"deal", "deal"},
		{"comedian", "comedian"},
	} {
		got, ok, err := resolveAgentName(agents, nil, nil, c.key, "")
		if err != nil || !ok || got.ID != c.wantID {
			t.Errorf("%q resolved to %+v, want %s", c.key, got, c.wantID)
		}
	}
}

// Unique or nothing. A fragment matching two agents means neither — resolving
// it would edit whichever sorted first, and an authoring tool silently
// rewriting the wrong agent is worse than a failed lookup.
func TestAnAmbiguousPartialNameRefuses(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a", Name: "Research Assistant"},
		{ID: "b", Name: "Research Reviewer"},
	}
	if _, ok, err := resolveAgentName(agents, nil, nil, "research", ""); ok || err == nil {
		t.Fatal("an ambiguous fragment picked one of two agents, or did not say it was ambiguous")
	}
}

// A prefix beats an interior match: "research" should mean the agent whose
// name STARTS that way, not one that merely contains the word.
func TestAPrefixBeatsAnInteriorMatch(t *testing.T) {
	agents := []AgentRecord{
		{ID: "deep", Name: "Deep Dive Research Agent [Fleet]"},
		{ID: "plain", Name: "Research Agent"},
	}
	got, ok, _ := resolveAgentName(agents, nil, nil, "research", "")
	if !ok || got.ID != "plain" {
		t.Fatalf("resolved to %+v, want the agent whose name begins with it", got)
	}
}

// A fragment too short to identify anything must not resolve: two letters
// matching one agent today match three after the next one is added, so the
// answer would be correct only until the fleet grew.
func TestAVeryShortFragmentDoesNotResolve(t *testing.T) {
	agents := []AgentRecord{{ID: "a", Name: "Comedian"}}
	for _, frag := range []string{"c", "co", ""} {
		if _, ok, _ := resolveAgentName(agents, nil, nil, frag, ""); ok {
			t.Errorf("%q resolved to an agent", frag)
		}
	}
}

// The precedence chain end to end: an exact name must never be beaten by a
// partial match on a different agent.
func TestExactNameStillWinsOverAPartial(t *testing.T) {
	agents := []AgentRecord{
		{ID: "long", Name: "Research Assistant"},
		{ID: "short", Name: "Research"},
	}
	// "Research" is exact for one and a prefix of the other; the exact one wins
	// because resolveAgentName checks exact names before ever reaching the
	// partial fallback.
	exact, _, err := resolveAgentName(agents, nil, nil, "Research", "")
	if err != nil || exact.ID != "short" {
		t.Fatalf("exact match resolved to %q", exact.ID)
	}
}

// The per-turn dispatch cap must break a genuine LOOP (same target + same
// message re-fired) without false-positiving on legitimate verification,
// where the Builder drives ONE agent with a DIFFERENT message per tool.

func TestDispatchCap_IdenticalMessageLoopBlocks(t *testing.T) {
	counts := map[string]int{}
	const msg = "run your profile tool"
	// First maxSameTargetDispatch identical calls are allowed.
	for i := 0; i < maxSameTargetDispatch; i++ {
		if block := dispatchCapDecision(counts, "tid", "Target", msg, false); block != "" {
			t.Fatalf("identical call %d/%d should be allowed, got block: %s", i+1, maxSameTargetDispatch, block)
		}
	}
	// The next identical call trips the loop cap.
	block := dispatchCapDecision(counts, "tid", "Target", msg, false)
	if block == "" {
		t.Fatal("identical message past the cap should be blocked")
	}
	if !strings.Contains(block, "SAME message") {
		t.Errorf("block should cite the identical-message loop; got: %s", block)
	}
}

func TestDispatchCap_DistinctMessagesDoNotCollide(t *testing.T) {
	counts := map[string]int{}
	// Exercising many DIFFERENT tools on one agent (profile, post, feed…) is
	// real progress — none of these should trip the loop cap even though
	// they all target the same agent, well past maxSameTargetDispatch calls.
	for i := 0; i < maxSameTargetDispatch+3; i++ {
		msg := fmt.Sprintf("run tool number %d", i)
		if block := dispatchCapDecision(counts, "tid", "Target", msg, false); block != "" {
			t.Fatalf("distinct message %d should be allowed (not a loop); got block: %s", i, block)
		}
	}
}

func TestDispatchCap_ThrashCeilingBuilderVsOrdinary(t *testing.T) {
	// An ordinary agent hits the lower total ceiling; Builder gets headroom.
	ordinary := map[string]int{}
	blockedAt := 0
	for i := 1; i <= maxBuilderTargetDispatch+1; i++ {
		msg := fmt.Sprintf("distinct %d", i)
		if dispatchCapDecision(ordinary, "tid", "Target", msg, false) != "" {
			blockedAt = i
			break
		}
	}
	if blockedAt != maxTotalTargetDispatch+1 {
		t.Errorf("ordinary agent should thrash-block at %d, blocked at %d", maxTotalTargetDispatch+1, blockedAt)
	}

	builder := map[string]int{}
	for i := 1; i <= maxTotalTargetDispatch+2; i++ {
		msg := fmt.Sprintf("distinct %d", i)
		if block := dispatchCapDecision(builder, "tid", "Target", msg, true); block != "" {
			t.Fatalf("Builder should not thrash-block at ordinary ceiling; blocked at %d: %s", i, block)
		}
	}
}

// A continuing dispatch end to end, against the scripted model: the run a
// channel message, a monitor wake, a machine step and agents(run) all go
// through. What its callers depend on is the reply it hands back and the
// thread it keeps, so that is what these read.

type dispatchFixture struct {
	t      *testing.T
	app    *OrchestrateApp
	udb    Database
	agent  AgentRecord
	llm    *FakeLLM
	looked []string // the order ids the app tool was asked for
}

func newDispatchFixture(t *testing.T, turns ...FakeTurn) *dispatchFixture {
	t.Helper()
	root := depStores(t, "u")
	df := &dispatchFixture{t: t, app: orchRef, udb: UserDB(root, "u"), llm: &FakeLLM{Turns: turns}}
	df.app.LLM = df.llm
	a, err := saveAgent(df.udb, AgentRecord{Name: "Clerk", Owner: "u", OrchestratorPrompt: "You answer questions about orders."})
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	return df
}

// lookupTool is the caller's own tool, the way an app hands one in.
func (df *dispatchFixture) lookupTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "lookup_order",
			Description: "Look up an order by id.",
			Parameters:  map[string]ToolParam{"id": {Type: "string", Description: "The order id."}},
			Required:    []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			df.looked = append(df.looked, id)
			return "order " + id + ": shipped Tuesday", nil
		},
	}
}

func (df *dispatchFixture) run(r AgentSyncRun) AgentSyncResult {
	df.t.Helper()
	if r.AgentOwner == "" {
		r.AgentOwner = "u"
	}
	if r.AgentKey == "" {
		r.AgentKey = df.agent.ID
	}
	r.AppTools = append(r.AppTools, df.lookupTool())
	res, err := df.app.RunAgentSyncContinuingRich(df.t.Context(), r)
	if err != nil {
		df.t.Fatalf("the dispatch did not run: %v", err)
	}
	return res
}

func (df *dispatchFixture) thread(id string) ChatSession {
	s, _ := loadChatSession(df.udb, df.agent.ID, id)
	return s
}

// request is the call that carried want as its live message, and that call's
// messages: the judges call the same model, so position says nothing.
func (df *dispatchFixture) request(want string) []Message {
	for i := 0; i < df.llm.Calls(); i++ {
		msgs := df.llm.Sent(i)
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && strings.Contains(msgs[n-1].Content, want) {
			return msgs
		}
	}
	return nil
}

func lookupCall(id string) FakeTurn {
	return FakeTurn{ToolCalls: []ToolCall{{ID: "c-" + id, Name: "lookup_order", Args: map[string]any{"id": id}}}}
}

func TestADispatchAnswersAndKeepsItsThread(t *testing.T) {
	const reply = "Order A17 shipped Tuesday."
	df := newDispatchFixture(t, lookupCall("A17"), FakeTurn{Content: reply, Repeat: true})
	// By name, as a schedule or a pipeline stage names its agent.
	res := df.run(AgentSyncRun{AgentKey: "Clerk", SubSessionID: "thread-1", Message: "Where is order A17?"})

	if res.Text != reply {
		t.Errorf("reply = %q, want %q", res.Text, reply)
	}
	if len(df.looked) != 1 || df.looked[0] != "A17" {
		t.Errorf("the caller's tool was not run with the model's arguments: %v", df.looked)
	}
	if len(res.ToolCalls) == 0 || res.ToolCalls[0].Name != "lookup_order" {
		t.Errorf("the result does not carry what the turn ran: %+v", res.ToolCalls)
	}

	s := df.thread("thread-1")
	if len(s.Messages) != 2 || s.Messages[0].Role != "user" || s.Messages[0].Content != "Where is order A17?" ||
		s.Messages[1].Role != "assistant" || s.Messages[1].Content != reply {
		t.Fatalf("the thread does not hold the exchange: %+v", s.Messages)
	}
	if calls := s.Messages[1].ToolCalls; len(calls) == 0 || calls[0].Name != "lookup_order" {
		t.Errorf("the stored reply has no tool chips: %+v", calls)
	}

	// The next message on the thread is asked with the exchange before it.
	df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "And order B2?"})
	sent := df.request("And order B2?")
	if sent == nil {
		t.Fatal("the second message never reached the model")
	}
	prior := false
	for _, m := range sent[:len(sent)-1] {
		if strings.Contains(m.Content, "Where is order A17?") {
			prior = true
		}
	}
	if !prior {
		t.Errorf("the second message was asked without the thread before it: %+v", sent)
	}
	if n := len(df.thread("thread-1").Messages); n != 4 {
		t.Errorf("the thread should hold both exchanges, has %d messages", n)
	}
}

func TestAFreshSessionStartsTheThreadOver(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Noted.", Repeat: true})
	df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Remember order A17."})
	df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Start over.", FreshSession: true})
	s := df.thread("thread-1")
	if len(s.Messages) != 2 || s.Messages[0].Content != "Start over." {
		t.Errorf("a fresh session should hold only its own exchange: %+v", s.Messages)
	}
	for _, m := range df.request("Start over.") {
		if strings.Contains(m.Content, "Remember order A17.") {
			t.Error("a fresh session was asked with the wiped thread")
		}
	}
}

func TestADispatchWithNoThreadGetsItsOwn(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Hello.", Repeat: true})
	df.run(AgentSyncRun{Message: "Hi."})
	if s := df.thread("external-dispatch:u:" + df.agent.ID); len(s.Messages) != 2 {
		t.Errorf("a dispatch with no thread should keep one per runtime user and agent: %+v", s.Messages)
	}
}

// A channel turn records who wrote each side, and the room takes its name
// from the caller.
func TestAChannelTurnRecordsWhoSaidWhat(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "It shipped.", Repeat: true})
	res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "Did it ship?", MessageSender: "Alice", Title: "Alice"})
	if res.Text != "It shipped." {
		t.Errorf("reply = %q", res.Text)
	}
	s := df.thread("room-1")
	if s.Title != "Alice" {
		t.Errorf("the room is titled %q, want the caller's", s.Title)
	}
	if len(s.Messages) != 2 || s.Messages[0].Sender != "Alice" || s.Messages[1].Sender != df.agent.Name {
		t.Errorf("each side should carry its author: %+v", s.Messages)
	}
}

// A wake's input is the event that fired, kept as its card, and the thread it
// lands in takes that name when it has none.
func TestAWakeIsKeptAsItsEventCard(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "The price dropped; I told the owner.", Repeat: true})
	df.run(AgentSyncRun{SubSessionID: "wake-1", Message: "price is now 41", InputReportFrom: "Price watch", InputReportKind: "monitor"})
	s := df.thread("wake-1")
	if s.Title != "Price watch" {
		t.Errorf("the thread is titled %q, want the event's", s.Title)
	}
	if len(s.Messages) != 2 || s.Messages[0].ReportFrom != "Price watch" || s.Messages[0].ReportKind != "monitor" {
		t.Errorf("the input should be kept as the event's card: %+v", s.Messages)
	}
}

// A note the person queued while the run was going reaches the model.
func TestAMidFlightNoteReachesTheRun(t *testing.T) {
	df := newDispatchFixture(t, lookupCall("A17"), FakeTurn{Content: "Done.", Repeat: true})
	q := RegisterInjectionQueue("q-1", "u", df.agent.ID)
	t.Cleanup(func() { ReleaseInjectionQueue("q-1") })
	q.Push("Use the express rate.")
	df.run(AgentSyncRun{SubSessionID: "thread-1", InjectionQueueID: "q-1", Message: "Reship order A17."})
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			if strings.Contains(m.Content, "[MID-FLIGHT NOTE") && strings.Contains(m.Content, "Use the express rate.") {
				return
			}
		}
	}
	t.Error("the queued note never reached the model")
}

// A turn that runs tools and then says nothing leaves a trail saying so.
func TestAReplyWithNothingToSayLeavesATrail(t *testing.T) {
	df := newDispatchFixture(t, lookupCall("A17"), FakeTurn{Content: "", Repeat: true})
	res := df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Check order A17."})
	if res.HitRoundCap {
		t.Skip("the loop spent its rounds; the trail is for a clean finish")
	}
	if res.Text != "" {
		t.Fatalf("want an empty reply, got %q", res.Text)
	}
	if diagsOf(df.app.DB, "u", df.agent.ID, "thread-1", "empty-reply") != 1 {
		t.Error("an empty reply left no breadcrumb")
	}
}

func TestADispatchWhoseModelFailsReturnsTheError(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Err: errors.New("model unavailable"), Repeat: true})
	_, err := df.app.RunAgentSyncContinuingRich(t.Context(), AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID, SubSessionID: "thread-1", Message: "Hi."})
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Errorf("err = %v, want the model's", err)
	}
	if s := df.thread("thread-1"); len(s.Messages) != 0 {
		t.Errorf("a failed run stored %d message(s)", len(s.Messages))
	}
}

// What the run refuses, it refuses before asking the model anything.
func TestADispatchRefusesWhatItCannotRun(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "should not be asked", Repeat: true})
	for _, c := range []struct {
		name string
		run  AgentSyncRun
		want string
	}{
		{"no owner", AgentSyncRun{AgentKey: df.agent.ID, Message: "Hi."}, "agentOwner is required"},
		{"no message", AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID}, "message is required"},
		{"no such agent", AgentSyncRun{AgentOwner: "u", AgentKey: "Nobody", Message: "Hi."}, "not found"},
	} {
		if _, err := df.app.RunAgentSyncContinuingRich(t.Context(), c.run); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if n := df.llm.Calls(); n != 0 {
		t.Errorf("a refused run asked the model %d time(s)", n)
	}
	df.app.LLM = nil
	if _, err := df.app.RunAgentSyncContinuingRich(t.Context(), AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID, Message: "Hi."}); err == nil {
		t.Error("a run with no model configured was not refused")
	}
}

// What the two inbound paths TEACH about media#N ids has to match, because the
// ids themselves already do. The chat path said "pass these to the image tool
// to edit, blend, or combine"; the channel path described them only as
// forwarding handles and warned against calling a tool for them at all. Asked
// over iMessage to "make x sit in y", a model did the only image thing it had
// been told about: it generated a new picture instead of editing the one it
// was sent.

func TestChannelManifestTeachesEditing(t *testing.T) {
	m := buildInboundMediaManifest("Alice", 1, nil)
	if m == "" {
		t.Fatal("no manifest for a turn carrying an image")
	}
	for _, want := range []string{
		"media#1",         // the handle
		"action=\"edit\"", // and what to do with it
		"images=[\"media#1\"]",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest never mentions %q:\n%s", want, m)
		}
	}
	// The specific failure: generating instead of editing.
	if !strings.Contains(m, "Do NOT generate a new picture") {
		t.Error("manifest does not warn against generating when asked to change a photo")
	}
}

// The anti-echo rule has to survive, and has to stop applying to edits — an
// edited picture is a new one and IS the answer to what was asked.
func TestChannelManifestStillBlocksEchoButAllowsEdits(t *testing.T) {
	m := buildInboundMediaManifest("Alice", 2, nil)
	if !strings.Contains(m, "do NOT send one back UNCHANGED") {
		t.Error("the echo guard is gone; a photo just posted would be re-sent to the same group")
	}
	if !strings.Contains(m, "An EDITED version is a new picture and is fine to send here") {
		t.Error("nothing says an edit may be sent back, so the echo guard reads as blocking the answer itself")
	}
	if !strings.Contains(m, "media#2") {
		t.Error("both items must be individuated")
	}
}

func TestNoManifestWithoutMedia(t *testing.T) {
	if m := buildInboundMediaManifest("Alice", 0, nil); m != "" {
		t.Errorf("manifest emitted for a turn with no media: %q", m)
	}
}

// A monitor wake landing in a thread someone is using: it must not answer
// their question for them, and it must not take their tools with it.
//
// Observed on a deployment with a JIRA watcher: the person asked something
// while the watcher's wake was running in the same thread. The wake had read
// the thread just after the question was saved, took it as the last thing
// said, and answered it in its own card while the person's turn answered it
// too. And every continuing turn deleted the thread's drafted tools as it
// ended, a cleanup copied from the one-shot dispatch whose thread is thrown
// away, so the tool the person had been building was gone.

func TestAWakeDoesNotAnswerTheQuestionBeingAnswered(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Ticket ABC-1 moved to review.", Repeat: true})
	if _, err := saveChatSession(df.udb, ChatSession{ID: "thread-j", AgentID: df.agent.ID, Messages: []ChatMessage{
		{Role: "user", Content: "what is the status of the build?"},
		{Role: "assistant", Content: "Green."},
		{Role: "user", Content: "which JIRA tickets are open for me?"}, // being answered by the person's own turn
	}}); err != nil {
		t.Fatal(err)
	}
	df.run(AgentSyncRun{SubSessionID: "thread-j", Message: "ABC-1 changed", InputReportFrom: "JIRA watch", InputReportKind: "monitor"})
	sent := df.request("ABC-1 changed")
	if sent == nil {
		t.Fatal("the wake never reached the model")
	}
	var saw []string
	for _, m := range sent {
		saw = append(saw, m.Content)
	}
	all := strings.Join(saw, "\n")
	if strings.Contains(all, "which JIRA tickets are open for me?") {
		t.Error("the wake read the person's unanswered question as part of its own turn")
	}
	if !strings.Contains(all, "what is the status of the build?") || !strings.Contains(all, "Green.") {
		t.Error("the wake should still read the conversation that was answered")
	}
	kept := df.thread("thread-j")
	found := false
	for _, m := range kept.Messages {
		if m.Content == "which JIRA tickets are open for me?" {
			found = true
		}
	}
	if !found {
		t.Error("the person's question should stay in the thread")
	}
}

// A message that arrives for the agent (no report card) is a turn of its own
// and still reads everything before it.
func TestAMessageStillReadsTheWholeThread(t *testing.T) {
	got := withoutUnansweredAsk([]ChatMessage{{Role: "assistant", Content: "a"}, {Role: "user", Content: "q"}})
	if len(got) != 1 {
		t.Fatalf("a trailing unanswered ask should be held back from an arrival: %+v", got)
	}
	got = withoutUnansweredAsk([]ChatMessage{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}})
	if len(got) != 2 {
		t.Errorf("an answered exchange should be kept whole: %+v", got)
	}
	got = withoutUnansweredAsk([]ChatMessage{{Role: "assistant", Content: "a"}, {Role: "user", Content: "fired", ReportFrom: "watch"}})
	if len(got) != 2 {
		t.Errorf("an earlier event card is not a person's question: %+v", got)
	}
}

func TestAWakeKeepsTheThreadsDraftedTools(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Noted.", Repeat: true})
	if err := SaveSessionTempTool(df.udb, "thread-d", TempTool{Name: "jira_open_tickets", Description: "List my open JIRA tickets."}); err != nil {
		t.Fatal(err)
	}
	df.run(AgentSyncRun{SubSessionID: "thread-d", Message: "ABC-2 changed", InputReportFrom: "JIRA watch", InputReportKind: "monitor"})
	tools := LoadSessionTempTools(df.udb, "thread-d")
	if len(tools) != 1 || tools[0].Name != "jira_open_tickets" {
		t.Errorf("a wake in the thread deleted the tool being built there: %+v", tools)
	}
}

// Each id says what it shows. Matched by position against a separate list,
// two photos from the same person were one guess apart.
func TestChannelManifestDescribesEachItem(t *testing.T) {
	m := buildInboundMediaManifest("Henry", 2, []string{"A brown dog on a porch.", ""})
	if !strings.Contains(m, "media#1: image from Henry: A brown dog on a porch.") {
		t.Errorf("media#1 lost its description:\n%s", m)
	}
	if !strings.Contains(m, "media#2: image from Henry\n") {
		t.Errorf("an undescribed item should read as before:\n%s", m)
	}
}

// countingVisionLLM answers every caption call the same and counts them.
type countingVisionLLM struct {
	LLM
	calls int
}

func (f *countingVisionLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	f.calls++
	return &Response{Content: "A picture."}, nil
}

// A message landing mid-turn reruns the turn with both folded in; the rerun
// must not describe the pictures the cancelled turn already had.
func TestARerunDoesNotDescribeThePicturesAgain(t *testing.T) {
	first, second := []byte("first-"+UUIDv4()), []byte("second-"+UUIDv4())
	llm := &countingVisionLLM{}
	captionInboundImages(context.Background(), llm, [][]byte{first})
	got := captionInboundImages(context.Background(), llm, [][]byte{first, second})
	if llm.calls != 2 {
		t.Errorf("%d vision calls, want 2: the rerun should describe only the new picture", llm.calls)
	}
	if len(got) != 2 || got[0] != "A picture." || got[1] != "A picture." {
		t.Errorf("captions = %q", got)
	}
}

func TestTheCaptionCacheStaysBounded(t *testing.T) {
	c := &captionCache{ttl: time.Minute, max: 2}
	c.put([32]byte{1}, "one")
	c.put([32]byte{2}, "two")
	c.put([32]byte{3}, "three")
	if _, ok := c.get([32]byte{1}); ok {
		t.Error("the oldest entry should have gone")
	}
	if v, ok := c.get([32]byte{3}); !ok || v != "three" {
		t.Error("the newest entry should be there")
	}
	c.put([32]byte{4}, "")
	if _, ok := c.get([32]byte{4}); ok {
		t.Error("an empty caption is not kept, so a failed call is tried again")
	}
	old := &captionCache{ttl: time.Nanosecond, max: 2}
	old.put([32]byte{5}, "five")
	time.Sleep(time.Millisecond)
	if _, ok := old.get([32]byte{5}); ok {
		t.Error("an expired caption should not be served")
	}
}
