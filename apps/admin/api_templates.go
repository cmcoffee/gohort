package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerTemplatesRoutes wires the templates API under the admin sub-mux.
func (a *AdminApp) registerTemplatesRoutes(sub *http.ServeMux) {
	// --- connector templates (the generic Add/Configure renderer) -----------
	//
	// One data-driven surface for every backend: a template declares its fields +
	// value↔spec mapping + optional Detect, and these endpoints render/save it. No
	// per-backend admin code (that's the point).

	// List templates for the Add menu (optional ?category=).
	sub.HandleFunc("/api/connector-templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Field schema for one template (Add mode — no values).
	sub.HandleFunc("/api/connector-template", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetConnectorTemplate(r.URL.Query().Get("name"))
		if !ok {
			http.Error(w, "no such template", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(templateSchemaMap(t, nil, "", true))
	})

	// Auto-detect (Detect hook) — powers the panel's Detect button.
	sub.HandleFunc("/api/connector-detect", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetConnectorTemplate(r.URL.Query().Get("template"))
		if !ok || !t.HasDetect() {
			http.Error(w, "template has no detect", http.StatusBadRequest)
			return
		}
		var req struct {
			Values map[string]any `json:"values"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vals, warns, err := t.Detect(req.Values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"values": vals, "warnings": warns})
	})

	// Configure an existing connector (GET: schema + current values) / Save
	// (POST: create or edit). Save MERGES onto the existing spec (forward-compat).
	sub.HandleFunc("/api/connector-config", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			c, ok := GetConnector(RootDB, strings.TrimSpace(r.URL.Query().Get("connector")))
			if !ok {
				http.Error(w, "no such connector", http.StatusNotFound)
				return
			}
			t, ok := TemplateForConnector(c)
			if !ok {
				http.Error(w, "this connector has no config template", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(templateSchemaMap(t, t.ReadValues(c.Spec), c.Name, false))
		case http.MethodPost:
			var req struct {
				Template   string         `json:"template"`
				Connector  string         `json:"connector"`
				Name       string         `json:"name"`
				Values     map[string]any `json:"values"`
				SetDefault bool           `json:"set_default"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			t, ok := GetConnectorTemplate(req.Template)
			if !ok {
				http.Error(w, "no such template", http.StatusBadRequest)
				return
			}
			raw, _, berr := t.BuildSpec(req.Values)
			if berr != nil {
				http.Error(w, berr.Error(), http.StatusBadRequest)
				return
			}
			if req.Connector != "" {
				// Edit: merge onto the existing spec so unknown fields survive.
				c, ok := GetConnector(RootDB, req.Connector)
				if !ok {
					http.Error(w, "no such connector", http.StatusNotFound)
					return
				}
				c.Spec = MergeSpec(c.Spec, raw)
				if err := SaveConnector(RootDB, c); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				Log("[admin] user %q configured backend %q via template %q", AuthCurrentUser(r), req.Connector, req.Template)
			} else {
				// Create + approve (admin is the approver).
				name := strings.TrimSpace(req.Name)
				if name == "" {
					http.Error(w, "name is required", http.StatusBadRequest)
					return
				}
				if _, exists := GetConnector(RootDB, name); exists {
					http.Error(w, "a connector named "+name+" already exists", http.StatusBadRequest)
					return
				}
				c := Connector{Name: name, Kind: t.Kind, Template: t.Name, Owner: AuthCurrentUser(r), Desc: t.Label + " backend", Spec: raw}
				if err := SaveConnector(RootDB, c); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if err := ApproveConnector(RootDB, name); err != nil {
					http.Error(w, "created but approve failed: "+err.Error(), http.StatusBadRequest)
					return
				}
				if req.SetDefault && a.db != nil && t.Category == "Image generation" {
					a.db.Set(ImageTable, "provider", name)
				}
				Log("[admin] user %q added backend %q via template %q", AuthCurrentUser(r), name, req.Template)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// --- extensions (the umbrella catalog spanning every template target) ----
	//
	// One browse surface over AllTemplates(): connector templates and tool
	// templates side by side, each carrying its Target so the catalog facets on
	// it. Add still routes by target — the schema endpoint stamps `target`, and
	// the generic renderer POSTs to api/connector-config or api/tool-config
	// accordingly. Discovery is unified here; governance stays split.

	sub.HandleFunc("/api/extensions", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Field schema for one extension template (Add mode). Takes ?target= &name=
	// so a single client action can open the Add form for either target.
	sub.HandleFunc("/api/extension-template", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		target := strings.TrimSpace(r.URL.Query().Get("target"))
		if target == "" {
			target = TargetConnector
		}
		t, ok := GetTemplate(target, r.URL.Query().Get("name"))
		if !ok {
			http.Error(w, "no such template", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(templateSchemaMap(t, nil, "", true))
	})

	// All templates (connector + tool) for the Templates catalog browse. Each row's
	// id is "<target>/<name>" so the two namespaces don't collide.
	sub.HandleFunc("/api/all-templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type row struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Label       string `json:"label"`
			Category    string `json:"category"`
			Target      string `json:"target"`
			Description string `json:"description"`
		}
		var out []row
		for _, target := range []string{TargetConnector, TargetTool} {
			for _, t := range Templates(target) {
				out = append(out, row{target + "/" + t.Name, t.Name, t.Label, t.Category, target, t.Description})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// --- tool templates (same generic renderer, tool target) ----------------
	//
	// The tool artifact is a TempTool that routes through the tool governance
	// (AdminPersistTempTool), NOT SaveConnector — a template eases authoring, it
	// grants no new power.

	sub.HandleFunc("/api/tool-templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	sub.HandleFunc("/api/tool-template", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetTemplate(TargetTool, r.URL.Query().Get("name"))
		if !ok {
			http.Error(w, "no such tool template", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(templateSchemaMap(t, nil, "", true))
	})

	sub.HandleFunc("/api/tool-detect", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetTemplate(TargetTool, r.URL.Query().Get("template"))
		if !ok || !t.HasDetect() {
			http.Error(w, "template has no detect", http.StatusBadRequest)
			return
		}
		var req struct {
			Values map[string]any `json:"values"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vals, warns, err := t.Detect(req.Values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"values": vals, "warnings": warns})
	})

	// Tools from a template: POST creates (or, in edit mode, reconfigures) a
	// TempTool in an owner's pool; GET returns the prefilled schema to re-open an
	// installed tool in the generic form — the tool half of the connector
	// "Configure" round-trip, resolved through provenance (TemplateForTool).
	sub.HandleFunc("/api/tool-config", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			toolName := strings.TrimSpace(r.URL.Query().Get("tool"))
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			if owner == "" {
				owner = AuthCurrentUser(r)
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
				http.Error(w, "no such tool", http.StatusNotFound)
				return
			}
			t, ok := TemplateForTool(tt)
			if !ok {
				http.Error(w, "this tool has no config template", http.StatusBadRequest)
				return
			}
			raw, _ := json.Marshal(tt)
			m := templateSchemaMap(t, t.ReadValues(raw), tt.Name, false)
			m["owner"] = owner // echoed back on save so the edit targets the right pool
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(m)
		case http.MethodPost:
			var req struct {
				Template  string         `json:"template"`
				Name      string         `json:"name"`
				Connector string         `json:"connector"` // edit: the existing tool id, carried in the generic identity slot
				Owner     string         `json:"owner"`
				Values    map[string]any `json:"values"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			t, ok := GetTemplate(TargetTool, req.Template)
			if !ok {
				http.Error(w, "no such tool template", http.StatusBadRequest)
				return
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
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			raw, _, berr := t.BuildSpec(req.Values)
			if berr != nil {
				http.Error(w, berr.Error(), http.StatusBadRequest)
				return
			}
			var tt TempTool
			if err := json.Unmarshal(raw, &tt); err != nil {
				http.Error(w, "template produced an invalid tool", http.StatusBadRequest)
				return
			}
			tt.Name = name
			tt.Template = req.Template // provenance
			owner := strings.TrimSpace(req.Owner)
			if owner == "" {
				owner = AuthCurrentUser(r)
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
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			Log("[admin] user %q %s tool %q for %q via template %q", AuthCurrentUser(r), verb, name, owner, req.Template)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

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
