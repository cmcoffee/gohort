package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerTemplatesRoutes wires the templates API under the admin sub-mux.
func (a *AdminApp) registerTemplatesRoutes(sub *http.ServeMux) {
	// --- connector templates (the generic Add/Configure renderer) -----------
	//
	// One data-driven surface for every backend: a template declares its fields +
	// value↔spec mapping + optional Detect, and these endpoints render/save it. No
	// per-backend admin code (that's the point).

	// List templates for the Add menu (optional ?category=).
	sub.HandleFunc("/api/connector-templates", jsonapi.Get(a.db, a.handleConnectorTemplates, a.gate()))

	// Field schema for one template (Add mode — no values).
	sub.HandleFunc("/api/connector-template", jsonapi.Get(a.db, a.handleConnectorTemplate, a.gate()))

	// Auto-detect (Detect hook) — powers the panel's Detect button.
	sub.HandleFunc("/api/connector-detect", jsonapi.Post(a.db, a.handleConnectorDetect, a.gate()))

	// Configure an existing connector (GET: schema + current values) / Save
	// (POST: create or edit). Save MERGES onto the existing spec (forward-compat).
	sub.HandleFunc("/api/connector-config", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleConnectorConfig, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleConnectorConfigSave, a.gate()),
	}.Serve)

	// --- extensions (the umbrella catalog spanning every template target) ----
	//
	// One browse surface over AllTemplates(): connector templates and tool
	// templates side by side, each carrying its Target so the catalog facets on
	// it. Add still routes by target — the schema endpoint stamps `target`, and
	// the generic renderer POSTs to api/connector-config or api/tool-config
	// accordingly. Discovery is unified here; governance stays split.

	sub.HandleFunc("/api/extensions", jsonapi.Get(a.db, a.handleExtensionTemplates, a.gate()))

	// Field schema for one extension template (Add mode). Takes ?target= &name=
	// so a single client action can open the Add form for either target.
	sub.HandleFunc("/api/extension-template", jsonapi.Get(a.db, a.handleExtensionTemplate, a.gate()))

	// --- tool templates (same generic renderer, tool target) ----------------
	//
	// The tool artifact is a TempTool that routes through the tool governance
	// (AdminPersistTempTool), NOT SaveConnector — a template eases authoring, it
	// grants no new power.

	sub.HandleFunc("/api/tool-templates", jsonapi.Get(a.db, a.handleToolTemplates, a.gate()))

	sub.HandleFunc("/api/tool-template", jsonapi.Get(a.db, a.handleToolTemplate, a.gate()))

	sub.HandleFunc("/api/tool-detect", jsonapi.Post(a.db, a.handleToolDetect, a.gate()))

	// Tools from a template: POST creates (or, in edit mode, reconfigures) a
	// TempTool in an owner's pool; GET returns the prefilled schema to re-open an
	// installed tool in the generic form — the tool half of the connector
	// "Configure" round-trip, resolved through provenance (TemplateForTool).
	sub.HandleFunc("/api/tool-config", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleToolConfig, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleToolConfigSave, a.gate()),
	}.Serve)

}

// handleConnectorTemplates serves /api/connector-templates; registerTemplatesRoutes says what it is for.
func (a *AdminApp) handleConnectorTemplates(c *jsonapi.Call) (any, error) {
	r := c.R
	cat := strings.TrimSpace(r.URL.Query().Get("category"))
	type row struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Category    string `json:"category"`
		Description string `json:"description"`
	}
	var out []row
	for _, t := range ConnectorTemplates() {
		if cat != "" && t.Category != cat {
			continue
		}
		out = append(out, row{t.Name, t.Label, t.Category, t.Description})
	}
	return out, nil
}

// handleConnectorTemplate serves /api/connector-template; registerTemplatesRoutes says what it is for.
func (a *AdminApp) handleConnectorTemplate(c *jsonapi.Call) (any, error) {
	r := c.R
	t, ok := GetConnectorTemplate(r.URL.Query().Get("name"))
	if !ok {
		return nil, jsonapi.NotFound("no such template")
	}
	return templateSchemaMap(t, nil, "", true), nil
}

// templateValues is what a template's form posts: its values.
type templateValues struct {
	Values map[string]any `json:"values"`
}

