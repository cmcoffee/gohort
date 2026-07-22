// openapi_tool — the OpenAPI/Swagger importer, the tool-target analog of
// ComfyUI's workflow auto-wire: paste an API spec and Detect/BuildSpec turn its
// operations into a TOOLBOX (one action per endpoint). Params are derived from
// the path/query parameters; a requestBody becomes a `body` argument. Supports
// OpenAPI 3.x (servers[]) and Swagger 2.0 (host/basePath/schemes). See
// docs/templates.md. Governance is unchanged — the artifact is a toolbox TempTool
// that still goes through the tool approval flow.
package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

func init() {
	RegisterStrategy(TargetTool, "openapi_tool", Strategy{
		BuildSpec:  openapiBuildSpec,
		ReadValues: openapiReadValues,
		Detect:     openapiDetect,
	})
	RegisterTemplate(openapiTemplate())
}

// --- spec parsing ------------------------------------------------------------

type openAPIOp struct {
	Method      string
	Path        string
	OpID        string
	Summary     string
	PathParams  []string
	QueryParams []string // required query params
	HasBody     bool
}

var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// parseOpenAPI extracts the base URL + the operations from a spec (3.x or 2.0).
func parseOpenAPI(specJSON string) (string, []openAPIOp, error) {
	specJSON = strings.TrimSpace(specJSON)
	if specJSON == "" {
		return "", nil, fmt.Errorf("spec is required")
	}
	var doc struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Host     string                     `json:"host"`
		BasePath string                     `json:"basePath"`
		Schemes  []string                   `json:"schemes"`
		Paths    map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		return "", nil, fmt.Errorf("spec is not valid JSON: %w", err)
	}
	if len(doc.Paths) == 0 {
		return "", nil, fmt.Errorf("spec has no paths (is this an OpenAPI/Swagger document?)")
	}

	base := ""
	switch {
	case len(doc.Servers) > 0 && doc.Servers[0].URL != "":
		base = strings.TrimRight(doc.Servers[0].URL, "/")
	case doc.Host != "":
		scheme := "https"
		if len(doc.Schemes) > 0 {
			scheme = doc.Schemes[0]
		}
		base = scheme + "://" + doc.Host + strings.TrimRight(doc.BasePath, "/")
	}

	type param struct {
		Name     string `json:"name"`
		In       string `json:"in"`
		Required bool   `json:"required"`
	}
	var ops []openAPIOp
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		var pathObj map[string]json.RawMessage
		if json.Unmarshal(doc.Paths[path], &pathObj) != nil {
			continue
		}
		var shared []param
		if pp, ok := pathObj["parameters"]; ok {
			_ = json.Unmarshal(pp, &shared)
		}
		methods := make([]string, 0, len(pathObj))
		for m := range pathObj {
			if httpMethods[strings.ToLower(m)] {
				methods = append(methods, m)
			}
		}
		sort.Strings(methods)
		for _, method := range methods {
			var op struct {
				OperationID string          `json:"operationId"`
				Summary     string          `json:"summary"`
				Description string          `json:"description"`
				Parameters  []param         `json:"parameters"`
				RequestBody json.RawMessage `json:"requestBody"`
			}
			if json.Unmarshal(pathObj[method], &op) != nil {
				continue
			}
			o := openAPIOp{
				Method:  strings.ToUpper(method),
				Path:    path,
				OpID:    op.OperationID,
				Summary: firstNonBlank(op.Summary, op.Description),
				HasBody: len(op.RequestBody) > 0,
			}
			seen := map[string]bool{}
			for _, pr := range append(append([]param{}, shared...), op.Parameters...) {
				if pr.Name == "" || seen[pr.In+":"+pr.Name] {
					continue
				}
				seen[pr.In+":"+pr.Name] = true
				switch pr.In {
				case "path":
					o.PathParams = append(o.PathParams, pr.Name)
				case "query":
					if pr.Required {
						o.QueryParams = append(o.QueryParams, pr.Name)
					}
				case "body": // Swagger 2.0
					o.HasBody = true
				}
			}
			// Path params also come from the {tokens} literally in the path.
			for _, m := range toolPlaceholderRE.FindAllStringSubmatch(path, -1) {
				if !containsStr(o.PathParams, m[1]) {
					o.PathParams = append(o.PathParams, m[1])
				}
			}
			ops = append(ops, o)
		}
	}
	return base, ops, nil
}

var actionNameSanitizeRE = regexp.MustCompile(`[^a-z0-9]+`)

