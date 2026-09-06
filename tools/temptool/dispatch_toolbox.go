package temptool

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// dispatchToolboxModeTempTool routes a toolbox-mode TempTool call to
// the named sub-action. The caller passes action="<name>" plus the
// action's own args; we look up the action, synthesize a single-
// endpoint api-mode TempTool that shares the parent's Credential,
// and reuse dispatchAPIModeTempTool. This keeps every api-mode
// concern (allow-list, audit log, response_pipe sandbox, etc.) in
// one place — toolbox is a packaging primitive, not a separate
// execution path.
func dispatchToolboxModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if len(tt.Actions) == 0 {
		return "", fmt.Errorf("toolbox tool %q has no actions defined", tt.Name)
	}
	actionName := strings.TrimSpace(StringArg(args, "action"))
	if actionName == "" {
		names := make([]string, 0, len(tt.Actions))
		for _, a := range tt.Actions {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("toolbox tool %q requires action=<name>. available: %v", tt.Name, names)
	}
	var act *TempToolAction
	for i := range tt.Actions {
		if tt.Actions[i].Name == actionName {
			act = &tt.Actions[i]
			break
		}
	}
	if act == nil {
		names := make([]string, 0, len(tt.Actions))
		for _, a := range tt.Actions {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("toolbox %q: no action named %q. available: %v", tt.Name, actionName, names)
	}
	if act.Disabled {
		return "", fmt.Errorf("toolbox %q action %q is disabled (quarantined). Fix it and re-enable via tool_def(action=\"update\", name=%q, actions=[{name:%q, disabled:false, ...}])", tt.Name, act.Name, tt.Name, act.Name)
	}
	// Drop the routing key so the synthetic api-mode tool doesn't see
	// it (action isn't one of its declared params, and the substitute
	// step would treat it as noise).
	inner := make(map[string]any, len(args))
	for k, v := range args {
		if k == "action" {
			continue
		}
		inner[k] = v
	}
	// Synthesize a single-endpoint api-mode tool. Carries the parent's
	// Credential + the action's URL/method/body/pipe + the action's
	// declared params + required list. Name encodes both layers so the
	// inner machinery's logs (rendered URL, response pipe failures)
	// stay attributable to the toolbox+action pair.
	// An action declares one template or the other. Both is not a richer action,
	// it is two actions wearing one name, and picking a winner silently would
	// make the toolbox do something its author did not write.
	hasURL := strings.TrimSpace(act.URLTemplate) != ""
	hasCmd := strings.TrimSpace(act.CommandTemplate) != ""
	switch {
	case hasURL && hasCmd:
		return "", fmt.Errorf("toolbox %q action %q declares both url_template and command_template — an action is an HTTP call or a local command, not both", tt.Name, act.Name)
	case !hasURL && !hasCmd:
		return "", fmt.Errorf("toolbox %q action %q declares neither url_template nor command_template, so there is nothing for it to run", tt.Name, act.Name)
	}

	// A SHELL action. Built as an ordinary shell-mode tool and handed to the
	// ordinary shell path, so it behaves at call time exactly as the same
	// command would as a standalone tool — same quoting, same sandbox, same
	// workspace. Anything else would be a second execution semantic living
	// inside the toolbox, which is where drift starts.
	if hasCmd {
		shellAct := TempTool{
			Name:            tt.Name + "." + act.Name,
			Description:     act.Description,
			Params:          act.Params,
			Required:        act.Required,
			Mode:            TempToolModeShell,
			CommandTemplate: act.CommandTemplate,
			// The parent's recipe and workspace posture travel with the action:
			// a toolbox that packages a binary has one deployment, not one per
			// verb, and an action that ran somewhere else would not find it.
			Recipe:         tt.Recipe,
			WorkspaceFiles: tt.WorkspaceFiles,
			StatePath:      tt.StatePath,
		}
		for _, r := range shellAct.Required {
			v, ok := lookupArgCI(inner, r)
			if !ok || v == nil {
				return "", fmt.Errorf("toolbox %q action %q: missing required arg %q", tt.Name, act.Name, r)
			}
			if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
				return "", fmt.Errorf("toolbox %q action %q: required arg %q is empty", tt.Name, act.Name, r)
			}
		}
		inner = canonicalizeArgKeys(inner, shellAct.Required, shellAct.Params)
		return dispatchTempToolUncached(sess, &shellAct, inner)
	}

	synthetic := TempTool{
		Name:            tt.Name + "." + act.Name,
		Description:     act.Description,
		Params:          act.Params,
		Required:        act.Required,
		Mode:            TempToolModeAPI,
		CommandTemplate: act.URLTemplate,
		Credential:      tt.Credential,
		Method:          act.Method,
		BodyTemplate:    act.BodyTemplate,
		ContentType:     act.ContentType,
		Headers:         act.Headers,
		ResponsePipe:    act.ResponsePipe,
		ResponseExtract: act.ResponseExtract,
	}
	// Required-arg check (mirrors the top-level dispatchTempTool guard
	// but scoped to the action's params, since the outer toolbox tool
	// only enforces "action" was provided).
	for _, r := range synthetic.Required {
		v, ok := lookupArgCI(inner, r)
		if !ok || v == nil {
			return "", fmt.Errorf("toolbox %q action %q: missing required arg %q", tt.Name, act.Name, r)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("toolbox %q action %q: required arg %q is empty", tt.Name, act.Name, r)
		}
	}
	inner = canonicalizeArgKeys(inner, synthetic.Required, synthetic.Params)
	return dispatchAPIModeTempTool(sess, &synthetic, inner)
}
