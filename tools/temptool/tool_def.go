// tool_def — grouped management tool consolidating list_temp_tools,
// create_temp_tool, create_api_tool, and delete_temp_tool into one
// catalog entry with action="<list|create|delete|help>".
//
// Brief catalog description points the LLM at action="help" for the
// full usage spec. Reduces prompt budget consumed by 4 separate tool
// descriptions every round.

package temptool

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	gt := NewGroupedTool("tool_def",
		"Manage runtime-defined tools — runtime-shaped wrappers around shell commands or registered API credentials. Use to list what's defined, create a new one, delete one you no longer need.")

	gt.AddAction("list", &GroupedToolAction{
		Description:  "List all session-scoped + persistent tools currently available to you. Returns name, mode (shell|api), and a one-line description for each.",
		Params:       map[string]ToolParam{},
		Required:     nil,
		Caps:         []Capability{CapExecute, CapNetwork},
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return listGrouped(args, sess)
		},
	})

	gt.AddAction("create", &GroupedToolAction{
		Description:  "Define a new runtime tool. Required: name, description, mode, params, plus mode-specific fields. mode=\"shell\" needs command_template (with {param} placeholders, shell-quoted at dispatch). mode=\"api\" needs credential, url_template, method, optional body_template (placeholders are URL-encoded for url_template, JSON-encoded for body_template). persist=true queues the tool for human approval and reuse across future sessions.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Tool name (snake_case, must not match an existing tool)."},
			"description":      {Type: "string", Description: "What the tool does. Shown to you in the catalog."},
			"mode":             {Type: "string", Description: "\"shell\" for sandboxed shell command, \"api\" for HTTP call against a registered credential."},
			"params":           {Type: "object", Description: "Object describing the tool's parameters. Each key is a param name, value is {type, description}."},
			"command_template": {Type: "string", Description: "(shell mode) Shell command with {param} placeholders, shell-quoted at dispatch. {workspace_dir} resolves to a workspace path. Use action=\"help\" for the workspace-binding flow when wrapping scripts."},
			"credential":       {Type: "string", Description: "(api mode) Name of the registered secure-API credential to dispatch through."},
			"url_template":     {Type: "string", Description: "(api mode) URL template with {param} placeholders, URL-encoded at dispatch."},
			"method":           {Type: "string", Description: "(api mode) HTTP method. Default GET."},
			"body_template":    {Type: "string", Description: "(api mode) JSON body template with {param} placeholders (JSON-encoded at dispatch). Optional for GET; usually required for POST/PUT/PATCH."},
			"required":         {Type: "array", Description: "Optional list of param names that must be provided by callers. Defaults to all params."},
			"persist":          {Type: "boolean", Description: "If true, request that this tool be saved across future sessions (queues for admin approval). Default false (session-only)."},
			"state_path":       {Type: "string", Description: "Optional. Relative subdirectory inside the workspace whose contents persist between invocations. Use ONLY for tools that legitimately need runtime state (counters, accumulating logs, lookup DBs) — most tools don't and should leave this unset. Example: state_path=\"state\" with command_template=\"python3 {workspace_dir}/run.py --db {workspace_dir}/state/log.db\"."},
		},
		Required:     []string{"name", "description", "mode", "params"},
		Caps:         []Capability{CapExecute, CapNetwork},
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return createGrouped(args, sess)
		},
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description:  "Remove a tool by name. Removes from this session AND from your persistent pool if applicable.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Name of the tool to remove."},
		},
		Required:     []string{"name"},
		Caps:         []Capability{CapExecute, CapNetwork},
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return deleteGrouped(args, sess)
		},
	})

	RegisterChatTool(gt)
}

// listGrouped reuses the existing ListTempToolsTool logic via a
// shim. We can't call its RunWithSession directly without an instance,
// so reproduce the formatting inline.
func listGrouped(args map[string]any, sess *ToolSession) (string, error) {
	persistentByName := map[string]bool{}
	pendingByName := map[string]bool{}
	if sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			persistentByName[p.Tool.Name] = true
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			pendingByName[p.Tool.Name] = true
		}
	}
	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 {
		return "No temp tools defined in this session.", nil
	}
	var b strings.Builder
	for i, t := range tools {
		var tag string
		switch {
		case persistentByName[t.Name]:
			tag = " [persistent]"
		case pendingByName[t.Name]:
			tag = " [pending approval]"
		default:
			tag = " [session-only]"
		}
		fmt.Fprintf(&b, "%d. %s%s [%s] — %s\n", i+1, t.Name, tag, modeLabel(t.Mode), t.Description)
	}
	return b.String(), nil
}

func modeLabel(mode string) string {
	if mode == TempToolModeAPI {
		return "api"
	}
	return "shell"
}

// createGrouped dispatches between create_temp_tool (shell) and
// create_api_tool (api) based on the mode arg.
func createGrouped(args map[string]any, sess *ToolSession) (string, error) {
	mode := strings.TrimSpace(StringArg(args, "mode"))
	switch mode {
	case "", TempToolModeShell:
		// Shell mode — call the existing CreateTempToolTool path by
		// reconstructing its expected args.
		shellArgs := map[string]any{
			"name":             args["name"],
			"description":      args["description"],
			"params":           args["params"],
			"command_template": args["command_template"],
		}
		if r, ok := args["required"]; ok {
			shellArgs["required"] = r
		}
		if p, ok := args["persist"]; ok {
			shellArgs["persist"] = p
		}
		if v, ok := args["state_path"]; ok {
			shellArgs["state_path"] = v
		}
		t := &CreateTempToolTool{}
		return t.RunWithSession(shellArgs, sess)
	case TempToolModeAPI:
		apiArgs := map[string]any{
			"name":         args["name"],
			"description":  args["description"],
			"params":       args["params"],
			"credential":   args["credential"],
			"url_template": args["url_template"],
		}
		if v, ok := args["method"]; ok {
			apiArgs["method"] = v
		}
		if v, ok := args["body_template"]; ok {
			apiArgs["body_template"] = v
		}
		if v, ok := args["required"]; ok {
			apiArgs["required"] = v
		}
		if v, ok := args["persist"]; ok {
			apiArgs["persist"] = v
		}
		t := &CreateAPIToolTool{}
		return t.RunWithSession(apiArgs, sess)
	default:
		return "", fmt.Errorf("mode must be \"shell\" or \"api\" (got %q)", mode)
	}
}

func deleteGrouped(args map[string]any, sess *ToolSession) (string, error) {
	t := &DeleteTempToolTool{}
	return t.RunWithSession(args, sess)
}

// suppress unused import — json is used by future expansions; keep
// the import handy.
var _ = json.Marshal