// actionForOp builds a toolbox action from one operation.
func actionForOp(base string, o openAPIOp) TempToolAction {
	name := sanitizeActionName(o.OpID)
	if name == "" {
		name = sanitizeActionName(strings.ToLower(o.Method) + "_" + o.Path)
	}
	url := base + o.Path
	params := map[string]ToolParam{}
	var required []string
	add := func(k, desc string) {
		if _, ok := params[k]; !ok {
			params[k] = ToolParam{Type: "string", Description: desc}
			required = append(required, k)
		}
	}
	for _, p := range o.PathParams {
		add(p, "path parameter")
	}
	if len(o.QueryParams) > 0 {
		var pairs []string
		for _, q := range o.QueryParams {
			add(q, "query parameter")
			pairs = append(pairs, q+"={"+q+"}")
		}
		url += "?" + strings.Join(pairs, "&")
	}
	body := ""
	if o.HasBody {
		add("body", "JSON request body")
		body = "{body}"
	}
	return TempToolAction{
		Name:         name,
		Description:  o.Summary,
		Method:       o.Method,
		URLTemplate:  url,
		BodyTemplate: body,
		Params:       params,
		Required:     required,
	}
}

func sanitizeActionName(s string) string {
	s = actionNameSanitizeRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_")
	return strings.Trim(s, "_")
}

func containsStr(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

// --- strategy ----------------------------------------------------------------

func openapiBuildSpec(t Template, vals map[string]any) (json.RawMessage, []string, error) {
	base, ops, err := parseOpenAPI(TemplateStr(vals, "spec"))
	if err != nil {
		return nil, nil, err
	}
	if b := TemplateStr(vals, "base_url"); b != "" {
		base = strings.TrimRight(b, "/")
	}
	if base == "" {
		return nil, nil, fmt.Errorf("no server URL in the spec — set base_url")
	}
	cred := TemplateStr(vals, "credential")
	if cred == "" {
		cred = "no_auth"
	}
	filter := TemplateStr(vals, "filter")

	var actions []TempToolAction
	names := map[string]bool{}
	for _, o := range ops {
		if filter != "" && !strings.HasPrefix(o.Path, filter) {
			continue
		}
		act := actionForOp(base, o)
		// De-dupe action names (distinct ops can sanitize to the same name).
		for names[act.Name] {
			act.Name += "_x"
		}
		names[act.Name] = true
		actions = append(actions, act)
	}
	if len(actions) == 0 {
		return nil, nil, fmt.Errorf("no operations matched (spec had %d; check the filter)", len(ops))
	}

	tt := TempTool{
		Description: firstNonBlank(TemplateStr(vals, "description"), "Imported from an OpenAPI spec."),
		Mode:        TempToolModeToolbox,
		Credential:  cred,
		Actions:     actions,
	}
	raw, err := json.Marshal(tt)
	return raw, []string{fmt.Sprintf("%d action(s) imported", len(actions))}, err
}

func openapiReadValues(_ Template, artifact json.RawMessage) map[string]any {
	var tt TempTool
	_ = json.Unmarshal(artifact, &tt)
	// The original spec isn't retained (the actions are the source of truth), so
	// Configure exposes only what's safely editable without a re-import.
	return map[string]any{
		"credential":  tt.Credential,
		"description": tt.Description,
	}
}

// openapiDetect previews what an import would produce: the base URL + a readable
// list of operations, filled into the (read-only-ish) preview fields.
func openapiDetect(_ Template, vals map[string]any) (map[string]any, []string, error) {
	base, ops, err := parseOpenAPI(TemplateStr(vals, "spec"))
	if err != nil {
		return nil, nil, err
	}
	lines := make([]string, 0, len(ops))
	for _, o := range ops {
		s := o.Method + " " + o.Path
		if o.Summary != "" {
			s += " — " + o.Summary
		}
		lines = append(lines, s)
	}
	return map[string]any{
		"base_url":   base,
		"operations": strings.Join(lines, "\n"),
	}, []string{fmt.Sprintf("%d operation(s) found", len(ops))}, nil
}

// --- declaration -------------------------------------------------------------

func openapiTemplate() Template {
	return Template{
		Name:        "openapi_tool",
		Label:       "Import from OpenAPI spec",
		Category:    "Tools",
		Description: "Paste an OpenAPI/Swagger spec — its endpoints become a toolbox (one action each). Params are derived from the path/query; Detect previews what you'll get.",
		Target:      TargetTool,
		Kind:        "toolbox",
		Strategy:    "openapi_tool",
		Fields: []TemplateField{
			{Key: "spec", Label: "OpenAPI / Swagger spec (JSON)", Type: "textarea", Group: "Spec", Help: "paste the full document, then click Detect"},
			{Key: "base_url", Label: "Base URL (optional override)", Type: "text", Group: "Connection", Help: "auto-filled from the spec's server; override if it's wrong"},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Auth", Help: "no_auth for a public API; a SecureAPI credential name for an authenticated one"},
			{Key: "filter", Label: "Path filter (optional)", Type: "text", Group: "Options", Help: "only import paths starting with this prefix, e.g. /pets"},
			{Key: "operations", Label: "Operations (preview)", Type: "textarea", Group: "Preview", Advanced: true, Help: "filled by Detect — the endpoints that will become actions"},
			{Key: "description", Label: "Toolbox description", Type: "text", Group: "Tool"},
		},
	}
}
