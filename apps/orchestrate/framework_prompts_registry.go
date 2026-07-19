package orchestrate

import . "github.com/cmcoffee/gohort/core"

// Surface the capability-gated framework blocks (framework_prompts.go) on the
// Prompts page. This is display metadata only — the injection path still reads
// the constants directly; registering here just makes the otherwise-hidden text
// visible to operators. When the RuleSet editing policy lands, the assembler
// will read overrides keyed by these Keys.
func init() {
	reg := func(key, title, category, gate, text string) {
		RegisterPromptBlock(PromptBlock{Key: key, Title: title, Category: category, Gate: gate, Text: text})
	}
	reg("framework.how_to_decide", "How to decide", "Orchestration", "Interactive surface", frameworkHowToDecideBlock)
	reg("framework.plan_set", "Inline tools vs plan_set", "Orchestration", "Interactive surface (plan_set)", frameworkPlanSetBlock)
	reg("framework.clarifying", "Asking clarifying questions", "Orchestration", "Interactive surface (ask_user)", frameworkClarifyingBlock)
	reg("framework.work_honestly", "Work it honestly", "Orchestration", "Interactive surface", frameworkWorkHonestlyBlock)
	reg("framework.tools_self_serve", "Tools are self-serve", "Authoring", "Agent has tool_def", frameworkToolsSelfServeBlock)
	reg("framework.export", "Document export", "Authoring", "Agent has export", frameworkExportBlock)
	reg("framework.builder_routing", "Apps / agents / pipelines → Builder", "Authoring", "Fleet, not Builder", frameworkBuilderRoutingBlock)
	reg("framework.channel", "Your channel", "Fleet", "Cortex", frameworkChannelBlock())
	reg("framework.fleet", "Supervising the fleet", "Fleet", "Fleet", frameworkFleetBlock)
}
