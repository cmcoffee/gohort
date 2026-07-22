package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// tool_template — the Builder's front door to TOOL TEMPLATES. Instead of
// hand-crafting a tool_def, the LLM lists ready-made templates (a REST call, a
// GitHub issue, …), fills their options, and the framework builds + attaches the
// tool. It's the tool-target analog of the connector tool, and it's governed
// exactly like add_tool: the built TempTool attaches to the focused agent and is
// verifiable — a template eases authoring, it grants no new power.
func toolTemplateTool() ChatTool {
	gt := NewGroupedTool("tool_template",
		"Author a tool from a ready-made TEMPLATE (e.g. a REST API call, or a service action like creating a GitHub issue) instead of hand-writing a tool_def. Call action=\"list\" to see the templates and the options each needs, then action=\"create\" to build one onto the focused agent.")
	gt.SetSingleFirePerBatch(true)

	gt.AddAction("list", &GroupedToolAction{
		Description: "List the available tool templates and the options each one needs, so you know what to pass to create.",
		Handler:     toolTemplateList,
	})
	gt.AddAction("create", &GroupedToolAction{
		Description: "Build a tool from a template and attach it to the focused agent (or the one named in `agent`). Fill `values` with the template's option keys (from list). Pass test_args to verify it against the real endpoint in the same call.",
		Params: map[string]ToolParam{
			"name":        {Type: "string", Description: "The tool's name (snake_case), how the model will call it."},
			"template":    {Type: "string", Description: "Which template to use — a name from tool_template(action=\"list\")."},
			"values":      {Type: "object", Description: "The template's options as {key: value}, e.g. {\"url\":\"https://api.x/v1/{id}\",\"credential\":\"x_api\"}. Keys come from the template's fields (see list)."},
			"agent":       {Type: "string", Description: "(optional) Target agent name/id; omit to use the agent in authoring focus."},
			"description": {Type: "string", Description: "(optional) Override the tool's description."},
			"test_args":   {Type: "object", Description: "(optional) Args to dispatch the new tool once for verification, e.g. {\"id\":\"123\"}."},
		},
		Required: []string{"name", "template"},
		Handler:  toolTemplateCreate,
	})
	return gt
}

func toolTemplateList(args map[string]any, sess *ToolSession) (string, error) {
	type field struct {
		Key, Label, Type, Group, Help string
		Advanced                      bool `json:",omitempty"`
	}
	type tpl struct {
		Name, Label, Description string
		Fields                   []field
	}
	var out []tpl
	for _, t := range Templates(TargetTool) {
		ft := tpl{Name: t.Name, Label: t.Label, Description: t.Description}
		for _, f := range t.Fields {
			ft.Fields = append(ft.Fields, field{f.Key, f.Label, f.Type, f.Group, f.Help, f.Advanced})
		}
		out = append(out, ft)
	}
	if len(out) == 0 {
		return "No tool templates are registered.", nil
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

func toolTemplateCreate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	tplName := strings.TrimSpace(stringArg(args, "template"))
	tpl, ok := GetTemplate(TargetTool, tplName)
	if !ok {
		var names []string
		for _, t := range Templates(TargetTool) {
			names = append(names, t.Name)
		}
		return "", fmt.Errorf("no tool template %q — available: %s (call tool_template(action=\"list\"))", tplName, strings.Join(names, ", "))
	}

	// Resolve the target agent — same rules as add_tool.
	target, err := resolveToolTargetAgent(args, sess)
	if err != nil {
		return "", err
	}

	// Build the tool from the template's strategy.
	vals, _ := args["values"].(map[string]any)
	raw, warns, berr := tpl.BuildSpec(vals)
	if berr != nil {
		return "", fmt.Errorf("building tool from template %q: %w", tplName, berr)
	}
	var tt TempTool
	if err := json.Unmarshal(raw, &tt); err != nil {
		return "", fmt.Errorf("template produced an invalid tool: %w", err)
	}
	tt.Name = name
	tt.Template = tplName // provenance
	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		tt.Description = d
	}
	if tt.Description == "" {
		tt.Description = tpl.Description
	}

	// Attach to the focused agent + persist — mirrors add_tool's tail.
	replaced := false
	for i, existing := range target.Tools {
		if existing.Name == tt.Name {
			target.Tools[i] = tt
			replaced = true
			break
		}
	}
	if !replaced {
		target.Tools = append(target.Tools, tt)
	}
	if _, err := saveAgent(sess.DB, target); err != nil {
		return "", fmt.Errorf("save agent: %w", err)
	}
	if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, tt); err != nil {
		Log("[orchestrate.tool_template] session draft save failed for %q: %v", tt.Name, err)
	}
	if sess.Username != "" {
		DequeuePendingTempTool(sess.DB, sess.Username, tt.Name)
	}

	verb := "added"
	if replaced {
		verb = "replaced"
	}
	msg := fmt.Sprintf("Tool %q %s on agent %q from template %q.", tt.Name, verb, target.Name, tplName)
	if len(warns) > 0 {
		msg += " Notes: " + strings.Join(warns, "; ")
	}

	// Verification: dispatch once with test_args, fold in the result.
	if testArgs := testArgsFromArgs(args, "test_args"); len(testArgs) > 0 {
		copyTT := tt
		res, derr := temptool.DispatchTempToolDirect(sess, &copyTT, testArgs)
		if derr != nil {
			return msg + fmt.Sprintf(" Verification FAILED: %v. Re-call create with corrected values.", derr), nil
		}
		trimmed := res
		if len(trimmed) > 1200 {
			trimmed = trimmed[:1200] + "…"
		}
		return msg + "\nVerification succeeded:\n\n" + trimmed, nil
	}
	return msg + " No test_args given — call create again with the same fields plus test_args to verify it against the real endpoint.", nil
}

// resolveToolTargetAgent mirrors add_tool's target resolution: an explicit
// `agent` arg, else the agent in authoring focus. Refuses app agents + read-only
// seeds.
func resolveToolTargetAgent(args map[string]any, sess *ToolSession) (AgentRecord, error) {
	var target AgentRecord
	if key := strings.TrimSpace(stringArg(args, "agent")); key != "" {
		found, ok := findAgentByNameOrID(sess.DB, sess.Username, key)
		if !ok {
			return target, fmt.Errorf("no agent named or id'd %q in your fleet — call agents(action=\"list\")", key)
		}
		target = found
	} else {
		focusedID := loadAuthoringInProgress(sess.DB, sess.ChatSessionID)
		if focusedID == "" {
			return target, fmt.Errorf("no agent in authoring focus and no `agent` argument — pass agent=\"<name or id>\", or get_agent/create_agent first")
		}
		found, ok := loadAgent(sess.DB, focusedID)
		if !ok {
			return target, fmt.Errorf("focused agent is gone from storage — re-call get_agent on a valid agent, or pass agent=\"<name or id>\"")
		}
		target = found
	}
	if isAppAgent(target.ID) {
		return target, fmt.Errorf("%q is an app agent — its tools are declared in code and can't be authored into it", target.Name)
	}
	if target.Owner != sess.Username {
		return target, fmt.Errorf("agent %q is a read-only seed — clone_agent first, then continue", target.Name)
	}
	return target, nil
}
