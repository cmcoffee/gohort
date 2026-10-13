package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// delegationFixture: alice owns two agents, one allowed the "weather" API and
// one not, a tool scoped to each, a Builder-only tool and a tool that calls
// weather.
func delegationFixture(t *testing.T) (udb Database) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb = UserDB(root, "alice")
	prevRoot := RootDB
	RootDB = root
	secStore := &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { RootDB = prevRoot; AuthDB = prevAuth })

	for _, id := range []string{"a-allowed", "a-other"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Name: id, Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Secure().Save(SecureCredential{Name: "weather", Type: SecureCredBearer, BaseURL: "https://weather.test", Owner: "alice"}, "k"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().SetOnlyAgentsOwned("alice", "weather", []string{"a-allowed"}); err != nil {
		t.Fatal(err)
	}
	persist := func(tt TempTool, scope ...string) {
		t.Helper()
		if tt.Description == "" {
			tt.Description = "d"
		}
		if err := AdminPersistTempTool(udb, "alice", tt); err != nil {
			t.Fatal(err)
		}
		if len(scope) > 0 && !SetUserToolScopeAgents(udb, "alice", tt.Name, scope) {
			t.Fatalf("scope %s", tt.Name)
		}
	}
	persist(TempTool{Name: "allowed_kit", CommandTemplate: "echo a"}, "a-allowed")
	persist(TempTool{Name: "other_kit", CommandTemplate: "echo o"}, "a-other")
	persist(TempTool{Name: "builder_scratch", CommandTemplate: "echo b", BuilderOnly: true})
	persist(TempTool{Name: "forecast", CommandTemplate: "echo f", Credential: "weather"})
	return udb
}

func builderSession(udb Database, sender string) *ToolSession {
	return &ToolSession{Username: "alice", DB: udb, AgentID: "seed-builder", DispatchParentAgentID: sender}
}

func builderKit(udb Database, sess *ToolSession) map[string]bool {
	turn := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "seed-builder", Owner: "alice"}}
	turn.loadAgentTempTools(sess, "alice", udb)
	got := map[string]bool{}
	for _, tt := range sess.TempTools {
		got[tt.Name] = true
	}
	return got
}

// The owner driving Builder reaches every tool and every API of theirs.
func TestOwnerDrivenBuilderIsUnrestricted(t *testing.T) {
	udb := delegationFixture(t)
	sess := builderSession(udb, "")
	applyBuilderDelegation(sess, udb)
	if sess.ToolEditRefusal != nil {
		t.Error("the owner's own Builder run was given a refusal")
	}
	if deny := credentialDenyFor(AgentRecord{ID: "seed-builder"}, sess); deny["weather"] {
		t.Error("the owner's own Builder run was denied an API narrowed to other agents")
	}
	kit := builderKit(udb, builderSession(udb, ""))
	for _, n := range []string{"allowed_kit", "other_kit", "builder_scratch", "forecast"} {
		if !kit[n] {
			t.Errorf("owner-driven Builder did not load %s", n)
		}
	}
}

// Sent by an agent, Builder has that agent's tools and APIs and no more.
func TestSentBuilderWorksWithinTheSendersLimits(t *testing.T) {
	udb := delegationFixture(t)

	kit := builderKit(udb, builderSession(udb, "a-other"))
	if !kit["other_kit"] {
		t.Error("the sender's own tool did not load")
	}
	for _, n := range []string{"allowed_kit", "builder_scratch"} {
		if kit[n] {
			t.Errorf("%s loaded for a run sent by an agent that cannot use it", n)
		}
	}

	sess := builderSession(udb, "a-other")
	applyBuilderDelegation(sess, udb)
	if !sess.CredentialDenied("weather") {
		t.Error("a run sent by an agent off the API's list may call the API")
	}
	if sess.ToolEditRefusal == nil {
		t.Fatal("no edit refusal on a sent run")
	}
	for tool, cred := range map[string]string{"allowed_kit": "", "builder_scratch": "", "forecast": "", "new_tool": "weather"} {
		if msg := sess.ToolEditRefusal(tool, cred); !strings.Contains(msg, "NOT DONE") || !strings.Contains(msg, "a-other") {
			t.Errorf("%s/%s: a sent run may change it: %q", tool, cred, msg)
		}
	}
	for tool, cred := range map[string]string{"other_kit": "", "new_tool": ""} {
		if msg := sess.ToolEditRefusal(tool, cred); msg != "" {
			t.Errorf("%s/%s: refused something the sender can use: %q", tool, cred, msg)
		}
	}

	// Sent by the allowed agent, the API is there.
	ok := builderSession(udb, "a-allowed")
	applyBuilderDelegation(ok, udb)
	if ok.CredentialDenied("weather") {
		t.Error("a run sent by an agent on the API's list was denied it")
	}
	if msg := ok.ToolEditRefusal("forecast", "weather"); msg != "" {
		t.Errorf("the allowed agent's Builder run may not fix its API tool: %q", msg)
	}
}

