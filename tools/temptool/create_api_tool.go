package temptool

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ----------------------------------------------------------------------
// create_api_tool
// ----------------------------------------------------------------------

// CreateAPIToolTool wraps a registered secure-API credential into a
// focused, structured tool. Sister of create_temp_tool but the body is
// a URL template against a credential rather than a shell command —
// the LLM never sees the credential value.
type CreateAPIToolTool struct{}

func (t *CreateAPIToolTool) Name() string { return "create_api_tool" }

func (t *CreateAPIToolTool) Caps() []Capability { return []Capability{CapNetwork} }

func (t *CreateAPIToolTool) NeedsConfirm() bool { return true }

func (t *CreateAPIToolTool) Desc() string {
	return "Define a focused tool that calls a registered API credential. The body is a URL template (with {param} placeholders) targeting a specific credential — the credential's auth is injected server-side, you never see the secret. Use when you've discovered a useful endpoint pattern via call_<credname> and want a structured, reusable shape (e.g. get_github_issue(owner, repo, number) wrapping /repos/{owner}/{repo}/issues/{number}). Set persist=true to queue the tool for human approval and reuse across sessions."
}

func (t *CreateAPIToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {
			Type:        "string",
			Description: "Tool name (snake_case, must not match an existing tool). E.g. \"get_github_issue\".",
		},
		"description": {
			Type:        "string",
			Description: "What the tool does. Include enough detail that you'll know when to call it in future rounds.",
		},
		"credential": {
			Type:        "string",
			Description: "Name of the registered secure-API credential to use (e.g. \"github_api\"). The credential's allowed-URL pattern is enforced — your URL template must resolve to a URL that matches.",
		},
		"url_template": {
			Type:        "string",
			Description: "URL template with {param} placeholders. Placeholders are URL-path-encoded at call time. Example: \"https://api.github.com/repos/{owner}/{repo}/issues/{number}\".",
		},
		"method": {
			Type:        "string",
			Description: "HTTP method. Defaults to GET. Use POST/PUT/PATCH/DELETE for write operations.",
		},
		"body_template": {
			Type:        "string",
			Description: "Optional JSON body template with {param} placeholders. Placeholders are JSON-encoded at call time — strings get wrapped in quotes automatically, numbers/booleans pass through, objects/arrays serialize structurally. DO NOT wrap placeholders in quotation marks yourself: write {prompt} not \"{prompt}\". The substitution layer handles the quoting and any escaping (newlines, embedded quotes) of the runtime value, so a long multi-line system prompt or any unsafe-for-JSON string passed as an arg comes out correctly. Put long literal content (system prompts, instructions) as runtime PARAMS, not baked into the template — that way you don't have to escape it inside the template string itself. Example: '{\"system_prompt\": {prompt}, \"user_message\": {msg}}'. Leave empty for GET requests.",
		},
		"params": {
			Type:        "object",
			Description: "Object describing the tool's parameters. Same shape as create_temp_tool. Each key matches a {placeholder} in url_template or body_template. OPTIONAL — omit for a no-param endpoint (a GET with no query string); don't invent a dummy placeholder.",
		},
		"required": {
			Type:        "array",
			Description: "Optional list of param names that must be provided. Defaults to all of them.",
		},
		"response_pipe": {
			Type:        "string",
			Description: "Optional shell command (sh -c) that receives the API response BODY on stdin and emits the LLM-visible result on stdout. The HTTP status line is stripped before piping and re-prepended to the output, so the pipe should target only the response body (no `tail -n +2` needed). Pipe is skipped on non-2xx responses so the LLM sees the raw error. Use to pre-filter noisy responses before they reach the LLM context — e.g. \"jq -c '[.items[] | {id, name, status}]'\" or \"jq -c '.[:20]'\". Runs in a tight sandbox (no network, no filesystem, /tmp tmpfs only) so it can use jq, awk, sed, grep, head, tr, etc. Leave empty if the LLM should see the raw response. Adds an exec dependency to the tool — sessions without execute capability won't be able to dispatch it.",
		},
		"persist": {
			Type:        "boolean",
			Description: "If true, request that this tool be saved across future sessions. Same approval flow as create_temp_tool — the tool works in this session immediately but persists only after human review.",
		},
	}
}

func (t *CreateAPIToolTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("create_api_tool requires a session")
}

