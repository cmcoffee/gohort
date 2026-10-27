package orchestrate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/snugforge/kvlite"
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

// Allow none stops every agent except the caller's own sub-agents, which exist
// to be called by it. It once stopped those too, after a dispatch-disabled
// agent called its own sub-agent 100+ times in one autonomous turn; the
// per-turn cap and serial firing that landed with that fix bound the volume,
// and still apply. The refusal for anything else names the policy.
func TestDispatchNoneStillReachesOwnSubAgent(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchNone)
	if target, _, err := turn.agentsRunGate(map[string]any{"agent": "Comedian", "message": "tell a joke"}); err != nil || target.Name != "Comedian" {
		t.Fatalf("Allow none must still reach the caller's own sub-agent; got target=%q err=%v", target.Name, err)
	}
	if _, err := saveAgent(turn.udb, AgentRecord{Name: "Stranger", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := turn.agentsRunGate(map[string]any{"agent": "Stranger", "message": "hello"})
	if err == nil {
		t.Fatal("Allow none reached an agent that is not the caller's own sub-agent")
	}
	if !strings.Contains(err.Error(), "Allow NONE") {
		t.Fatalf("refusal should name the Allow NONE policy; got: %v", err)
	}
}

// Allow none stops the fleet, own sub-agents included, but Builder answers to
// its own grant: with "Can dispatch Builder" on, the gate lets Builder through
// under Allow none, and without it (a Fleet controller's implicit access
// included) Builder is refused like everything else.
func TestDispatchNoneLetsTheBuilderGrantThrough(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchNone)
	turn.agent.AllowBuilderDispatch = true
	target, _, err := turn.agentsRunGate(map[string]any{"agent": "seed-builder", "message": "build a tool"})
	if err != nil || !isBuilderAgent(target.ID) {
		t.Fatalf("Allow none + grant must reach Builder; got target=%q err=%v", target.ID, err)
	}
	if _, err := saveAgent(turn.udb, AgentRecord{Name: "Stranger", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := turn.agentsRunGate(map[string]any{"agent": "Stranger", "message": "hello"}); err == nil {
		t.Fatal("the Builder grant must not reopen the rest of the fleet under Allow none")
	}

	fleet, _ := newRunGateTurn(t, dispatchNone)
	fleet.agent.Fleet = true
	if _, _, err := fleet.agentsRunGate(map[string]any{"agent": "seed-builder", "message": "build a tool"}); err == nil {
		t.Fatal("a Fleet controller without the explicit grant must not reach Builder under Allow none")
	}
}

// TestPermissionBlockRefusesAgentsRun pins tool/shell symmetry for the
// Permissions-pane delegation policy: a target Blocked there must be
// unreachable through agents(run) too, not just through the Operator's
// delegate tool. Before the fix agents(run) never consulted the policy, so a
// blocked target kept getting dispatched by every standing-cycle fire.
func TestPermissionBlockRefusesAgentsRun(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchAll)
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	// Not the harness's target: that one is the caller's own sub-agent, which
	// no Block reaches (ownSubAgent).
	target, err := saveAgent(turn.udb, AgentRecord{Name: "Stranger", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	// Scope "" is the owner-wide block, which is what this table has always
	// held and what the dispatch surfaces have always read: off limits to
	// everyone, whatever route reaches it.
	SetDelegationPolicy(RootDB, "u", "", target.Name, PolicyBlock)

	out, err := turn.agentsRunAction(map[string]any{"agent": "Stranger", "message": "hello"})
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
	if !strings.HasPrefix(err.Error(), "STOP:") {
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
	got := materializeIfRetiringSeed(udb, "u", "Research", seed)
	if got.ID == "seed-research" || got.Owner != "u" {
		t.Fatalf("virgin seed should materialize to a user-owned agent; got id=%q owner=%q", got.ID, got.Owner)
	}
	if got.Name != "Research" {
		t.Fatalf("materialized agent must keep the name for resolution; got %q", got.Name)
	}
	if len(got.AllowedTools) == 0 {
		t.Fatal("materialized Research should carry the seed's curated toolset")
	}

	// Idempotent: a second call returns the SAME copy.
	again := materializeIfRetiringSeed(udb, "u", "Research", seed)
	if again.ID != got.ID {
		t.Fatalf("materialize must be idempotent; first=%q second=%q", got.ID, again.ID)
	}
	// After materialize, "Research" resolves to the user-owned copy, not the seed.
	if r, _ := findAgentByNameOrID(udb, "u", "Research"); r.ID != got.ID {
		t.Fatalf("name should now resolve to the materialized copy; got %q", r.ID)
	}

	// A non-seed target is untouched.
	normal := AgentRecord{ID: "abc", Owner: "u", Name: "Normal"}
	if out := materializeIfRetiringSeed(udb, "u", "Normal", normal); out.ID != "abc" {
		t.Fatalf("non-seed target must pass through unchanged; got %q", out.ID)
	}
}

// An app that runs the seed as its engine names it by id, and runs it in
// place: no "Research" of the user's own appears, in the store or the lists.
func TestASeedRunByIDIsNotMaterialized(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	seed, ok, err := resolveAgentRef(udb, "u", "seed-research")
	if err != nil || !ok || seed.ID != "seed-research" {
		t.Fatalf("the seed should resolve by id: %+v", seed)
	}
	if got := materializeIfRetiringSeed(udb, "u", "seed-research", seed); got.ID != "seed-research" || got.Owner != seedOwner {
		t.Fatalf("by id it should run in place, got id=%q owner=%q", got.ID, got.Owner)
	}
	for _, a := range listAgents(udb, "u") {
		if a.Owner == "u" && a.Name == "Research" {
			t.Fatalf("no user-owned Research should have been made: %+v", a)
		}
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
	out := materializeIfRetiringSeed(udb, "u", "Research", resolved)
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
	if !isRetiringArchetypeSeed("seed-research") {
		t.Fatal("seed-research must be a retiring archetype seed")
	}
	if isRetiringArchetypeSeed("seed-chat") || isRetiringArchetypeSeed("seed-builder") || isRetiringArchetypeSeed("seed-kb") {
		t.Fatal("only research is a retiring archetype seed")
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

// The whole run, as Scribe and Collections make it: RunAgentSync of the seed
// by id answers, and leaves the user with no "Research" agent of their own.
// By name it still makes the copy, which is what keeps a standing mission
// that asks for "Research" working.
func TestRunningTheResearchSeedByIDLeavesNoAgentBehind(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Here is what the sources say.", Repeat: true}}}
	T.LLM, T.LeadLLM = fake, fake
	out, err := T.RunAgentSync(context.Background(), user, user, "seed-research", "What changed in the release?")
	if err != nil || !strings.Contains(out, "sources say") {
		t.Fatalf("the seed should answer: %v %q", err, out)
	}
	own := func() int {
		n := 0
		for _, a := range listAgents(udb, user) {
			if a.Owner == user && strings.EqualFold(a.Name, "Research") {
				n++
			}
		}
		return n
	}
	if n := own(); n != 0 {
		t.Fatalf("a run by id made %d Research agent(s) of the user's own", n)
	}
	if _, err := T.RunAgentSync(context.Background(), user, user, "Research", "And by name?"); err != nil {
		t.Fatal(err)
	}
	if n := own(); n != 1 {
		t.Errorf("a run by name should make the user's copy, made %d", n)
	}
}

// The agent assistant's Apply button.
//
// It shipped 2026-09-09 sending PATCH to /api/agents/<id>. That route had
// answered GET, POST and DELETE since three days earlier and has never answered
// PATCH, so every press returned "method not allowed" for nine days. Nothing
// caught it: the panel builds, the route is registered, the proposal arrives,
// and the failure is one verb deep on a path nothing exercises until a person
// clicks.
//
// So the pieces are pinned end to end: the verb the client sends, the route
// that serves it, the id it takes from the path, and the allowlist every
// spelling has to go through.

// applyBlock returns the Apply handler's client source.
func applyBlock(t *testing.T) string {
	t.Helper()
	src := readFile(t, "page_agent.go")
	i := strings.Index(src, "apply.onclick=function(){")
	if i < 0 {
		t.Fatal("the Apply button is gone")
	}
	end := strings.Index(src[i:], "body.appendChild(el('div',{class:'aa-chat'}")
	if end < 0 {
		t.Fatal("could not bound the Apply handler")
	}
	return src[i : i+end]
}

func TestApplyTargetsARouteThatServesItsVerb(t *testing.T) {
	block := applyBlock(t)
	if !strings.Contains(block, "method:'PATCH'") {
		t.Fatal("Apply no longer PATCHes; this test is reading the wrong place")
	}
	// Whichever spelling it uses, the route behind it has to accept PATCH.
	app, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	var path string
	switch {
	case strings.Contains(block, "'../api/agents/'+id"):
		path = "/api/agents/a1"
	case strings.Contains(block, "'../api/agents?id='"):
		path = "/api/agents?id=a1"
	default:
		t.Fatalf("Apply posts somewhere this test does not recognise; check the route serves PATCH:\n%s", block)
	}
	if w := patchAs(app, user, path, `{"description":"applied"}`); w.Code != http.StatusOK {
		t.Errorf("Apply PATCHes %s, which answers %d %s", path, w.Code, w.Body.String())
	}
}

// patchAs sends one write to the app's routes as user.
func patchAs(app *OrchestrateApp, user, path, body string) *httptest.ResponseRecorder {
	return writeAs(app, user, http.MethodPatch, path, body)
}

func writeAs(app *OrchestrateApp, user, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	routed(app)(w, asUser(httptest.NewRequest(method, path, strings.NewReader(body)), user))
	return w
}

// Every spelling must reach the SAME merge, or a second way to do something is
// a second set of rules for it. patchAgentFields leaves guardrails and the
// injection-scan settings out BY NAME so no partial save can weaken the rule it
// is about to be judged against; a path that skipped it would undo that.
func TestEverySpellingOfPatchGoesThroughTheAllowlist(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Ada", OrchestratorPrompt: "p", Guardrails: "never curse"}); err != nil {
		t.Fatal(err)
	}
	for _, sp := range []struct{ method, path string }{
		{http.MethodPatch, "/api/agents/a1"},
		{http.MethodPost, "/api/agents/a1"},
		{http.MethodPatch, "/api/agents?id=a1"},
	} {
		if w := writeAs(app, user, sp.method, sp.path, `{"guardrails":"anything goes"}`); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s changed a protected field: %d %s", sp.method, sp.path, w.Code, w.Body.String())
		}
		if w := writeAs(app, user, sp.method, sp.path, `{"description":"`+sp.method+`"}`); w.Code != http.StatusOK {
			t.Errorf("%s %s refused an allowed field: %d %s", sp.method, sp.path, w.Code, w.Body.String())
		}
	}
	if got, _ := loadAgent(udb, "a1"); got.Guardrails != "never curse" {
		t.Errorf("the guardrails changed: %q", got.Guardrails)
	}
	// The fields the assistant may propose all have to be settable, or Apply
	// succeeds on some changes and 400s on others with no warning beforehand.
	assist := readFile(t, "agent_assist.go")
	for field := range assistableFields {
		if !patchAgentFields[field] {
			t.Errorf("assist may propose %q but PATCH refuses it: Apply fails on any proposal touching it", field)
		}
		if !strings.Contains(assist, `"`+field+`"`) {
			t.Errorf("%q is not named in agent_assist.go", field)
		}
	}
}

// The target is the path's id, else the query's, else the body's; with none
// of them the PATCH is refused rather than guessed.
func TestThePathIdReachesTheMerge(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	for _, id := range []string{"a1", "a2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Owner: user, Name: id, OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ path, body, id string }{
		{"/api/agents/a1", `{"description":"by path"}`, "a1"},
		{"/api/agents?id=a2", `{"description":"by query"}`, "a2"},
		{"/api/agents", `{"id":"a1","description":"by body"}`, "a1"},
	} {
		if w := patchAs(app, user, c.path, c.body); w.Code != http.StatusOK {
			t.Fatalf("PATCH %s: %d %s", c.path, w.Code, w.Body.String())
		}
		got, _ := loadAgent(udb, c.id)
		if !strings.Contains(c.body, got.Description) {
			t.Errorf("PATCH %s landed elsewhere: %s has %q", c.path, c.id, got.Description)
		}
	}
	if w := patchAs(app, user, "/api/agents", `{"description":"nowhere"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a PATCH naming no agent: %d %s", w.Code, w.Body.String())
	}
}

// funcBody returns one function's source, from its signature to the closing
// brace at column 0.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("no %q", sig)
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("%q never closes", sig)
	}
	return src[i : i+j]
}

// A POST to one agent is a partial save, and the protected fields stay out of
// it: it once merged the whole body onto the record, so one POST could clear
// an agent's guardrails, switch off its scanner, or re-parent it past the move
// endpoint's check, which every other way to save an agent refuses.
func TestAPostToOneAgentCannotWeakenItsGuardrails(t *testing.T) {
	app, req, udb := authedApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p",
		Guardrails: "Never delete anything.", ScanToolResults: true}); err != nil {
		t.Fatal(err)
	}
	post := func(body map[string]any) int {
		w := httptest.NewRecorder()
		routed(app)(w, req("POST", "/api/agents/agent-1", body))
		return w.Code
	}
	for _, body := range []map[string]any{
		{"guardrails": ""},
		{"guardrails_disabled": true},
		{"scan_tool_results": false},
		{"owned_by": "agent-2"},
	} {
		if code := post(body); code < 400 {
			t.Errorf("POST %v answered %d, want it refused", body, code)
		}
	}
	got, _ := loadAgent(udb, "agent-1")
	if got.Guardrails != "Never delete anything." || got.GuardrailsDisabled || !got.ScanToolResults || got.OwnedBy != "" {
		t.Errorf("a refused POST changed the agent: %+v", got)
	}
	// The fields a partial save may change still change.
	if code := post(map[string]any{"name": "Renamed", "allowed_dispatch_targets": []string{"agent-3"}}); code != 200 {
		t.Fatalf("an allowed field answered %d", code)
	}
	if got, _ := loadAgent(udb, "agent-1"); got.Name != "Renamed" || len(got.AllowedDispatchTargets) != 1 {
		t.Errorf("an allowed POST did not save: %+v", got)
	}
}

// TestAgentMutationToolsResolveByName: agents(action="get") finds an agent by
// name, so a model that reads "Moltbook Conversational Agent" and then calls
// update_agent with that same string must reach the same record. It did not:
// update_agent looked the id up directly, said "not found", and the turn went
// off narrating instead (seen live 2026-09-07). The three mutation tools now
// share the get path's name-or-id resolution.
func TestAgentMutationToolsResolveByName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	saved, err := saveAgent(db, AgentRecord{Name: "Moltbook Conversational Agent", Owner: owner, OrchestratorPrompt: "posts things"})
	if err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: owner, DB: db}

	out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": "Moltbook Conversational Agent", "description": "updated by name"}, sess)
	if err != nil {
		t.Fatalf("update_agent by name: %v", err)
	}
	if !strings.Contains(out, "AGENT_UPDATED") {
		t.Fatalf("update_agent did not report success: %s", out)
	}
	got, ok := loadAgent(db, saved.ID)
	if !ok || got.Description != "updated by name" {
		t.Fatalf("the update did not land on the named agent: ok=%v desc=%q", ok, got.Description)
	}

	out, err = cloneAgentTool{}.RunWithSession(map[string]any{"id": "Moltbook Conversational Agent", "name": "Moltbook Copy"}, sess)
	if err != nil {
		t.Fatalf("clone_agent by name: %v", err)
	}
	if !strings.Contains(out, "AGENT_CLONED") {
		t.Fatalf("clone_agent did not report success: %s", out)
	}

	if _, err := (deleteAgentTool{}).RunWithSession(map[string]any{"id": "Moltbook Copy"}, sess); err != nil {
		t.Fatalf("delete_agent by name: %v", err)
	}
	for _, a := range listAgents(db, owner) {
		if a.Name == "Moltbook Copy" {
			t.Fatal("delete_agent by name left the agent in place")
		}
	}

	// A name that is nobody's agent still fails, and says so.
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "No Such Agent"}, sess); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error for an unknown name, got %v", err)
	}
}

// Editing an agent used to be a straight overwrite: after a behaviour
// regression there was no answer to "what did this say last week" and no way
// back. One authoring session burned four saves and every one was final.
func TestSavingAnAgentKeepsThePriorVersion(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "answer briefly"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A creation replaces nothing, so it has no history of its own.
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Fatalf("a new agent starts with history: %+v", revs)
	}

	edited := created
	edited.OrchestratorPrompt = "answer at length"
	if _, err := saveAgentAs(db, edited, "edited instructions"); err != nil {
		t.Fatalf("edit: %v", err)
	}

	revs := revisions.List(db, revisions.KindAgent, created.ID)
	if len(revs) != 1 {
		t.Fatalf("got %d revisions, want 1", len(revs))
	}
	if revs[0].Reason != "edited instructions" {
		t.Errorf("reason = %q", revs[0].Reason)
	}
	var back AgentRecord
	if !revisions.Load(db, revisions.KindAgent, created.ID, "", &back) {
		t.Fatal("the kept version did not load")
	}
	if back.OrchestratorPrompt != "answer briefly" {
		t.Errorf("kept prompt = %q, want the version that was replaced", back.OrchestratorPrompt)
	}
}

// A ring six deep cannot afford writes that changed nothing. A re-save with no
// edit in it, which a debounced editor produces per keystroke pause, would
// otherwise evict every version worth keeping.
func TestResavingAnUnchangedAgentFilesNothing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "answer briefly"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := saveAgent(db, created); err != nil {
			t.Fatalf("re-save: %v", err)
		}
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Errorf("a save that changed nothing filed %d revision(s)", len(revs))
	}
}

func TestDeletingAnAgentDropsItsHistory(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1", Owner: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := created
	edited.OrchestratorPrompt = "v2"
	if _, err := saveAgent(db, edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 1 {
		t.Fatalf("setup: got %d revisions", len(revs))
	}
	if err := deleteAgent(db, created.ID, "u"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Errorf("history outlived the agent: %+v", revs)
	}
}

// The point of keeping versions: walk an agent back to what it used to say.
func TestRollingBackAnAgentRestoresTheKeptVersion(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, p := range []string{"v2", "v3", "v4"} {
		next := created
		next.OrchestratorPrompt = p
		if _, err := saveAgentAs(db, next, "edited instructions"); err != nil {
			t.Fatalf("edit %s: %v", p, err)
		}
		created = next
	}

	revs := revisions.List(db, revisions.KindAgent, created.ID)
	if len(revs) != 3 {
		t.Fatalf("got %d revisions, want 3", len(revs))
	}
	// Find the entry holding v1 and go back to it by id, the way an owner
	// reading a listing would.
	var target int
	for _, r := range revs {
		var body AgentRecord
		if revisions.Load(db, revisions.KindAgent, created.ID, strconv.Itoa(r.Seq), &body) && body.OrchestratorPrompt == "v1" {
			target = r.Seq
		}
	}
	if target == 0 {
		t.Fatal("no kept version holds v1")
	}

	back, err := rollbackAgent(db, created.ID, strconv.Itoa(target))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if back.OrchestratorPrompt != "v1" {
		t.Errorf("restored prompt = %q, want v1", back.OrchestratorPrompt)
	}
	stored, ok := loadAgent(db, created.ID)
	if !ok || stored.OrchestratorPrompt != "v1" {
		t.Errorf("stored prompt = %q, want v1", stored.OrchestratorPrompt)
	}

	// The rollback files what it replaced, so going back is itself reversible.
	after := revisions.List(db, revisions.KindAgent, created.ID)
	if len(after) != 4 {
		t.Fatalf("got %d revisions after rollback, want 4", len(after))
	}
	if after[0].Reason != "rolled back to #"+strconv.Itoa(target) {
		t.Errorf("newest reason = %q", after[0].Reason)
	}
	var replaced AgentRecord
	if !revisions.Load(db, revisions.KindAgent, created.ID, "", &replaced) || replaced.OrchestratorPrompt != "v4" {
		t.Errorf("the rollback did not file the version it replaced: %q", replaced.OrchestratorPrompt)
	}
}

func TestRollingBackWithoutHistoryRefuses(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := rollbackAgent(db, created.ID, ""); err == nil {
		t.Error("rolling back an agent with no kept versions must say so")
	}
	if _, err := rollbackAgent(db, created.ID, "99"); err == nil {
		t.Error("an id that was never issued must not resolve")
	}
}

// Exposed used to mean two things at once: everybody may use this, AND put a
// card on the dashboard. The public surface needed an access gate and the app
// grant happened to be one, so the two welded together — which meant an owner
// could not add a shortcut without widening who could use the agent, or widen
// it without putting a card in front of people who never asked for one.

// A record written before the split keeps behaving exactly as it did: it was
// usable by everybody and it had a card, which is both halves set.
func TestAPublishedRecordMigratesToBothHalves(t *testing.T) {
	got := migrateExposedFlag(AgentRecord{ID: "a1", Exposed: true})
	if !got.Everyone || !got.ShowOnDashboard {
		t.Errorf("the split lost a half: %+v", got)
	}
	// And the old flag is cleared, so the migration is idempotent rather than
	// re-deciding on every read.
	if got.Exposed {
		t.Error("the retired flag survived the migration")
	}
	// An already-split record is untouched, including one the owner has
	// deliberately narrowed to a shortcut with no reach.
	shortcut := AgentRecord{ID: "a2", ShowOnDashboard: true}
	if out := migrateExposedFlag(shortcut); out.Everyone {
		t.Errorf("a shortcut-only agent was widened by the migration: %+v", out)
	}
}

// The reach is an administrator's to grant; the shortcut is nobody's business
// but the owner's. A PATCH used to apply either directly, so somebody who
// could not flip the toggle on the privileges card could publish to the whole
// deployment through the other door.
func TestPatchCannotPublishWithoutApproval(t *testing.T) {
	T, udb, _ := newTestOrchestrate(t)
	rec, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Wren",
		OrchestratorPrompt: "help"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	requestIsAdminAgent = func(*http.Request) bool { return false }
	t.Cleanup(func() { requestIsAdminAgent = RequestIsAdmin })

	r := httptest.NewRequest(http.MethodPatch, "/api/agents/a1",
		strings.NewReader(`{"exposed":true}`))
	routed(T)(httptest.NewRecorder(), as(t, r, "alice"))

	got, _ := loadAgent(udb, rec.ID)
	if got.Everyone {
		t.Error("a PATCH published the agent without an administrator")
	}
	// The shortcut takes the same door and IS applied, because it grants
	// nothing.
	r = httptest.NewRequest(http.MethodPatch, "/api/agents/a1",
		strings.NewReader(`{"show_on_dashboard":true}`))
	routed(T)(httptest.NewRecorder(), as(t, r, "alice"))
	if got, _ := loadAgent(udb, rec.ID); !got.ShowOnDashboard {
		t.Error("a shortcut was refused; it grants nothing and needs nobody")
	}
}

// Hiding an agent from the fleet list leaves its owner no way to open it, so
// the save path adds a SHORTCUT. It must not also widen who can use it — that
// was the old flag doing two things, and the default only ever meant one.
func TestHidingAnAgentAddsAShortcutNotReach(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	saved, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Helper",
		OrchestratorPrompt: "x", Hidden: true})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.ShowOnDashboard {
		t.Error("a hidden agent got no surface to reach it")
	}
	if saved.Everyone {
		t.Error("hiding an agent published it to the whole deployment")
	}
}
