package admin

// The Templates section: recipes for integrating a service without Go
// (core/recipes), and the built-in forms that author one connector or tool,
// in one list. A template asks its questions, fills the answers in and
// imports its pieces as drafts; a built-in form opens the form it always did.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recipes"
)

// templateRow is one line of the Templates table.
type templateRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // "template" | "form"
	Title       string `json:"title"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	Contains    string `json:"contains,omitempty"`
	Source      string `json:"source"`
	// A built-in form's own name and target, for the form that opens it.
	Name   string `json:"name,omitempty"`
	Target string `json:"target,omitempty"`
	Recipe bool   `json:"_recipe,omitempty"`
	Form   bool   `json:"_form,omitempty"`
	Owned  bool   `json:"_imported,omitempty"`
}

func (a *AdminApp) registerRecipeRoutes(sub *http.ServeMux) {
	// GET: every template and built-in form, in one list.
	sub.HandleFunc("/api/templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		rows := []templateRow{}
		for _, s := range recipes.List(RootDB) {
			rows = append(rows, templateRow{ID: s.ID, Kind: "template", Title: s.Title, Category: s.Category,
				Description: s.Description, Contains: s.Contains, Source: s.Source,
				Recipe: true, Owned: s.Source == recipes.Imported})
		}
		var forms []templateRow
		for _, t := range AllTemplates() {
			forms = append(forms, templateRow{ID: "form:" + t.Target + "/" + t.Name, Kind: "form", Title: t.Label,
				Category: t.Category, Description: t.Description, Source: recipes.BuiltIn,
				Name: t.Name, Target: t.Target, Form: true})
		}
		sort.SliceStable(forms, func(i, j int) bool { return forms[i].Title < forms[j].Title })
		writeJSONOut(w, append(rows, forms...))
	})

	// GET ?id=: a template's questions and setup notes, for its Add form.
	sub.HandleFunc("/api/templates/recipe", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		rec, src, ok := recipes.Get(RootDB, strings.TrimSpace(r.URL.Query().Get("id")))
		if !ok {
			http.Error(w, "no such template", http.StatusNotFound)
			return
		}
		var contains []string
		for _, p := range rec.Bundle.Artifacts {
			contains = append(contains, p.Name+" ("+p.Type+")")
		}
		writeJSONOut(w, map[string]any{"id": rec.ID, "title": rec.Title, "description": rec.Description,
			"setup_notes": rec.SetupNotes, "questions": rec.Questions, "contains": contains, "source": src})
	})

	// POST ?id= {answers}: add a template.
	sub.HandleFunc("/api/templates/install", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Answers map[string]string `json:"answers"`
		}
		// Room for a pasted workflow or spec, not just short answers.
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := recipes.Install(RootDB, strings.TrimSpace(r.URL.Query().Get("id")), AuthCurrentUser(r), body.Answers)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONOut(w, struct {
			ArtifactImportResult
			Message string `json:"message"`
		}{res, res.Summary()})
	})

	// GET ?id=: a template as a file.
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
	sub.HandleFunc("/api/templates/import", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		data, err := readArtifactBundleBody(r)
		if err != nil {
			http.Error(w, "could not read the file", http.StatusBadRequest)
			return
		}
		rec, err := recipes.Import(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONOut(w, map[string]any{"ok": true, "message": fmt.Sprintf("Imported the template %q. Add it from the list when you are ready.", rec.Title)})
	})

	// POST ?id=: delete an imported template.
	sub.HandleFunc("/api/templates/delete", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if err := recipes.Delete(RootDB, strings.TrimSpace(r.URL.Query().Get("id"))); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONOut(w, map[string]any{"ok": true})
	})

	// GET: everything on this deployment that can go into a template, as
	// options for Save as template.
	sub.HandleFunc("/api/templates/pieces", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type opt struct {
			Value string `json:"value"`
			Label string `json:"label"`
			Group string `json:"group,omitempty"`
		}
		out := []opt{}
		if RootDB == nil {
			writeJSONOut(w, out)
			return
		}
		for _, s := range ArtifactSelectionForTypes(RootDB) {
			label := s.Name
			if s.Owner != "" {
				label += " (" + s.Owner + ")"
			}
			out = append(out, opt{Value: s.Type + "|" + s.Name + "|" + s.Owner, Label: label, Group: s.Type})
		}
		writeJSONOut(w, out)
	})

	// POST: save things already built as a template, turning chosen values
	// into questions.
	sub.HandleFunc("/api/templates/save", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
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
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
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
			Category: body.Category, SetupNotes: body.SetupNotes}, sels, qs, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONOut(w, map[string]any{"ok": true, "message": fmt.Sprintf("Saved the template %q with %d question(s). Export it to share it.", rec.Title, len(rec.Questions))})
	})
}

func writeJSONOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