func (t *CreateAPIToolTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("create_api_tool requires a session")
	}
	if sess.DB == nil {
		return "", fmt.Errorf("create_api_tool requires a session with DB access")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !validToolName(name) {
		return "", fmt.Errorf("name must be lowercase letters / digits / underscores only (got %q)", name)
	}
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == name {
			return "", fmt.Errorf("name %q collides with a registered tool", name)
		}
	}
	if err := CheckCatalogNameCollision(sess, name, nil); err != nil {
		return "", err
	}
	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", fmt.Errorf("description is required")
	}
	credName := strings.TrimSpace(StringArg(args, "credential"))
	if credName == "" {
		// Default to the always-bootstrapped no_auth credential when
		// the LLM omitted credential — public APIs are the common case
		// and forcing the author to specify credential="no_auth"
		// explicitly was friction without security benefit (the
		// no_auth credential is the deployment's open-by-default
		// pattern anyway). Admin tighten via no_auth's
		// AllowedURLPattern if scope-limiting matters.
		credName = "no_auth"
	}
	// Owner-aware: resolve in the AUTHOR's namespace so a user-owned credential
	// (their own "API credentials", e.g. a Builder-drafted key) is found, not
	// just global/admin ones. Runtime dispatch already resolves owner-aware, so
	// this keeps create-time validation consistent with it.
	cr, ok := Secure().Resolve(credName, sess.Username)
	if !ok {
		return "", fmt.Errorf("credential %q is not registered. Register it in Extensions > API credentials (or Admin > APIs for a shared one), then enable it", credName)
	}
	// A secured credential auto-binds to any tool that declares it (api-mode
	// dispatches server-side; secret never exposed) — no approval step, access
	// follows the tool's scope. Exception: an admin's explicit REVOKE is a durable
	// deny. See docs/secured-credential-tool-binding.md.
	if cr.Secured {
		if Secure().ToolBindingRevoked(credName, name) {
			return "", fmt.Errorf("credential %q is SECURED and tool %q's binding was REVOKED by an admin — ask them to restore it in Admin > APIs", credName, name)
		}
		_ = Secure().ApproveToolBinding(credName, name)
	}
	urlTpl := strings.TrimSpace(StringArg(args, "url_template"))
	if urlTpl == "" {
		return "", fmt.Errorf("url_template is required")
	}
	method := strings.ToUpper(strings.TrimSpace(StringArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	bodyTpl := strings.TrimSpace(StringArg(args, "body_template"))
	respPipe := strings.TrimSpace(StringArg(args, "response_pipe"))

	params, err := parseParamsArg(args["params"])
	if err != nil {
		return "", fmt.Errorf("params: %w", err)
	}
	if err := validateTemplate(urlTpl, params); err != nil {
		return "", fmt.Errorf("url_template: %w", err)
	}
	if bodyTpl != "" {
		if err := validateTemplate(bodyTpl, params); err != nil {
			return "", fmt.Errorf("body_template: %w", err)
		}
	}

	required := stringSliceArg(args["required"])
	// Omitted required → default all params required; an EXPLICIT [] → make
	// all optional. Distinguish by presence (see the toolbox path).
	if raw, present := args["required"]; !present || raw == nil {
		for k := range params {
			required = append(required, k)
		}
	} else {
		for _, r := range required {
			if _, ok := params[r]; !ok {
				return "", fmt.Errorf("required lists %q which is not in params", r)
			}
		}
	}
	// Hard authoring gate (mirrors the toolbox path): a write action whose
	// required param appears in neither the url_template nor the body_template
	// sends it nowhere → a live 400 the author can't diagnose. Reject here.
	// A PATH placeholder can't be optional — substitution has nothing to put
	// there, so the call dies at dispatch instead of at authoring time.
	if missing := pathPlaceholderParams(urlTpl, required); len(missing) > 0 {
		return "", fmt.Errorf(pathPlaceholderMsg, missing, missing[0], missing[0])
	}
	if unsent := unsentWriteParams(method, urlTpl, bodyTpl, required); len(unsent) > 0 {
		return "", fmt.Errorf("required param(s) %v are sent NOWHERE — this %s tool references them in neither url_template nor body_template, so the API never receives them (the cause of a 400 like \"content must be a string\"). Add a body_template that carries them, e.g. body_template: {\"content\": {content}}", unsent, method)
	}

	tool := &TempTool{
		Name:            name,
		Description:     desc,
		Params:          params,
		Required:        required,
		CommandTemplate: urlTpl,
		Mode:            TempToolModeAPI,
		Credential:      credName,
		Method:          method,
		BodyTemplate:    bodyTpl,
		ContentType:     strings.TrimSpace(StringArg(args, "content_type")),
		Headers:         stringMapArg(args, "headers"),
		ResponsePipe:    respPipe,
		ResponseExtract: ParseExtractSpec(args["response_extract"]),
		Category:        strings.TrimSpace(StringArg(args, "category")),
	}
	// Allow in-session overwrite — see CreateTempToolTool for rationale.
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	// Durable home for the unapproved tool — see persistUnapprovedTool.
	saveSessionScoped := func() { persistUnapprovedTool(sess, tool) }

	spec := formatTempToolSpec(tool)

	// Persistence is admin-driven now — silently ignore the `persist`
	// flag if the LLM still sends it. Tools always land in the
	// session-scoped pool; the admin promotes via the Tools modal.
	_ = BoolArg(args, "persist")
	saveSessionScoped()
	return fmt.Sprintf("Created api tool %q (wraps credential %q). It is now in your tool catalog.%s", name, credName, spec), nil
}
