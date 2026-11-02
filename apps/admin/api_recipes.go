package admin

// The Templates section: recipes for integrating a service without Go
// (core/recipes), and the built-in forms that author one connector or tool,
// in one list. A template asks its questions, fills the answers in and
// imports its pieces as drafts; a built-in form opens the form it always did.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/ui"
)

// templateRow is one line of the Templates table.
type templateRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // "template"
	Title       string `json:"title"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	Contains    string `json:"contains,omitempty"`
	Source      string `json:"source"`
	Recipe      bool   `json:"_recipe,omitempty"`
	Owned       bool   `json:"_imported,omitempty"`
}

func (a *AdminApp) registerRecipeRoutes(sub *http.ServeMux) {
	// GET: every template: those that ship with gohort, and those imported
	// or saved here, by category.
	sub.HandleFunc("/api/templates", jsonapi.Get(a.db, a.handleTemplates, a.gate()))
	// GET ?need=: the templates "+ Add another…" offers at the end of a
	// page's list of connectors (templateNeeds).
	sub.HandleFunc("/api/templates/for", jsonapi.Get(a.db, a.handleTemplatesFor, a.gate()))

	// GET ?id=: a template's questions and setup notes, for its Add form.
	sub.HandleFunc("/api/templates/recipe", jsonapi.Get(a.db, a.handleTemplateRecipe, a.gate()))

	// POST ?id= {answers}: add a template. Room for a pasted workflow or spec,
	// not just short answers.
	sub.HandleFunc("/api/templates/install", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleTemplateInstall, a.gate(), jsonapi.MaxBody(4<<20)),
	}.Serve)

	// GET ?id=: a template as a file. A download, so it stays a plain handler.
	sub.HandleFunc("/api/templates/export", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		data, err := recipes.Export(RootDB, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gohort-template-%s.json"`, id))
		w.Write(data)
	})

	// POST (the file's JSON, or {"pack": "<json>"}): import a template file.
	sub.HandleFunc("/api/templates/import", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleTemplateImport, a.gate(), jsonapi.MaxBody(artifactBundleMaxBody)),
	}.Serve)

	// POST ?id=: delete an imported template.
	sub.HandleFunc("/api/templates/delete", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleTemplateDelete, a.gate()),
	}.Serve)

	// GET: everything on this deployment that can go into a template, as
	// options for Save as template.
	sub.HandleFunc("/api/templates/pieces", jsonapi.Get(a.db, a.handleTemplatePieces, a.gate()))

	// POST: save things already built as a template, turning chosen values
	// into questions.
	sub.HandleFunc("/api/templates/save", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleTemplateSave, a.gate()),
	}.Serve)
}

// handleTemplates is every template, each set up from its row: those that
// ship with gohort and those imported or saved here.
func (a *AdminApp) handleTemplates(c *jsonapi.Call) (any, error) {
	rows := []templateRow{}
	for _, s := range recipes.List(RootDB) {
		rows = append(rows, templateRow{ID: s.ID, Kind: "template", Title: s.Title, Category: s.Category,
			Description: s.Description, Contains: s.Contains, Source: s.Source,
			Recipe: true, Owned: s.Source == recipes.Imported})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Category < rows[j].Category })
	return rows, nil
}

// templateNeed is what one page's list of connectors takes: the template
// category its templates are filed under, and the job their connector must
// do ("" for any).
type templateNeed struct{ category, capability string }

// templateNeeds are the lists "+ Add another…" ends, by the need its option
// names: what each offers to add.
var templateNeeds = map[string]templateNeed{
	"web.search":        {"Web Search", "web.search"},
	"research":          {"Research Sources", "web.search"},
	"image.generate":    {"Images", ""},
	"embed.text":        {"Embeddings", "embed.text"},
	"speech.transcribe": {"Speech", "speech.transcribe"},
	"speech.synthesize": {"Speech", "speech.synthesize"},
	"page.render":       {"Page rendering", "page.render"},
	"mail.send":         {"Mail", "mail.send"},
	"file.convert":      {"File conversion", "file.convert"},
}