// handleConnectorDetect fills the ?template= connector template's values in
// from what was pasted.
func (a *AdminApp) handleConnectorDetect(c *jsonapi.Call, req templateValues) (any, error) {
	t, ok := GetConnectorTemplate(c.R.URL.Query().Get("template"))
	if !ok || !t.HasDetect() {
		return nil, jsonapi.BadRequest("template has no detect")
	}
	vals, warns, err := t.Detect(req.Values)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"values": vals, "warnings": warns}, nil
}

// handleConnectorConfig is the ?connector= backend's edit form, filled from
// its spec.
func (a *AdminApp) handleConnectorConfig(c *jsonapi.Call) (any, error) {
	conn, ok := GetConnector(RootDB, strings.TrimSpace(c.R.URL.Query().Get("connector")))
	if !ok {
		return nil, jsonapi.NotFound("no such connector")
	}
	t, ok := TemplateForConnector(conn)
	if !ok {
		return nil, jsonapi.BadRequest("this connector has no config template")
	}
	return templateSchemaMap(t, t.ReadValues(conn.Spec), conn.Name, false), nil
}

// handleConnectorConfigSave builds a backend from a template: an edit of the
// one it names, or a new one, created and approved (the admin is the
// approver).
func (a *AdminApp) handleConnectorConfigSave(c *jsonapi.Call, req struct {
	Template   string         `json:"template"`
	Connector  string         `json:"connector"`
	Name       string         `json:"name"`
	Values     map[string]any `json:"values"`
	SetDefault bool           `json:"set_default"`
}) (any, error) {
	t, ok := GetConnectorTemplate(req.Template)
	if !ok {
		return nil, jsonapi.BadRequest("no such template")
	}
	raw, _, berr := t.BuildSpec(req.Values)
	if berr != nil {
		return nil, jsonapi.BadRequest(berr.Error())
	}
	if req.Connector != "" {
		// Edit: merge onto the existing spec so unknown fields survive.
		conn, ok := GetConnector(RootDB, req.Connector)
		if !ok {
			return nil, jsonapi.NotFound("no such connector")
		}
		conn.Spec = MergeSpec(conn.Spec, raw)
		if err := SaveConnector(RootDB, conn); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		adminChange(c.R, activity.Settings, "configured backend %q via template %q", req.Connector, req.Template)
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, jsonapi.BadRequest("name is required")
	}
	if _, exists := GetConnector(RootDB, name); exists {
		return nil, jsonapi.BadRequest("a connector named " + name + " already exists")
	}
	conn := Connector{Name: name, Kind: t.Kind, Template: t.Name, Owner: c.User, Desc: t.Label + " backend", Spec: raw}
	if err := SaveConnector(RootDB, conn); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := ApproveConnector(RootDB, name); err != nil {
		return nil, jsonapi.BadRequest("created but approve failed: " + err.Error())
	}
	if req.SetDefault && a.db != nil && t.Category == "Image generation" {
		a.db.Set(ImageTable, "provider", name)
	}
	adminChange(c.R, activity.Settings, "added backend %q via template %q", name, req.Template)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleExtensionTemplates serves /api/extensions; registerTemplatesRoutes says what it is for.
func (a *AdminApp) handleExtensionTemplates(c *jsonapi.Call) (any, error) {
	r := c.R
	cat := strings.TrimSpace(r.URL.Query().Get("category"))
	type row struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Target      string `json:"target"`
		Category    string `json:"category"`
		Description string `json:"description"`
	}
	var out []row
	for _, t := range AllTemplates() {
		if cat != "" && t.Category != cat {
			continue
		}
		out = append(out, row{t.Name, t.Label, t.Target, t.Category, t.Description})
	}
	return out, nil
}

// handleExtensionTemplate serves /api/extension-template; registerTemplatesRoutes says what it is for.
func (a *AdminApp) handleExtensionTemplate(c *jsonapi.Call) (any, error) {
	r := c.R
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		target = TargetConnector
	}
	t, ok := GetTemplate(target, r.URL.Query().Get("name"))
	if !ok {
		return nil, jsonapi.NotFound("no such template")
	}
	return templateSchemaMap(t, nil, "", true), nil
}

// handleToolTemplates serves /api/tool-templates; registerTemplatesRoutes says what it is for.
func (a *AdminApp) handleToolTemplates(c *jsonapi.Call) (any, error) {
	type row struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Category    string `json:"category"`
		Description string `json:"description"`
	}
	var out []row
	for _, t := range Templates(TargetTool) {
		out = append(out, row{t.Name, t.Label, t.Category, t.Description})
	}
	return out, nil
}

