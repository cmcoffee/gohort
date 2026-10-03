package prompts

import "testing"

// A named tool's shipped description becomes a block the first time it is
// seen, remembered for the next start; an edit replaces it, for every tier or
// for one; a tool not named is never touched.
func TestToolDescriptionsAreBlocks(t *testing.T) {
	store := jsonStore{}
	SetPromptOverrideDB(store)
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	RegisterTunableTools(ToolGroupAuthoring, "test_tool_def")

	if got := ToolDescriptionFor(TierWorker, "someones_own_tool", "theirs"); got != "theirs" {
		t.Fatalf("an unnamed tool was changed: %q", got)
	}
	ObserveToolDescription("test_tool_def", "Define a tool.")
	var b PromptBlock
	for _, x := range AllPromptBlocks() {
		if x.Key == ToolBlockKey("test_tool_def") {
			b = x
		}
	}
	if b.Text != "Define a tool." || b.Category != ToolGroupAuthoring {
		t.Fatalf("block = %+v", b)
	}
	var kept PromptBlock
	if !store.Get(OverrideTable, observedToolPrefix+"test_tool_def", &kept) || kept.Text != "Define a tool." {
		t.Fatal("the shipped description was not kept for the next start")
	}

	if got := ToolDescriptionFor(TierWorker, "test_tool_def", "Define a tool."); got != "Define a tool." {
		t.Fatalf("no edit, yet %q", got)
	}
	SetPromptOverride(ToolBlockKey("test_tool_def"), "Define a tool, then test it.")
	SetPromptTierOverride(TierWorker, ToolBlockKey("test_tool_def"), "DEFINE, THEN TEST.", "")
	if got := ToolDescriptionFor(TierLead, "test_tool_def", "Define a tool."); got != "Define a tool, then test it." {
		t.Fatalf("lead reads %q", got)
	}
	if got := ToolDescriptionFor(TierWorker, "test_tool_def", "Define a tool."); got != "DEFINE, THEN TEST." {
		t.Fatalf("worker reads %q", got)
	}
}

// A description that keeps changing is built per caller: after a few
// changes it stops being editable, so an edit cannot overwrite what each
// caller was meant to read.
func TestAToolBuiltPerCallerIsLeftAlone(t *testing.T) {
	SetPromptOverrideDB(jsonStore{})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	RegisterTunableTools(ToolGroupFramework, "test_per_caller")
	SetPromptOverride(ToolBlockKey("test_per_caller"), "edited")
	told := ""
	SetUnstableToolLogger(func(name string) { told = name })
	for _, d := range []string{"for alice", "for bob", "for alice", "for bob"} {
		ObserveToolDescription("test_per_caller", d)
	}
	if TunableToolGroup("test_per_caller") != "" || told != "test_per_caller" {
		t.Fatalf("group %q, told %q", TunableToolGroup("test_per_caller"), told)
	}
	if got := ToolDescriptionFor(TierWorker, "test_per_caller", "for carol"); got != "for carol" {
		t.Fatalf("an edit still applied: %q", got)
	}
}

// A named tool's parameters are blocks too: each recorded as it ships and
// listed so it comes back after a restart, each editable for every model or
// one, and a parameter of a tool not named is never touched.
func TestToolParameterDescriptionsAreBlocks(t *testing.T) {
	store := jsonStore{}
	SetPromptOverrideDB(store)
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	RegisterTunableTools(ToolGroupAuthoring, "test_app_def")

	ObserveToolParamDescription("test_app_def", "sections", "Ordered sections.")
	ObserveToolParamDescription("someones_tool", "q", "theirs")
	key := ToolParamBlockKey("test_app_def", "sections")
	var found, foreign bool
	for _, b := range AllPromptBlocks() {
		found = found || (b.Key == key && b.Text == "Ordered sections." && b.Title == "test_app_def: sections")
		foreign = foreign || b.Key == ToolParamBlockKey("someones_tool", "q")
	}
	if !found || foreign {
		t.Fatalf("found %v, foreign %v", found, foreign)
	}
	var index []string
	if !store.Get(OverrideTable, observedIndexKey, &index) || !tierHas(index, key) {
		t.Fatalf("index = %v", index)
	}
	SetPromptTierOverride(TierLead, key, "Sections, in order, each with a kind.", "")
	if got := ToolParamDescriptionFor(TierLead, "test_app_def", "sections", "Ordered sections."); got != "Sections, in order, each with a kind." {
		t.Fatalf("lead reads %q", got)
	}
	if got := ToolParamDescriptionFor(TierWorker, "test_app_def", "sections", "Ordered sections."); got != "Ordered sections." {
		t.Fatalf("worker reads %q", got)
	}
}
