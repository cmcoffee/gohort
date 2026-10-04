// Which shipped tools' descriptions are prompt blocks.
//
// The descriptions Builder builds with decide as much about how it builds as
// its prompt does: when a machine and when a pipeline, what makes a tool
// definition done. Named here, each becomes a prompt block, worded per
// tier and tunable by the tuning harness
// (core/prompts/tool_desc.go).
//
// Authoring is every tool Builder authors with; a test keeps the list equal
// to what builderAuthoringTools hands out, so a new authoring tool is a
// block from the start. Framework is the turn plumbing an agent builds and
// verifies through. Left out on purpose: tools whose description differs by
// caller (agents has a read-only variant for Builder; plan_set's carries the
// agent's step budget; workspace and introspect describe the caller), since
// one edit would overwrite what each caller was meant to read.

package orchestrate

import "github.com/cmcoffee/gohort/core/prompts"

var authoringToolNames = []string{
	"survey", "create_agent", "update_agent", "list_reference_sources", "archetype", "clone_agent",
	"delete_agent", "add_tool", "tool_def", "skill_def", "bridge", "connector", "tool_template",
	"collections", "draft_oauth_credential", "draft_api_credential", "update_api_credential",
	"store_credential_secret", "check_credential", "bulletins",
}

// Authored with outside builderAuthoringTools: the three things Builder makes
// that are not tools or agents.
var authoringPrimitiveNames = []string{"app_def", "pipeline", "machine"}

var frameworkToolNames = []string{
	"ask_user", "ask_user_form", "present_build_plan", "mark_step_in_progress", "mark_step_done",
	"mark_step_blocked", "revise_build_plan", "report_build_gaps", "load_tool", "find_tools",
	"skip_step", "keep_going", "stay_silent", "send_status", "read_output", "release_output",
	"background_work", "await_result", "delegate", "consult",
}

func init() {
	prompts.RegisterTunableTools(prompts.ToolGroupAuthoring, authoringToolNames...)
	prompts.RegisterTunableTools(prompts.ToolGroupAuthoring, authoringPrimitiveNames...)
	prompts.RegisterTunableTools(prompts.ToolGroupFramework, frameworkToolNames...)
}
