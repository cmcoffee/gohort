// Tool templates — the second template TARGET. Same declaration+strategy model
// as connectors, but a strategy emits a TempTool (an api-mode tool definition)
// instead of a connector Spec. rest_tool is the generic strategy: a declaration
// gives a URL template + method + credential, and the tool's params are DERIVED
// from the {placeholders} in the URL/body — so "add a REST tool" is one
// declaration. A later openapi_tool strategy will add a Detect that generates
// actions from a spec (the killer feature). See docs/templates.md.
//
// Governance is unchanged: a tool built from a template is a TempTool that still
// runs through credential binding + the tool approval flow — templates ease
// authoring, they grant no new power.
package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

func init() {
	RegisterStrategy(TargetTool, "rest_tool", Strategy{
		BuildSpec:  restToolBuildSpec,
		ReadValues: restToolReadValues,
	})
	RegisterTemplate(restGetTemplate())
	RegisterTemplate(githubIssueTemplate())
}

// toolPlaceholderRE matches {arg_name} placeholders in a URL/body template.
var toolPlaceholderRE = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// restToolBuildSpec builds an api-mode TempTool from the declaration + values.
// URL/method/body come from the form when present, else the declaration's Params
// (so a fixed-shape tool like github_issue bakes them and asks only for a
// credential). Params are derived from the {placeholders}. Name is left blank —
// the save endpoint stamps it, exactly as the connector path does.
func restToolBuildSpec(t Template, vals map[string]any) (json.RawMessage, []string, error) {
	url := firstNonBlank(TemplateStr(vals, "url"), t.Params["url"])
	if url == "" {
		return nil, nil, fmt.Errorf("url is required")
	}
	method := strings.ToUpper(firstNonBlank(TemplateStr(vals, "method"), t.Params["method"], "GET"))
	cred := TemplateStr(vals, "credential")
	if cred == "" {
		cred = "no_auth"
	}
	body := firstNonBlank(TemplateStr(vals, "body"), t.Params["body"])
	if base := TemplateStr(vals, "base_url"); base != "" {
		url = strings.ReplaceAll(url, "{base_url}", strings.TrimRight(base, "/"))
	}

	// Derive the tool's params from the {placeholders} in the URL + body.
	params := map[string]ToolParam{}
	var required []string
	for _, m := range toolPlaceholderRE.FindAllStringSubmatch(url+" "+body, -1) {
		p := m[1]
		if p == "base_url" {
			continue
		}
		if _, seen := params[p]; !seen {
			params[p] = ToolParam{Type: "string", Description: "value for {" + p + "}"}
			required = append(required, p)
		}
	}

	tt := TempTool{
		Description:     TemplateStr(vals, "description"),
		Mode:            TempToolModeAPI,
		CommandTemplate: url,
		Method:          method,
		Credential:      cred,
		BodyTemplate:    body,
		ResponsePipe:    TemplateStr(vals, "response_pipe"),
		Params:          params,
		Required:        required,
	}
	raw, err := json.Marshal(tt)
	return raw, nil, err
}

// restToolReadValues prefills the Configure panel from a stored TempTool. Only the
// template's declared Fields are rendered, so a fixed-shape tool shows just what
// it exposes (e.g. the credential).
func restToolReadValues(_ Template, artifact json.RawMessage) map[string]any {
	var tt TempTool
	_ = json.Unmarshal(artifact, &tt)
	return map[string]any{
		"description":   tt.Description,
		"url":           tt.CommandTemplate,
		"method":        tt.Method,
		"credential":    tt.Credential,
		"body":          tt.BodyTemplate,
		"response_pipe": tt.ResponsePipe,
	}
}

// ToolTemplateRef is the provenance handle for a tool: Target is always "tool";
// Name is its stored TempTool.Template.
func ToolTemplateRef(tt TempTool) TemplateRef {
	return TemplateRef{Target: TargetTool, Name: strings.TrimSpace(tt.Template)}
}

// TemplateForTool resolves which template authored a tool, from its provenance.
// Unlike TemplateForConnector there's no shape-inference fallback: a tool either
// carries its template name or was hand-authored. ok=false covers both the
// hand-authored case and a template this build doesn't ship.
func TemplateForTool(tt TempTool) (Template, bool) {
	return ToolTemplateRef(tt).Resolve()
}

// firstNonBlank returns the first non-empty (trimmed) string.
func firstNonBlank(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- declarations (pure data) -------------------------------------------------

// restGetTemplate — a generic authenticated REST call the user fully specifies.
func restGetTemplate() Template {
	return Template{
		Name:        "rest_call",
		Label:       "REST API call",
		Category:    "Tools",
		Description: "A generic authenticated HTTP call. Put {placeholders} in the URL/body — each becomes a tool argument.",
		Target:      TargetTool,
		Kind:        "api",
		Strategy:    "rest_tool",
		Fields: []TemplateField{
			{Key: "description", Label: "What the tool does", Type: "text", Group: "Tool", Help: "shown to the model when it decides to call this"},
			{Key: "url", Label: "URL", Type: "text", Group: "Request", Help: "e.g. https://api.example.com/v1/items/{id} — {id} becomes an argument"},
			{Key: "method", Label: "Method", Type: "select", Group: "Request", Options: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
			{Key: "body", Label: "Body template (optional)", Type: "textarea", Group: "Request", Help: "JSON with {placeholders}, for POST/PUT/PATCH"},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Auth", Help: "no_auth for a public API; a SecureAPI credential name for an authenticated one"},
			{Key: "response_pipe", Label: "Response filter (optional)", Type: "text", Group: "Advanced", Advanced: true, Help: "a jq/awk/sed command to reshape the response before the model sees it, e.g. \"jq '.items'\""},
		},
	}
}

// githubIssueTemplate — a fixed-shape tool: URL/method/body baked in Params, the
// user supplies only a GitHub token credential. Args (owner/repo/title/body) are
// derived from the placeholders.
func githubIssueTemplate() Template {
	return Template{
		Name:        "github_issue",
		Label:       "GitHub: create issue",
		Category:    "Tools",
		Description: "Open an issue on a GitHub repo. Needs a SecureAPI credential holding a GitHub token.",
		Target:      TargetTool,
		Kind:        "api",
		Strategy:    "rest_tool",
		Params: map[string]string{
			"url":    "https://api.github.com/repos/{owner}/{repo}/issues",
			"method": "POST",
			"body":   `{"title":"{title}","body":"{issue_body}"}`,
		},
		Fields: []TemplateField{
			{Key: "description", Label: "What the tool does", Type: "text", Group: "Tool", Default: "Create a GitHub issue on a repo."},
			{Key: "credential", Label: "GitHub credential", Type: "credential", Group: "Auth", Help: "a SecureAPI bearer credential holding a GitHub token (repo scope)"},
		},
	}
}