// handleTemplatesFor is the templates for the ?need= list: those filed
// under its category whose connector does its job. A connector whose spec
// is made from the answers (a ComfyUI workflow) says nothing of its job
// until then, and is offered on its category.
func (a *AdminApp) handleTemplatesFor(c *jsonapi.Call) (any, error) {
	need, ok := templateNeeds[strings.TrimSpace(c.R.URL.Query().Get("need"))]
	if !ok {
		return nil, jsonapi.BadRequest("no list of connectors by that need")
	}
	rows := []templateRow{}
	for _, s := range recipes.List(RootDB) {
		if s.Category != need.category {
			continue
		}
		if gives, known := templateGives(s.ID); need.capability != "" && known && !slices.Contains(gives, need.capability) {
			continue
		}
		rows = append(rows, templateRow{ID: s.ID, Kind: "template", Title: s.Title, Category: s.Category,
			Description: s.Description, Source: s.Source, Recipe: true})
	}
	return rows, nil
}

// addAnother is the "+ Add another…" option that ends a page's list of
// connectors: it opens the templates for need (templateNeeds), and what is
// added from one joins the list (connector_add_another).
func addAnother(need string) ui.SelectOption {
	return ui.SelectOption{Value: need, Label: "+ Add another…", Action: "connector_add_another"}
}

// templateGives are the jobs the connectors template id makes do, and
// whether its specs say: one made from the answers does not until then.
func templateGives(id string) ([]string, bool) {
	rec, _, ok := recipes.Get(RootDB, id)
	if !ok {
		return nil, false
	}
	var gives []string
	known := false
	for _, art := range rec.Bundle.Artifacts {
		if art.Type != "connector" {
			continue
		}
		var r struct {
			Spec json.RawMessage `json:"spec"`
		}
		var spec struct {
			Provides []struct {
				Capability string `json:"capability"`
			} `json:"provides"`
		}
		if json.Unmarshal(art.Recipe, &r) != nil || json.Unmarshal(r.Spec, &spec) != nil {
			continue
		}
		known = true
		for _, b := range spec.Provides {
			gives = append(gives, b.Capability)
		}
	}
	return gives, known
}

// handleTemplateRecipe is one template's questions, setup notes, pieces, and
// where each credential it installs sends its secret.
func (a *AdminApp) handleTemplateRecipe(c *jsonapi.Call) (any, error) {
	rec, src, ok := recipes.Get(RootDB, strings.TrimSpace(c.R.URL.Query().Get("id")))
	if !ok {
		return nil, jsonapi.NotFound("no such template")
	}
	var contains []string
	// Where each credential the template installs sends its secret: the
	// base URL (and token URL) the template fixed. A secret answer is
	// asked for here, and the credential lands disabled, so this is the
	// one place its destination can be seen before the key is typed in.
	dest := map[string]string{}
	for _, p := range rec.Bundle.Artifacts {
		contains = append(contains, p.Name+" ("+p.Type+")")
		if p.Type != "credential" {
			continue
		}
		var c struct {
			BaseURL, AllowedURLPattern, TokenURL string
		}
		var raw map[string]any
		if json.Unmarshal(p.Recipe, &raw) == nil {
			c.BaseURL, _ = raw["base_url"].(string)
			c.AllowedURLPattern, _ = raw["allowed_url_pattern"].(string)
			c.TokenURL, _ = raw["token_url"].(string)
		}
		d := firstNonBlankStr(c.BaseURL, c.AllowedURLPattern)
		if c.TokenURL != "" {
			d += " (tokens from " + c.TokenURL + ")"
		}
		dest[p.Name] = d
	}
	return map[string]any{"id": rec.ID, "title": rec.Title, "description": rec.Description,
		"setup_notes": rec.SetupNotes, "questions": rec.Questions, "contains": contains, "source": src,
		"destinations": dest}, nil
}