// Builder's own API tools refuse an API the sender may not use.
func TestSentBuilderAPIToolsRefuse(t *testing.T) {
	udb := delegationFixture(t)
	sess := builderSession(udb, "a-other")
	applyBuilderDelegation(sess, udb)
	called := false
	tools := limitCredentialToolsToDelegator(sess, []AgentToolDef{{
		Tool:    Tool{Name: "check_credential"},
		Handler: func(context.Context, map[string]any) (string, error) { called = true; return "ok", nil },
	}})
	out, _ := tools[0].Handler(context.Background(), map[string]any{"name": "weather"})
	if called || !strings.Contains(out, "NOT DONE") {
		t.Errorf("check_credential reached a denied API: %q", out)
	}
	if out, _ := tools[0].Handler(context.Background(), map[string]any{"name": "news"}); out != "ok" {
		t.Errorf("an API the sender may use was refused: %q", out)
	}
}

// A sender that cannot be read is treated as having nothing: fails closed.
func TestUnreadableSenderFailsClosed(t *testing.T) {
	udb := delegationFixture(t)
	kit := builderKit(udb, builderSession(udb, "a-gone"))
	if len(kit) != 0 {
		t.Errorf("a run sent by an unknown agent loaded tools: %v", kit)
	}
	sess := builderSession(udb, "a-gone")
	applyBuilderDelegation(sess, udb)
	if !sess.CredentialDenied("weather") {
		t.Error("a run sent by an unknown agent may call a narrowed API")
	}
}

// Building an agent for the sender is not a way past its limits: the tools
// an authoring call grants or attaches are held to what the sender may use.
func TestSentBuilderCannotGiveAnAgentWhatTheSenderLacks(t *testing.T) {
	udb := delegationFixture(t)
	sess := builderSession(udb, "a-other")
	applyBuilderDelegation(sess, udb)

	refused := map[string]struct {
		inline  []TempTool
		allowed []string
	}{
		"a tool scoped to another agent": {allowed: []string{"allowed_kit"}},
		"a Builder-only tool":            {allowed: []string{"builder_scratch"}},
		"an inline tool on a denied API": {inline: []TempTool{{Name: "fresh", Credential: "weather"}}},
		"a script reaching a denied API": {inline: []TempTool{{Name: "fresh", HookCapabilities: []string{"fetch_via:weather"}}}},
	}
	for name, c := range refused {
		if msg := agentToolsRefusal(sess, c.inline, c.allowed); !strings.Contains(msg, "NOT DONE") {
			t.Errorf("%s was allowed", name)
		}
	}
	if msg := agentToolsRefusal(sess, []TempTool{{Name: "fresh"}}, []string{"other_kit", "*"}); msg != "" {
		t.Errorf("the sender's own tools, or every tool for a sender with no list, were refused: %q", msg)
	}

	// A sender with its own tool list cannot hand out every tool.
	if _, err := saveAgent(udb, AgentRecord{ID: "a-listed", Name: "a-listed", Owner: "alice", OrchestratorPrompt: "p", AllowedTools: []string{"other_kit"}}); err != nil {
		t.Fatal(err)
	}
	listed := builderSession(udb, "a-listed")
	applyBuilderDelegation(listed, udb)
	if msg := agentToolsRefusal(listed, nil, []string{"*"}); !strings.Contains(msg, "NOT DONE") {
		t.Error("a sender with a tool list handed out every tool")
	}

	// End to end through create_agent: refused, and nothing saved.
	out, err := createAgentTool{}.RunWithSession(map[string]any{
		"name": "Helper", "description": "d", "orchestrator_prompt": "p",
		"allowed_tools": []any{"allowed_kit"},
	}, sess)
	if err != nil || !strings.Contains(out, "NOT DONE") {
		t.Fatalf("create_agent granted a tool the sender lacks: %q %v", out, err)
	}
	for _, a := range listAgents(udb, "alice") {
		if a.Name == "Helper" {
			t.Error("the refused agent was saved anyway")
		}
	}

	// The owner's own Builder run is not held to anything.
	if msg := agentToolsRefusal(builderSession(udb, ""), nil, []string{"allowed_kit"}); msg != "" {
		t.Errorf("the owner's own Builder run was refused: %q", msg)
	}
}
