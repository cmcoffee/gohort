package orchestrate

import (
	"errors"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
	"strings"
	"testing"
)

// harness: a caller agent + a sub-agent it owns, saved in one user store, with
// a chatTurn wired the way agentsRunAction expects for a fleet lookup.
func newRunGateTurn(t *testing.T, callerMode string) (*chatTurn, AgentRecord) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	caller, err := saveAgent(udb, AgentRecord{Name: "Moltbook", Owner: "u", DispatchMode: callerMode, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	target, err := saveAgent(udb, AgentRecord{Name: "Comedian", Owner: "u", OwnedBy: caller.ID, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save target: %v", err)
	}
	return &chatTurn{user: "u", udb: udb, agent: caller}, target
}

// TestDispatchNoneBlocksOwnedSubAgent pins the "Allow none is absolute" rule.
// Before the fix, the ownership carve-out ran FIRST, so a dispatch-disabled
// agent could still dispatch its own sub-agents without limit — observed as a
// Comedian storm (100+ dispatches in one autonomous turn) from an agent whose
// dispatch policy the user had set to Allow none.
func TestDispatchNoneBlocksOwnedSubAgent(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchNone)
	out, err := turn.agentsRunAction(map[string]any{"agent": "Comedian", "message": "tell a joke"})
	if err == nil {
		t.Fatalf("dispatch-none caller reached its owned sub-agent; out=%q", out)
	}
	if !strings.Contains(err.Error(), "Allow NONE") {
		t.Fatalf("refusal should name the Allow NONE policy; got: %v", err)
	}
}

// TestPermissionBlockRefusesAgentsRun pins tool/shell symmetry for the
// Permissions-pane delegation policy: a target Blocked there must be
// unreachable through agents(run) too, not just through the Operator's
// delegate tool. Before the fix agents(run) never consulted the policy, so a
// blocked target kept getting dispatched by every standing-cycle fire.
func TestPermissionBlockRefusesAgentsRun(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	SetDelegationPolicy(RootDB, "u", target.Name, PolicyBlock)

	out, err := turn.agentsRunAction(map[string]any{"agent": "Comedian", "message": "tell a joke"})
	if err == nil {
		t.Fatalf("permission-blocked target was dispatched anyway; out=%q", out)
	}
	if !strings.Contains(err.Error(), "BLOCKED in the user's permission settings") {
		t.Fatalf("refusal should name the permission block; got: %v", err)
	}
}

// TestAgentsToolBatchesThroughALane pins the batching mode of the agents tool.
// The loop executes batched tool calls in parallel goroutines; the agents tool
// opts into a lane so calls it must not overlap stay a sequence, and at the
// shipped default that lane is the shared serial one — a batch of dispatches
// behaves exactly as it did before fan-out existed. See dispatch_fanout.go for
// what raising the knob changes and what it can never change (two dispatches
// to ONE target share a sub-session id and always serialize).
func TestAgentsToolBatchesThroughALane(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	for _, allowRun := range []bool{true, false} {
		td := turn.agentsGroupedToolDef(allowRun)
		if td.BatchLane == nil {
			t.Fatalf("agents tool (allowRun=%v) must declare a BatchLane; without one its calls fan out unpartitioned", allowRun)
		}
		if td.SingleFirePerBatch {
			t.Fatalf("agents tool (allowRun=%v) must not be single-fire — batched calls all run, the lane decides which run together", allowRun)
		}
		lane := td.BatchLane(map[string]any{"action": "run", "agent": target.Name, "message": "hi"})
		if lane != "" {
			t.Fatalf("agents tool (allowRun=%v) must default to the shared serial lane; got %q", allowRun, lane)
		}
	}
}

// TestDispatchCapVerdictIsAnErrorNotAFencedResult pins the delivery channel of
// the per-turn dispatch cap. As a normal result the verdict rode through
// fenceAgentsOutput and reached the model inside the untrusted-content banner
// — a STOP instruction the fence itself tells the model to ignore, and one the
// loop's failure-streak machinery never counted. It must surface as an error.
func TestDispatchCapVerdictIsAnErrorNotAFencedResult(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	// Pre-burn the total-dispatch budget so the cap decision blocks before any
	// real sub-dispatch is attempted.
	turn.agentDispatchCounts = map[string]int{"total\x00" + target.ID: maxTotalTargetDispatch}
	out, err := turn.agentsRunAction(map[string]any{"agent": "Comedian", "message": "tell a joke"})
	if err == nil {
		t.Fatalf("cap verdict came back as a normal result (would be fenced): %q", out)
	}
	if !strings.HasPrefix(err.Error(), "STOP —") {
		t.Fatalf("cap error must carry the STOP verdict for the loop guards; got: %v", err)
	}
	if strings.Contains(out+err.Error(), "UNTRUSTED EXTERNAL CONTENT") {
		t.Fatal("cap verdict must never be wrapped in the untrusted-content fence")
	}
}

// TestAgentsToolOptsOutOfBlanketFence — the agents tool carries CapNetwork so
// Private mode strips it (a `run` could reach a sub-agent's web_search), but the
// untrusted fence keys off that SAME cap. Without the TrustedOutput opt-out,
// every list/get — pure reads of the user's own agent registry — got wrapped in
// "UNTRUSTED EXTERNAL CONTENT", telling an authoring agent to distrust the very
// records it was about to edit. Assert both halves: the cap stays (Private mode
// keeps working) AND the blanket fence is declined.
func TestAgentsToolOptsOutOfBlanketFence(t *testing.T) {
	def := (&chatTurn{}).agentsGroupedToolDef(true)

	if !toolCarriesNetworkCap(def.Tool) {
		t.Fatal("agents lost CapNetwork — Private mode would stop stripping it, letting a turn leak via a sub-agent dispatch")
	}
	if !def.Tool.TrustedOutput {
		t.Fatal("agents must set TrustedOutput so the blanket fence doesn't wrap internal list/get reads")
	}
	// The two must combine to "runner does not blanket-fence this tool" — the
	// exact condition at the fence site.
	if toolCarriesNetworkCap(def.Tool) && !def.Tool.TrustedOutput {
		t.Fatal("agents would still be blanket-fenced")
	}
}

// TestFenceAgentsOutput pins the per-action half: dispatch results get the
// fence, while errors and empty results pass through untouched.
func TestFenceAgentsOutput(t *testing.T) {
	out, err := fenceAgentsOutput("a sub-agent said something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(out, untrustedContentFence) {
		t.Fatal("run/run_tool output must be fenced — it carries whatever a sub-agent produced, possibly from the web")
	}
	if !strings.HasSuffix(out, "a sub-agent said something") {
		t.Fatalf("fence must PREFIX the payload, leaving it intact; got %q", out)
	}

	// An error must not be fenced — that would bury the message behind a banner.
	sentinel := errors.New("dispatch blew up")
	if out, err := fenceAgentsOutput("", sentinel); err != sentinel || out != "" {
		t.Fatalf("error path should pass through untouched; got (%q, %v)", out, err)
	}
	if out, _ := fenceAgentsOutput("   ", nil); strings.Contains(out, untrustedContentFence) {
		t.Fatal("blank output should not be fenced — there is nothing to fence")
	}
}

// TestAgentsGetResolvesByName — agents(get) used to hard-require an id while the
// same tool's `agent` param documents "Name or id" for run/run_tool, so the
// natural first call was rejected and cost a list-then-get round trip.
func TestAgentsGetResolvesByName(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}
	saved, err := saveAgent(udb, AgentRecord{
		Name: "OSINT Investigator", OrchestratorPrompt: "investigate", Owner: owner,
	})
	if err != nil {
		t.Fatalf("saveAgent: %v", err)
	}
	turn := &chatTurn{udb: udb, user: owner}

	// By name — the call that used to fail.
	byName, err := turn.agentsGetAction(map[string]any{"id": "OSINT Investigator"})
	if err != nil {
		t.Fatalf("agents(get) by name: %v", err)
	}
	if !strings.Contains(byName, saved.ID) {
		t.Fatalf("get by name did not return the agent record; got %q", byName)
	}

	// Via the `agent` key, which run/run_tool use — accepted interchangeably.
	viaAgentKey, err := turn.agentsGetAction(map[string]any{"agent": "OSINT Investigator"})
	if err != nil {
		t.Fatalf("agents(get) via agent key: %v", err)
	}
	if !strings.Contains(viaAgentKey, saved.ID) {
		t.Fatal("get via the agent key did not return the agent record")
	}

	// By id still works — the fix must not regress the documented path.
	byID, err := turn.agentsGetAction(map[string]any{"id": saved.ID})
	if err != nil {
		t.Fatalf("agents(get) by id: %v", err)
	}
	if !strings.Contains(byID, saved.ID) {
		t.Fatal("get by id regressed")
	}

	// A name that matches nothing must still 404 rather than resolve to junk.
	if _, err := turn.agentsGetAction(map[string]any{"id": "No Such Agent"}); err == nil {
		t.Fatal("expected not-found for an unknown name")
	}
	// Neither key supplied — the error should name both options.
	if _, err := turn.agentsGetAction(map[string]any{}); err == nil {
		t.Fatal("expected an error when neither id nor agent is supplied")
	}
}