// handleTemplateInstall adds the ?id= template with the answers given.
func (a *AdminApp) handleTemplateInstall(c *jsonapi.Call, body struct {
	Answers map[string]string `json:"answers"`
}) (any, error) {
	res, err := recipes.Install(RootDB, strings.TrimSpace(c.R.URL.Query().Get("id")), c.User, body.Answers)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return struct {
		ArtifactImportResult
		Message string `json:"message"`
	}{res, res.Summary()}, nil
}

// handleTemplateImport stores a template file.
func (a *AdminApp) handleTemplateImport(c *jsonapi.Call, body json.RawMessage) (any, error) {
	rec, err := recipes.Import(RootDB, artifactBundleBytes(body), c.User)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true, "message": fmt.Sprintf("Imported the template %q. Add it from the list when you are ready.", rec.Title)}, nil
}

// handleTemplateDelete deletes the ?id= imported template.
func (a *AdminApp) handleTemplateDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	if err := recipes.Delete(RootDB, strings.TrimSpace(c.R.URL.Query().Get("id"))); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true}, nil
}

// handleTemplatePieces is everything here that can go into a template.
func (a *AdminApp) handleTemplatePieces(c *jsonapi.Call) (any, error) {
	type opt struct {
		Value string `json:"value"`
		Label string `json:"label"`
		Group string `json:"group,omitempty"`
	}
	out := []opt{}
	if RootDB == nil {
		return out, nil
	}
	for _, s := range ArtifactSelectionForTypes(RootDB) {
		label := s.Name
		if s.Owner != "" {
			label += " (" + s.Owner + ")"
		}
		out = append(out, opt{Value: s.Type + "|" + s.Name + "|" + s.Owner, Label: label, Group: s.Type})
	}
	return out, nil
}

// templateSave is the Save as template form.
type templateSave struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	SetupNotes  string   `json:"setup_notes"`
	Pieces      []string `json:"pieces"`
	Questions   []struct {
		Name       string `json:"name"`
		Label      string `json:"label"`
		Value      string `json:"value"`
		Help       string `json:"help"`
		Kind       string `json:"kind"`
		Secret     string `json:"secret"`
		Credential string `json:"credential"`
		Required   string `json:"required"`
	} `json:"questions"`
}

// handleTemplateSave saves pieces already built as a template.
func (a *AdminApp) handleTemplateSave(c *jsonapi.Call, body templateSave) (any, error) {
	var sels []ArtifactSel
	for _, p := range body.Pieces {
		parts := strings.SplitN(p, "|", 3)
		if len(parts) < 2 {
			continue
		}
		s := ArtifactSel{Type: parts[0], Name: parts[1]}
		if len(parts) == 3 {
			s.Owner = parts[2]
		}
		sels = append(sels, s)
	}
	var qs []recipes.SaveQuestion
	for _, q := range body.Questions {
		if strings.TrimSpace(q.Name) == "" {
			continue
		}
		label := strings.TrimSpace(q.Label)
		if label == "" {
			label = strings.TrimSpace(q.Name)
		}
		qs = append(qs, recipes.SaveQuestion{Question: recipes.Question{
			Name: strings.TrimSpace(q.Name), Label: label, Help: q.Help, Kind: q.Kind,
			Secret: q.Secret == "yes", Credential: strings.TrimSpace(q.Credential), Required: q.Required == "yes",
		}, Value: q.Value})
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = slugID(body.Title)
	}
	rec, err := recipes.Save(RootDB, recipes.Recipe{ID: id, Title: strings.TrimSpace(body.Title), Description: body.Description,
		Category: body.Category, SetupNotes: body.SetupNotes}, sels, qs, c.User)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true, "message": fmt.Sprintf("Saved the template %q with %d question(s). Export it to share it.", rec.Title, len(rec.Questions))}, nil
}

func firstNonBlankStr(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// slugID makes a template id from its title.
func slugID(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
