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

// Clarifying-questions guidance must be LIFTED OUT of the Chat seed persona.
func TestClarifyingRemovedFromChatSeed(t *testing.T) {
	if strings.Contains(chatSeed(t).OrchestratorPrompt, clarifyingSectionHeading) {
		t.Fatalf("Chat seed still contains %q", clarifyingSectionHeading)
	}
}

// ...injected on the interactive surface (hasPlanSet true), gated off otherwise.
func TestFrameworkGatesClarifyingOnInteractiveSurface(t *testing.T) {
	if !strings.Contains(frameworkPromptBlocks("", chatSeed(t), true), clarifyingSectionHeading) {
		t.Fatal("clarifying block missing when hasPlanSet=true")
	}
	if strings.Contains(frameworkPromptBlocks("", chatSeed(t), false), clarifyingSectionHeading) {
		t.Fatal("clarifying block leaked onto a non-interactive surface (hasPlanSet=false)")
	}
}

// tools-self-serve and export must be LIFTED OUT of the Chat seed persona.
func TestToolBlocksRemovedFromChatSeed(t *testing.T) {
	p := chatSeed(t).OrchestratorPrompt
	if strings.Contains(p, toolsSelfServeMarker) {
		t.Fatalf("Chat seed still contains the tools-self-serve block (%q)", toolsSelfServeMarker)
	}
	if strings.Contains(p, exportMarker) {
		t.Fatalf("Chat seed still contains the export block (%q)", exportMarker)
	}
}

// Default-pool agent (empty AllowedTools, like Chat) has tool_def + export, so
// both blocks inject.
func TestFrameworkInjectsToolBlocksForDefaultPool(t *testing.T) {
	got := frameworkPromptBlocks("", chatSeed(t), true)
	if !strings.Contains(got, toolsSelfServeMarker) {
		t.Fatal("tools-self-serve block missing for a default-pool agent")
	}
	if !strings.Contains(got, exportMarker) {
		t.Fatal("export block missing for a default-pool agent")
	}
}

// A restricted allowlist gates each tool block on membership — no false "use
// tool_def" prompt for an agent whose allowlist doesn't include it.
func TestFrameworkGatesToolBlocksOnAllowlist(t *testing.T) {
	without := AgentRecord{ID: "restricted", AllowedTools: []string{"web_search"}}
	if strings.Contains(frameworkPromptBlocks("", without, true), toolsSelfServeMarker) {
		t.Fatal("tools-self-serve block appeared for an agent whose allowlist lacks tool_def")
	}
	with := AgentRecord{ID: "authoring", AllowedTools: []string{"tool_def"}}
	if !strings.Contains(frameworkPromptBlocks("", with, true), toolsSelfServeMarker) {
		t.Fatal("tools-self-serve block missing for an agent that allowlists tool_def")
	}
}

// Channel + fleet supervision must be LIFTED OUT of the Chat seed persona.
func TestChannelFleetRemovedFromChatSeed(t *testing.T) {
	p := chatSeed(t).OrchestratorPrompt
	if strings.Contains(p, channelSectionHeading) {
		t.Fatalf("Chat seed still contains the channel block (%q)", channelSectionHeading)
	}
	if strings.Contains(p, fleetSupervisionMarker) {
		t.Fatalf("Chat seed still contains the fleet block (%q)", fleetSupervisionMarker)
	}
}

// The channel block is gated on Cortex; the fleet block on Fleet. A Cortex-only
// agent gets the home-thread guidance but not fleet supervision, and vice-versa.
func TestFrameworkGatesChannelAndFleetIndependently(t *testing.T) {
	cortexOnly := AgentRecord{ID: "cx", Cortex: true}
	got := frameworkPromptBlocks("", cortexOnly, false)
	if !strings.Contains(got, channelSectionHeading) {
		t.Fatal("channel block missing for a Cortex agent")
	}
	if strings.Contains(got, fleetSupervisionMarker) {
		t.Fatal("fleet block leaked onto a non-Fleet agent")
	}

	fleetOnly := AgentRecord{ID: "fl", Fleet: true}
	got = frameworkPromptBlocks("", fleetOnly, false)
	if !strings.Contains(got, fleetSupervisionMarker) {
		t.Fatal("fleet block missing for a Fleet agent")
	}
	if strings.Contains(got, channelSectionHeading) {
		t.Fatal("channel block leaked onto a non-Cortex agent")
	}
}

// Chat (Cortex+Fleet) reconstructs the original single section: the heading, then
// the channel paragraph, then the fleet paragraphs, in order.
func TestChatReconstructsChannelThenFleet(t *testing.T) {
	got := frameworkPromptBlocks("", chatSeed(t), true)
	ch := strings.Index(got, channelSectionHeading)
	fl := strings.Index(got, fleetSupervisionMarker)
	if ch < 0 || fl < 0 {
		t.Fatalf("expected both channel and fleet blocks for Chat (ch=%d fl=%d)", ch, fl)
	}
	if ch > fl {
		t.Fatal("channel heading should precede the fleet block, matching the original section order")
	}
}

// How-to-decide and Work-it-honestly must be LIFTED OUT of the Chat seed.
func TestHowToDecideAndWorkHonestlyRemovedFromChatSeed(t *testing.T) {
	p := chatSeed(t).OrchestratorPrompt
	if strings.Contains(p, howToDecideSectionHeading) {
		t.Fatalf("Chat seed still contains %q", howToDecideSectionHeading)
	}
	if strings.Contains(p, workHonestlyMarker) {
		t.Fatalf("Chat seed still contains %q", workHonestlyMarker)
	}
}

// ...injected on the interactive surface, gated off otherwise.
func TestFrameworkGatesHowToDecideAndWorkHonestly(t *testing.T) {
	on := frameworkPromptBlocks("", chatSeed(t), true)
	if !strings.Contains(on, howToDecideSectionHeading) || !strings.Contains(on, workHonestlyMarker) {
		t.Fatal("how-to-decide / work-honestly missing when hasPlanSet=true")
	}
	off := frameworkPromptBlocks("", chatSeed(t), false)
	if strings.Contains(off, howToDecideSectionHeading) || strings.Contains(off, workHonestlyMarker) {
		t.Fatal("how-to-decide / work-honestly leaked onto a non-interactive surface")
	}
}

// Milestone: with every framework block lifted, the Chat seed persona is now
// pure voice — no "## " framework sections and none of the lifted markers.
func TestChatSeedIsPurePersona(t *testing.T) {
	p := chatSeed(t).OrchestratorPrompt
	if strings.Contains(p, "\n## ") || strings.HasPrefix(p, "## ") {
		t.Fatalf("Chat seed still contains a '## ' framework section:\n%s", p)
	}
	for _, marker := range []string{
		planSetSectionHeading, clarifyingSectionHeading, toolsSelfServeMarker,
		exportMarker, builderRoutingMarker, channelSectionHeading,
		fleetSupervisionMarker, howToDecideSectionHeading, workHonestlyMarker,
	} {
		if strings.Contains(p, marker) {
			t.Fatalf("Chat seed still contains lifted marker %q", marker)
		}
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