// TestAppAgentDispatchDefaultsToNone — an app agent is hidden, bound to its
// app's surface, and gets the `agents` grouped tool whether or not its spec's
// AllowedTools names it (it is a framework tool). Left on the ordinary blank
// default it therefore reached every non-hidden agent in the user's fleet, and
// no app author chose that or could see it. Guides' Guide Author was dispatching
// to agents its guide had never attached as Sources.
func TestAppAgentDispatchDefaultsToNone(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-quiet", Name: "Quiet", OwningApp: "Test", Hidden: true,
		Prompt: "x",
	})
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-loud", Name: "Loud", OwningApp: "Test", Hidden: true,
		Prompt: "x", DispatchMode: appagents.DispatchAll,
	})

	// A blank spec means NONE. Asked of a record whose own field is blank too —
	// which is what a per-user shadow saved before the spec grew the field looks
	// like, and the case a spec-only fix would have missed.
	if got := effectiveDispatchMode(AgentRecord{ID: "app-test-quiet"}); got != dispatchNone {
		t.Errorf("app agent with no declared policy = %q, want %q", got, dispatchNone)
	}
	// Reaching the fleet is opt-IN, and the opt-in works.
	if got := effectiveDispatchMode(AgentRecord{ID: "app-test-loud"}); got != dispatchAll {
		t.Errorf("app agent declaring DispatchAll = %q, want %q", got, dispatchAll)
	}
	// The spec is a FALLBACK, never an override: an explicit mode on the record
	// is a decision, whether the owner made it in the editor or the hosting app
	// made it on its per-turn copy (guides binds dispatch to the open guide's
	// attached agent Sources exactly this way).
	rec := AgentRecord{ID: "app-test-quiet", DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"a1"}}
	if got := effectiveDispatchMode(rec); got != dispatchOnly {
		t.Errorf("host app's per-turn override = %q, want %q — the spec default must not overrule it", got, dispatchOnly)
	}
	// And an ordinary agent is untouched: blank still means the fleet.
	if got := effectiveDispatchMode(AgentRecord{ID: "some-user-agent"}); got != dispatchAll {
		t.Errorf("non-app agent with no policy = %q, want %q — this change must not narrow ordinary agents", got, dispatchAll)
	}
	// The spec's own resolver agrees with what the record path produced.
	if s, ok := appagents.AppAgentByID("app-test-quiet"); !ok || s.EffectiveDispatchMode() != appagents.DispatchNone {
		t.Error("AppAgentSpec.EffectiveDispatchMode disagrees with the record path")
	}
	// The record built from a spec carries the policy, so the editor shows it
	// rather than an empty select that reads as "all".
	if r := appAgentSpecToRecord(appagents.AppAgentSpec{ID: "x", Name: "x"}); r.DispatchMode != dispatchNone {
		t.Errorf("spec-to-record left DispatchMode %q; the editor would render it as the default", r.DispatchMode)
	}
}

