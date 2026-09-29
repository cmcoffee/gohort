// Package recipes holds templates: recipes for integrating a service into
// gohort without writing Go.
//
// A template is a file: a title and setup notes, the QUESTIONS it asks when it
// is added (your site's address, your email, an API token), and a bundle of
// the pieces it installs (an API credential, tools, a skill, an agent) in the
// ordinary gohort.bundle/v1 shape, with {{question}} marking where an answer
// goes. Adding one fills the answers in and runs the same importer a bundle
// file does, so everything lands as a draft for review: credentials disabled,
// tools pending, connectors unapproved.
//
// A secret answer never goes into a recipe or a piece: it is written straight
// into the named credential's secret store after the import. A template with a
// secret-shaped value written into it is refused, on import and on save.
//
// Built-in templates ship embedded (builtin/*.json). Others are imported from a
// file or saved from things already built ("Save as template"), which turns
// chosen values in them into questions. Built-ins cannot be changed or
// deleted; imported ones can.
package recipes

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core"
)

//go:embed builtin/*.json
var builtinFiles embed.FS

// storeTable holds the templates imported or saved on this deployment.
const storeTable = "recipes"

// Sources of a template.
const (
	BuiltIn  = "built-in"
	Imported = "imported"
)

// Question is one thing a template asks when it is added.
type Question struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Default  string   `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"` // a pick-one question
	// Kind "url" checks the answer is an https address and drops a trailing
	// slash, so {{site}}/rest/... joins cleanly; "http_url" allows http too,
	// for a server on the local network; "long" asks in a multi-line box.
	Kind string `json:"kind,omitempty"`
	// Helper hands the answer to a registered helper (see Helper), whose
	// outputs the pieces use as {{question.output}}. With are the helper's
	// other inputs, which may use {{answer}} placeholders.
	Helper string            `json:"helper,omitempty"`
	With   map[string]string `json:"with,omitempty"`
	// Secret answers are never substituted into the pieces: they are written
	// into Credential's secret store after the import.
	Secret     bool   `json:"secret,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Recipe is one template.
type Recipe struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Category    string         `json:"category,omitempty"`
	SetupNotes  string         `json:"setup_notes,omitempty"`
	Questions   []Question     `json:"questions,omitempty"`
	Bundle      core.ArtifactBundle `json:"bundle"`
	// Set on the stored copy of an imported or saved template.
	By    string    `json:"by,omitempty"`
	Added time.Time `json:"added,omitempty"`
}

// Summary is the browse view of a template.
type Summary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Source      string `json:"source"`
	Contains    string `json:"contains"`
	Questions   int    `json:"questions"`
}

var (
	idRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	nameRe        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	placeholderRe = regexp.MustCompile(`\{\{([a-z][a-z0-9_]*)(?:\.([a-z][a-z0-9_]*))?\}\}`)
	wholeRe       = regexp.MustCompile(`^\{\{([a-z][a-z0-9_]*)(?:\.([a-z][a-z0-9_]*))?\}\}$`)
)

// Validate refuses a template that could not install cleanly or would carry
// a secret: a bad id or question name, a placeholder no question answers, a
// secret question used as a placeholder or not tied to a credential in the
// bundle, a bundle that does not parse, or a secret-shaped value written in.
func Validate(r Recipe) error {
	if !idRe.MatchString(r.ID) {
		return fmt.Errorf("the id must be lowercase letters, digits and dashes, not %q", r.ID)
	}
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("the template has no title")
	}
	if len(r.Bundle.Artifacts) == 0 {
		return fmt.Errorf("the template installs nothing")
	}
	creds := map[string]bool{}
	for _, a := range r.Bundle.Artifacts {
		if a.Type == "credential" {
			creds[a.Name] = true
		}
	}
	asked, secret := map[string]bool{}, map[string]bool{}
	for _, q := range r.Questions {
		if !nameRe.MatchString(q.Name) {
			return fmt.Errorf("question name %q must be lowercase letters, digits and underscores", q.Name)
		}
		if asked[q.Name] {
			return fmt.Errorf("question %q is asked twice", q.Name)
		}
		asked[q.Name] = true
		if strings.TrimSpace(q.Label) == "" {
			return fmt.Errorf("question %q has no label", q.Name)
		}
		if q.Secret {
			secret[q.Name] = true
			if !creds[q.Credential] {
				return fmt.Errorf("secret question %q must name a credential the template installs", q.Name)
			}
		}
		switch q.Kind {
		case "", "url", "http_url", "long":
		default:
			return fmt.Errorf("question %q has kind %q; the kinds are url, http_url and long", q.Name, q.Kind)
		}
		if q.Helper != "" {
			if q.Secret {
				return fmt.Errorf("secret question %q cannot go to a helper", q.Name)
			}
			if _, ok := LookupHelper(q.Helper); !ok {
				return fmt.Errorf("question %q names helper %q, which this gohort does not have", q.Name, q.Helper)
			}
		}
	}
	helperOf := map[string]string{}
	for _, q := range r.Questions {
		if q.Helper != "" {
			helperOf[q.Name] = q.Helper
		}
	}
	data, err := json.Marshal(r.Bundle)
	if err != nil {
		return fmt.Errorf("the bundle does not encode: %v", err)
	}
	for _, m := range placeholderRe.FindAllStringSubmatch(string(data), -1) {
		switch {
		case secret[m[1]]:
			return fmt.Errorf("{{%s}} is a secret answer: it goes into its credential's secret store, never into a piece", m[1])
		case !asked[m[1]]:
			return fmt.Errorf("{{%s}} is used but no question asks for it", m[1])
		case m[2] != "":
			h, ok := LookupHelper(helperOf[m[1]])
			if !ok || !h.hasOutput(m[2]) {
				return fmt.Errorf("{{%s.%s}}: question %q has no helper output %q", m[1], m[2], m[1], m[2])
			}
		}
	}
	var hit string
	walkStrings(r.Bundle, func(s string) string {
		if hit == "" && core.ContainsLikelySecret(placeholderRe.ReplaceAllString(s, "{x}")) {
			hit = s
		}
		return s
	})
	if hit != "" {
		return fmt.Errorf("a piece carries what looks like a secret written into it (%q): route it through a credential and ask for it as a secret question", clip(hit, 60))
	}
	check := r.Bundle
	if check.Bundle == "" {
		check.Bundle = core.ArtifactBundleFormat
	}
	b, _ := json.Marshal(check)
	if _, err := core.ParseArtifactBundle(b); err != nil {
		return fmt.Errorf("the bundle does not parse: %v", err)
	}
	return nil
}

// walkStrings rewrites every string inside every piece's recipe.
func walkStrings(b core.ArtifactBundle, fn func(string) string) core.ArtifactBundle {
	out := b
	out.Artifacts = make([]core.PortableArtifact, len(b.Artifacts))
	for i, a := range b.Artifacts {
		out.Artifacts[i] = a
		var v any
		if json.Unmarshal(a.Recipe, &v) != nil {
			continue
		}
		v = walk(v, fn)
		if raw, err := json.Marshal(v); err == nil {
			out.Artifacts[i].Recipe = raw
		}
		out.Artifacts[i].Name = fn(a.Name)
	}
	return out
}

// walkAny is walkStrings where a string may become any JSON value.
func walkAny(b core.ArtifactBundle, fn func(string) any) core.ArtifactBundle {
	out := b
	out.Artifacts = make([]core.PortableArtifact, len(b.Artifacts))
	var conv func(v any) any
	conv = func(v any) any {
		switch t := v.(type) {
		case string:
			return fn(t)
		case []any:
			for i := range t {
				t[i] = conv(t[i])
			}
			return t
		case map[string]any:
			for k, x := range t {
				t[k] = conv(x)
			}
			return t
		}
		return v
	}
	for i, a := range b.Artifacts {
		out.Artifacts[i] = a
		var v any
		if json.Unmarshal(a.Recipe, &v) != nil {
			continue
		}
		if raw, err := json.Marshal(conv(v)); err == nil {
			out.Artifacts[i].Recipe = raw
		}
		if name, ok := fn(a.Name).(string); ok {
			out.Artifacts[i].Name = name
		}
	}
	return out
}

func walk(v any, fn func(string) string) any {
	switch t := v.(type) {
	case string:
		return fn(t)
	case []any:
		for i := range t {
			t[i] = walk(t[i], fn)
		}
		return t
	case map[string]any:
		for k, x := range t {
			t[k] = walk(x, fn)
		}
		return t
	}
	return v
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// builtins parses the embedded templates; a malformed one is skipped and
// logged, never fatal.
func builtins() []Recipe {
	entries, err := builtinFiles.ReadDir("builtin")
	if err != nil {
		return nil
	}
	var out []Recipe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := builtinFiles.ReadFile("builtin/" + e.Name())
		if err != nil {
			continue
		}
		var r Recipe
		if err := json.Unmarshal(data, &r); err != nil {
			core.Log("[recipes] built-in %s does not parse: %v", e.Name(), err)
			continue
		}
		if err := Validate(r); err != nil {
			core.Log("[recipes] built-in %s is not valid: %v", e.Name(), err)
			continue
		}
		out = append(out, r)
	}
	return out
}

func isBuiltIn(id string) bool {
	for _, r := range builtins() {
		if r.ID == id {
			return true
		}
	}
	return false
}

// Get returns one template, built-in or stored.
func Get(db core.Database, id string) (Recipe, string, bool) {
	for _, r := range builtins() {
		if r.ID == id {
			return r, BuiltIn, true
		}
	}
	if db == nil {
		return Recipe{}, "", false
	}
	var r Recipe
	if db.Get(storeTable, id, &r) && r.ID != "" {
		return r, Imported, true
	}
	return Recipe{}, "", false
}

// List is every template, built-in first, each group by category and title.
func List(db core.Database) []Summary {
	type entry struct {
		r   Recipe
		src string
	}
	var all []entry
	for _, r := range builtins() {
		all = append(all, entry{r, BuiltIn})
	}
	if db != nil {
		for _, k := range db.Keys(storeTable) {
			var r Recipe
			if db.Get(storeTable, k, &r) && r.ID != "" {
				all = append(all, entry{r, Imported})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].src != all[j].src {
			return all[i].src == BuiltIn
		}
		if all[i].r.Category != all[j].r.Category {
			return all[i].r.Category < all[j].r.Category
		}
		return all[i].r.Title < all[j].r.Title
	})
	out := make([]Summary, 0, len(all))
	for _, e := range all {
		var parts []string
		for _, a := range e.r.Bundle.Artifacts {
			parts = append(parts, a.Name+" ("+a.Type+")")
		}
		out = append(out, Summary{ID: e.r.ID, Title: e.r.Title, Description: e.r.Description, Category: e.r.Category,
			Source: e.src, Contains: strings.Join(parts, ", "), Questions: len(e.r.Questions)})
	}
	return out
}

// Fill answers a template's questions into its bundle: defaults where an
// answer is blank, a required question left blank refused, a url answer
// checked, each helper question run through its helper. Secret answers are
// returned apart, by credential, never filled in; helpers' warnings are
// returned for the person to read.
func Fill(r Recipe, answers map[string]string) (core.ArtifactBundle, map[string]string, []string, error) {
	vals, secrets := map[string]any{}, map[string]string{}
	plain := map[string]string{}
	for _, q := range r.Questions {
		v := strings.TrimSpace(answers[q.Name])
		if v == "" {
			v = strings.TrimSpace(q.Default)
		}
		if v == "" {
			if q.Required {
				return core.ArtifactBundle{}, nil, nil, fmt.Errorf("%s is needed", q.Label)
			}
			if q.Secret {
				continue
			}
		}
		if len(q.Options) > 0 && v != "" {
			ok := false
			for _, o := range q.Options {
				ok = ok || o == v
			}
			if !ok {
				return core.ArtifactBundle{}, nil, nil, fmt.Errorf("%s must be one of %s", q.Label, strings.Join(q.Options, ", "))
			}
		}
		if (q.Kind == "url" || q.Kind == "http_url") && v != "" {
			lower := strings.ToLower(v)
			okScheme := strings.HasPrefix(lower, "https://") || (q.Kind == "http_url" && strings.HasPrefix(lower, "http://"))
			if !okScheme || strings.ContainsAny(v, " \"'<>") {
				if q.Kind == "url" {
					return core.ArtifactBundle{}, nil, nil, fmt.Errorf("%s must be an https address", q.Label)
				}
				return core.ArtifactBundle{}, nil, nil, fmt.Errorf("%s must be an http or https address", q.Label)
			}
			v = strings.TrimRight(v, "/")
		}
		if q.Secret {
			secrets[q.Credential] = v
			continue
		}
		plain[q.Name] = v
		vals[q.Name] = v
	}
	fillText := func(s string) string {
		return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			return plain[placeholderRe.FindStringSubmatch(m)[1]]
		})
	}
	var warnings []string
	for _, q := range r.Questions {
		if q.Helper == "" {
			continue
		}
		h, ok := LookupHelper(q.Helper)
		if !ok {
			return core.ArtifactBundle{}, nil, nil, fmt.Errorf("the helper %q is not on this gohort", q.Helper)
		}
		with := map[string]string{}
		for k, v := range q.With {
			with[k] = strings.TrimSpace(fillText(v))
		}
		out, warns, err := h.Run(plain[q.Name], with)
		if err != nil {
			return core.ArtifactBundle{}, nil, nil, fmt.Errorf("%s: %v", q.Label, err)
		}
		warnings = append(warnings, warns...)
		for k, v := range out {
			vals[q.Name+"."+k] = v
		}
	}
	lookup := func(m []string) (any, bool) {
		key := m[1]
		if m[2] != "" {
			key += "." + m[2]
		}
		v, ok := vals[key]
		return v, ok
	}
	out := walkAny(r.Bundle, func(s string) any {
		// A placeholder standing alone takes the value whole, so a helper's
		// output can be a JSON object (a connector's spec).
		if m := wholeRe.FindStringSubmatch(s); m != nil {
			if v, ok := lookup(m); ok {
				return v
			}
			return ""
		}
		return placeholderRe.ReplaceAllStringFunc(s, func(ph string) string {
			v, _ := lookup(placeholderRe.FindStringSubmatch(ph))
			if str, isStr := v.(string); isStr {
				return str
			}
			if v == nil {
				return ""
			}
			b, _ := json.Marshal(v)
			return string(b)
		})
	})
	if out.Bundle == "" {
		out.Bundle = core.ArtifactBundleFormat
	}
	out.ExportedAt = time.Now()
	return out, secrets, warnings, nil
}

// Install adds a template: fills the answers in, imports the pieces as
// drafts owned by owner, and writes each secret answer into its credential,
// which stays disabled until the admin tests and enables it. A credential
// that already existed is left as it was, secret included.
func Install(db core.Database, id, owner string, answers map[string]string) (core.ArtifactImportResult, error) {
	r, _, ok := Get(db, id)
	if !ok {
		return core.ArtifactImportResult{}, fmt.Errorf("no template %q", id)
	}
	bundle, secrets, warnings, err := Fill(r, answers)
	if err != nil {
		return core.ArtifactImportResult{}, err
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return core.ArtifactImportResult{}, err
	}
	res, err := core.ImportArtifactBundle(db, data, owner)
	if err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, warnings...)
	for cred, secret := range secrets {
		landed := false
		for _, o := range res.Outcomes {
			landed = landed || (o.Type == "credential" && o.Name == cred && o.Status == "imported")
		}
		if !landed {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the credential %q already existed, so the secret you gave was not written into it", cred))
			continue
		}
		c, ok := core.Secure().Load(cred)
		if !ok {
			continue
		}
		if err := core.Secure().Save(c, secret); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the secret for %q could not be stored: %v", cred, err))
		}
	}
	core.Log("[recipes] %s added template %q: %d imported, %d skipped", owner, id, res.Imported, res.Skipped)
	return res, nil
}

// Import stores a template from a file. A built-in's id is refused; an
// imported template with the same id is replaced.
func Import(db core.Database, data []byte, by string) (Recipe, error) {
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("this is not a template file: %v", err)
	}
	if err := Validate(r); err != nil {
		return r, err
	}
	if isBuiltIn(r.ID) {
		return r, fmt.Errorf("%q is a built-in template's id: give this one another", r.ID)
	}
	r.By, r.Added = by, time.Now()
	db.Set(storeTable, r.ID, r)
	return r, nil
}

// Delete removes an imported template. Built-ins stay.
func Delete(db core.Database, id string) error {
	if isBuiltIn(id) {
		return fmt.Errorf("built-in templates cannot be deleted")
	}
	var r Recipe
	if !db.Get(storeTable, id, &r) {
		return fmt.Errorf("no template %q", id)
	}
	db.Set(storeTable, id, Recipe{})
	return nil
}

// Export is a template as a file, without who added it here.
func Export(db core.Database, id string) ([]byte, error) {
	r, _, ok := Get(db, id)
	if !ok {
		return nil, fmt.Errorf("no template %q", id)
	}
	r.By, r.Added = "", time.Time{}
	return json.MarshalIndent(r, "", "  ")
}

// SaveQuestion turns a value in the pieces into a question. Value is the
// literal to replace with {{Name}} wherever it appears; a secret question
// names the credential whose secret it asks for instead.
type SaveQuestion struct {
	Question
	Value string `json:"value,omitempty"`
}

// Save builds a template from things already on this deployment: exports
// them (with what they depend on), turns each chosen value into a question,
// and stores the result after the same checks an imported file gets.
func Save(db core.Database, meta Recipe, sels []core.ArtifactSel, questions []SaveQuestion, by string) (Recipe, error) {
	if len(sels) == 0 {
		return meta, fmt.Errorf("pick at least one thing to put in the template")
	}
	bundle, err := core.ExportArtifactBundle(db, sels)
	if err != nil {
		return meta, err
	}
	for _, q := range questions {
		meta.Questions = append(meta.Questions, q.Question)
	}
	// Longest values first, so a value inside another is not replaced first.
	byLen := append([]SaveQuestion(nil), questions...)
	sort.SliceStable(byLen, func(i, j int) bool { return len(byLen[i].Value) > len(byLen[j].Value) })
	for _, q := range byLen {
		v := q.Value
		if q.Secret || strings.TrimSpace(v) == "" {
			continue
		}
		mark := "{{" + q.Name + "}}"
		bundle = walkStrings(bundle, func(s string) string { return strings.ReplaceAll(s, v, mark) })
	}
	bundle.ExportedAt, bundle.GohortVersion = time.Time{}, ""
	meta.Bundle = bundle
	if err := Validate(meta); err != nil {
		return meta, err
	}
	if isBuiltIn(meta.ID) {
		return meta, fmt.Errorf("%q is a built-in template's id: give this one another", meta.ID)
	}
	meta.By, meta.Added = by, time.Now()
	db.Set(storeTable, meta.ID, meta)
	return meta, nil
}
