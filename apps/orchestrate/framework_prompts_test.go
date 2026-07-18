package orchestrate

import "strings"
import "testing"

func chatSeed(t *testing.T) AgentRecord {
	t.Helper()
	for _, s := range coreSeedAgents() {
		if s.Name == "Chat" {
			return s
		}
	}
	t.Fatal("Chat seed not found in coreSeedAgents()")
	return AgentRecord{}
}

// The plan_set guidance must be LIFTED OUT of the Chat seed persona — no longer
// hand-authored into the one seed that happened to carry it.
func TestPlanSetSectionRemovedFromChatSeed(t *testing.T) {
	if strings.Contains(chatSeed(t).OrchestratorPrompt, planSetSectionHeading) {
		t.Fatalf("Chat seed still contains %q — it should be lifted into framework_prompts.go", planSetSectionHeading)
	}
}

// ...and re-injected by the framework block on any surface that actually offers
// plan_set (the web chat surface passes hasPlanSet=true), so the behavior the
// Chat agent had is preserved — and Research/Knowledge/user agents now inherit
// it too.
func TestFrameworkInjectsPlanSetWhenSurfaceHasIt(t *testing.T) {
	got := frameworkPromptBlocks("", chatSeed(t), true)
	if !strings.Contains(got, planSetSectionHeading) {
		t.Fatalf("framework blocks missing %q when hasPlanSet=true", planSetSectionHeading)
	}
}

// ...and gated OFF where the surface has no plan_set (dispatch passes false), so
// an agent is never told to use a tool the surface can't give it.
func TestFrameworkOmitsPlanSetWhenSurfaceLacksIt(t *testing.T) {
	// Check the section HEADING, not the bare word "plan_set" — other blocks
	// (Builder-routing) legitimately mention plan_set in prose.
	if strings.Contains(frameworkPromptBlocks("", chatSeed(t), false), planSetSectionHeading) {
		t.Fatal("plan_set section leaked onto a surface without plan_set (hasPlanSet=false)")
	}
}

func seedNamed(t *testing.T, name string) AgentRecord {
	t.Helper()
	for _, s := range coreSeedAgents() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("seed %q not found in coreSeedAgents()", name)
	return AgentRecord{}
}

// Builder-routing guidance must be LIFTED OUT of the Chat seed persona.
func TestBuilderRoutingRemovedFromChatSeed(t *testing.T) {
	if strings.Contains(chatSeed(t).OrchestratorPrompt, builderRoutingMarker) {
		t.Fatalf("Chat seed still contains the Builder-routing block (%q)", builderRoutingMarker)
	}
}

// ...and injected for a delegating (Fleet) agent that is not Builder — Chat has
// Fleet: true, so its behavior is preserved and Research/user fleet agents now
// inherit it too.
func TestFrameworkInjectsBuilderRoutingForFleetAgent(t *testing.T) {
	chat := chatSeed(t)
	if !chat.Fleet {
		t.Fatal("precondition: Chat seed expected to have Fleet: true")
	}
	if !strings.Contains(frameworkPromptBlocks("", chat, true), builderRoutingMarker) {
		t.Fatal("Builder-routing block missing for a Fleet, non-Builder agent")
	}
}

// ...but NOT for Builder itself — routing the authoring agent to itself is
// nonsense.
func TestFrameworkOmitsBuilderRoutingForBuilder(t *testing.T) {
	builder := seedNamed(t, "Builder")
	if !isBuilderAgent(builder.ID) {
		t.Fatalf("precondition: %q expected to be the Builder seed", builder.ID)
	}
	if strings.Contains(frameworkPromptBlocks("", builder, true), builderRoutingMarker) {
		t.Fatal("Builder-routing block leaked onto Builder itself")
	}
}

// ...and NOT for a non-delegating (no-Fleet) agent — it has no way to hand work
// to Builder, so the guidance is noise.
func TestFrameworkOmitsBuilderRoutingWithoutFleet(t *testing.T) {
	a := AgentRecord{ID: "some-agent", Fleet: false}
	if strings.Contains(frameworkPromptBlocks("", a, true), builderRoutingMarker) {
		t.Fatal("Builder-routing block appeared for a non-Fleet agent")
	}
}

// Dedup guard: a pre-lift clone froze the section into its own persona. The
// framework must NOT inject a second copy — the block already present in the
// prompt-so-far wins.
func TestFrameworkDoesNotDuplicateBlockAlreadyInPersona(t *testing.T) {
	existing := "Some cloned persona text\n\n" + frameworkPlanSetBlock + "\n\nmore text"
	// Non-Fleet agent so only the plan_set block is in play (Builder-routing is
	// Fleet-gated); its heading is already in `existing`, so it must NOT repeat.
	a := AgentRecord{ID: "clone-x", Fleet: false}
	if got := frameworkPromptBlocks(existing, a, true); strings.Contains(got, planSetSectionHeading) {
		t.Fatalf("framework injected a duplicate plan_set block; got:\n%s", got)
	}
}