// TestUserAgentWinsANameCollision — the app-agent registry is process-global and
// its entries are HIDDEN, so a user naming an agent "Investigator" has no way to
// know one already answers to that. Resolution used to hand back whichever
// record listAgents emitted first, making the answer depend on registration
// order — on which apps are compiled in. A caller asking for the agent the user
// built and named got a hidden framework agent instead, silently.
func TestUserAgentWinsANameCollision(t *testing.T) {
	const name = "Zzcollide Fixture"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-collide", Name: name, OwningApp: "Test", Hidden: true, Prompt: "x",
	})
	_, udb, user := newTestOrchestrate(t)

	// With no agent of their own by that name, the framework's still resolves —
	// precedence must not become a hard block.
	if a, ok := findAgentByNameOrID(udb, user, name); !ok || a.ID != "app-test-collide" {
		t.Fatalf("framework agent should resolve when nothing else claims the name; got %+v ok=%v", a.ID, ok)
	}

	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine-collide", Name: name, Owner: user, OrchestratorPrompt: "mine",
	}); err != nil {
		t.Fatalf("saving the user's agent: %v", err)
	}

	// Now theirs wins.
	if a, ok := findAgentByNameOrID(udb, user, name); !ok || a.ID != "mine-collide" {
		t.Errorf("user's own agent lost the name they gave it; resolved to %q", a.ID)
	}
	// Case and separator drift resolve to theirs too — precedence applies at
	// every tier, not just the exact one.
	if a, ok := findAgentByNameOrID(udb, user, "zzcollide fixture"); !ok || a.ID != "mine-collide" {
		t.Errorf("case-insensitive lookup resolved to %q, want the user's own", a.ID)
	}
	if a, ok := findAgentByNameOrID(udb, user, "zzcollide-fixture"); !ok || a.ID != "mine-collide" {
		t.Errorf("slug-tolerant lookup resolved to %q, want the user's own", a.ID)
	}
	// An explicit ID still addresses exactly what it names — precedence is a
	// tie-break on NAMES and must never override an id.
	if a, ok := findAgentByNameOrID(udb, user, "app-test-collide"); !ok || a.ID != "app-test-collide" {
		t.Errorf("lookup by id resolved to %q; an id is not ambiguous", a.ID)
	}
}