// handleToolTemplate serves /api/tool-template; registerTemplatesRoutes says what it is for.
func (a *AdminApp) handleToolTemplate(c *jsonapi.Call) (any, error) {
	r := c.R
	t, ok := GetTemplate(TargetTool, r.URL.Query().Get("name"))
	if !ok {
		return nil, jsonapi.NotFound("no such tool template")
	}
	return templateSchemaMap(t, nil, "", true), nil
}

// handleToolDetect fills the ?template= tool template's values in from what
// was pasted.
func (a *AdminApp) handleToolDetect(c *jsonapi.Call, req templateValues) (any, error) {
	t, ok := GetTemplate(TargetTool, c.R.URL.Query().Get("template"))
	if !ok || !t.HasDetect() {
		return nil, jsonapi.BadRequest("template has no detect")
	}
	vals, warns, err := t.Detect(req.Values)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"values": vals, "warnings": warns}, nil
}

// handleToolConfig is the ?tool= tool's edit form, filled from the tool.
func (a *AdminApp) handleToolConfig(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	toolName := strings.TrimSpace(q.Get("tool"))
	owner := strings.TrimSpace(q.Get("owner"))
	if owner == "" {
		owner = c.User
	}
	var tt TempTool
	found := false
	for _, p := range LoadPersistentTempTools(a.db, owner) {
		if p.Tool.Name == toolName {
			tt, found = p.Tool, true
			break
		}
	}
	if !found {
		return nil, jsonapi.NotFound("no such tool")
	}
	t, ok := TemplateForTool(tt)
	if !ok {
		return nil, jsonapi.BadRequest("this tool has no config template")
	}
	raw, _ := json.Marshal(tt)
	m := templateSchemaMap(t, t.ReadValues(raw), tt.Name, false)
	m["owner"] = owner // echoed back on save so the edit targets the right pool
	return m, nil
}

// handleToolConfigSave builds a tool from a template: a new one, or an edit
// of the one the generic form names in its identity slot.
func (a *AdminApp) handleToolConfigSave(c *jsonapi.Call, req struct {
	Template  string         `json:"template"`
	Name      string         `json:"name"`
	Connector string         `json:"connector"` // edit: the existing tool id, carried in the generic identity slot
	Owner     string         `json:"owner"`
	Values    map[string]any `json:"values"`
}) (any, error) {
	t, ok := GetTemplate(TargetTool, req.Template)
	if !ok {
		return nil, jsonapi.BadRequest("no such tool template")
	}
	// Create carries a Name; the generic form's edit path leaves Name blank
	// and carries the existing tool id in the identity slot instead.
	editing := false
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Connector)
		editing = name != ""
	}
	if name == "" {
		return nil, jsonapi.BadRequest("name is required")
	}
	raw, _, berr := t.BuildSpec(req.Values)
	if berr != nil {
		return nil, jsonapi.BadRequest(berr.Error())
	}
	var tt TempTool
	if err := json.Unmarshal(raw, &tt); err != nil {
		return nil, jsonapi.BadRequest("template produced an invalid tool")
	}
	tt.Name = name
	tt.Template = req.Template // provenance
	owner := strings.TrimSpace(req.Owner)
	if owner == "" {
		owner = c.User
	}
	// Edit preserves the tool's share + adopt-ACL state; create mints a
	// fresh pool entry.
	persist := AdminPersistTempTool
	verb := "added"
	if editing {
		persist = AdminReconfigureTempTool
		verb = "reconfigured"
	}
	if err := persist(a.db, owner, tt); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[admin] user %q %s tool %q for %q via template %q", c.User, verb, name, owner, req.Template)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// writeTestResult is the shared response shape for connectivity-test
// endpoints wired into FormPanel.TestURL buttons. Always responds 200
// — the ok flag carries pass/fail so the client can render success or
// error inline next to the Test button without HTTP-status branching.
// templateSchemaMap is the payload the generic renderer consumes for both Add
// (values nil, create=true) and Configure (values from ReadValues, create=false).
func templateSchemaMap(t Template, vals map[string]any, connector string, create bool) map[string]any {
	target := t.Target
	if target == "" {
		target = TargetConnector
	}
	return map[string]any{
		"template":  t.Name,
		"label":     t.Label,
		"category":  t.Category,
		"target":    target,
		"fields":    t.Fields,
		"detect":    t.HasDetect(),
		"values":    vals,
		"connector": connector,
		"create":    create,
	}
}