// TestFrameworkSplitKeysOnIDNotOwner — a customized seed carries a per-user
// shadow whose Owner IS the user. Splitting on Owner would call it theirs and
// hand a shadowed framework agent the same precedence as one they built.
func TestFrameworkSplitKeysOnIDNotOwner(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-shadowed", Name: "Shadowed", OwningApp: "Test", Hidden: true, Prompt: "x",
	})
	own, framework := splitOwnAndFrameworkAgents([]AgentRecord{
		{ID: "app-test-shadowed", Name: "Shadowed", Owner: "alice"}, // shadow: owner looks like the user
		{ID: "user-made", Name: "Mine", Owner: "alice"},
	})
	if len(framework) != 1 || framework[0].ID != "app-test-shadowed" {
		t.Errorf("a shadowed app agent must count as framework, got %+v", framework)
	}
	if len(own) != 1 || own[0].ID != "user-made" {
		t.Errorf("own = %+v, want just the user-made agent", own)
	}
}

// TestMaterializeArchetypeAgent pins the retirement safety net: a virgin
// retiring seed materializes into a user-owned agent carrying the seed's
// config and name; the operation is idempotent; a shadow or a non-seed target
// passes through materializeIfRetiringSeed unchanged.
func TestMaterializeArchetypeAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	// The virgin seed resolves in a bare store (in-code seed), Owner=system.
	seed, ok := findAgentByNameOrID(udb, "u", "Research")
	if !ok || seed.ID != "seed-research" || seed.Owner != seedOwner {
		t.Fatalf("virgin Research should resolve to the seed; got id=%q owner=%q ok=%v", seed.ID, seed.Owner, ok)
	}

	// materializeIfRetiringSeed swaps the virgin seed for a user-owned copy.
	got := materializeIfRetiringSeed(udb, "u", seed)
	if got.ID == "seed-research" || got.Owner != "u" {
		t.Fatalf("virgin seed should materialize to a user-owned agent; got id=%q owner=%q", got.ID, got.Owner)
	}
	if got.Name != "Research" {
		t.Fatalf("materialized agent must keep the name for resolution; got %q", got.Name)
	}
	if len(got.AllowedTools) == 0 {
		t.Fatal("materialized Research should carry the seed's curated toolset")
	}

	// Idempotent: a second call (and a by-id dispatch) returns the SAME copy.
	again := materializeIfRetiringSeed(udb, "u", seed)
	if again.ID != got.ID {
		t.Fatalf("materialize must be idempotent; first=%q second=%q", got.ID, again.ID)
	}
	// After materialize, "Research" resolves to the user-owned copy, not the seed.
	if r, _ := findAgentByNameOrID(udb, "u", "Research"); r.ID != got.ID {
		t.Fatalf("name should now resolve to the materialized copy; got %q", r.ID)
	}

	// A non-seed target is untouched.
	normal := AgentRecord{ID: "abc", Owner: "u", Name: "Normal"}
	if out := materializeIfRetiringSeed(udb, "u", normal); out.ID != "abc" {
		t.Fatalf("non-seed target must pass through unchanged; got %q", out.ID)
	}
}

// TestShadowedRetiringSeedNotMaterialized pins that a user who SHADOWED the
// seed (customized it — their row is Owner=user at the seed id) is left
// untouched: no duplicate, no lost customization.
func TestShadowedRetiringSeedNotMaterialized(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	// Save a shadow: a user-owned row at the seed id.
	shadow := AgentRecord{ID: "seed-research", Owner: "u", Name: "Research",
		OrchestratorPrompt: "my custom research persona", AllowedTools: []string{"web_search"}}
	if _, err := saveAgent(udb, shadow); err != nil {
		t.Fatalf("save shadow: %v", err)
	}
	resolved, ok := findAgentByNameOrID(udb, "u", "seed-research")
	if !ok || resolved.Owner != "u" {
		t.Fatalf("shadow should resolve as user-owned; got owner=%q ok=%v", resolved.Owner, ok)
	}
	// materializeIfRetiringSeed leaves the shadow alone (Owner != seedOwner):
	// no new agent minted, the id stays the seed id. (The prompt is governed
	// by the seed-field merge in loadAgent, not by materialize — out of scope
	// here.)
	out := materializeIfRetiringSeed(udb, "u", resolved)
	if out.ID != "seed-research" || out.Owner != "u" {
		t.Fatalf("shadow must pass through unmaterialized; got id=%q owner=%q", out.ID, out.Owner)
	}
	// And no duplicate user-owned "Research" was created.
	dupes := 0
	for _, a := range listAgents(udb, "u") {
		if a.Name == "Research" {
			dupes++
		}
	}
	if dupes != 1 {
		t.Fatalf("shadow dispatch must not create a duplicate Research; found %d", dupes)
	}
}

// TestRetiringSeedsFilteredFromDispatchPicker pins that the dispatch-target
// picker no longer offers the retiring seeds (they're materialized on
// dispatch, not chosen as targets), while a real agent stays.
func TestRetiringSeedsFilteredFromDispatchPicker(t *testing.T) {
	if !isRetiringArchetypeSeed("seed-research") || !isRetiringArchetypeSeed("seed-kb") {
		t.Fatal("seed-research and seed-kb must be retiring archetype seeds")
	}
	if isRetiringArchetypeSeed("seed-chat") || isRetiringArchetypeSeed("seed-builder") {
		t.Fatal("only research/kb are retiring archetype seeds")
	}
}

// effectiveDispatchMode must apply back-compat: a blank mode with a non-empty
// target list is the legacy allowlist ("only"), and an unknown mode fails OPEN
// to the same inference (never a silent hard block).
func TestEffectiveDispatchMode(t *testing.T) {
	cases := []struct {
		mode    string
		targets []string
		want    string
	}{
		{"", nil, dispatchAll},              // default
		{"", []string{"a"}, dispatchOnly},   // legacy allowlist inference
		{"all", []string{"a"}, dispatchAll}, // explicit all wins over a list
		{"only", nil, dispatchOnly},         // explicit
		{"except", []string{"a"}, dispatchExcept},
		{"none", nil, dispatchNone},
		{"bogus", []string{"a"}, dispatchOnly}, // unknown → fail-open, legacy infer
		{"bogus", nil, dispatchAll},            // unknown, no list → all
	}
	for _, c := range cases {
		got := effectiveDispatchMode(AgentRecord{DispatchMode: c.mode, AllowedDispatchTargets: c.targets})
		if got != c.want {
			t.Errorf("mode=%q targets=%v → %q, want %q", c.mode, c.targets, got, c.want)
		}
	}
}
