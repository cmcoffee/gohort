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
	"github.com/cmcoffee/gohort/core/messaging"
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
	// into Credential's secret store after the import, or with WebhookSecret,
	// kept as that webhook connector's secret (its signing secret or token).
	Secret        bool   `json:"secret,omitempty"`
	Credential    string `json:"credential,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// Secrets are a template's secret answers, held apart from its pieces: by
// credential, and by webhook connector.
type Secrets struct {
	Credentials map[string]string
	Webhooks    map[string]string
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
	creds, conns := map[string]bool{}, map[string]bool{}
	for _, a := range r.Bundle.Artifacts {
		switch a.Type {
		case "credential":
			creds[a.Name] = true
		case "connector":
			conns[a.Name] = true
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
			switch {
			case q.Credential != "" && q.WebhookSecret != "":
				return fmt.Errorf("secret question %q goes to a credential or a webhook, not both", q.Name)
			case q.WebhookSecret != "":
				if !conns[q.WebhookSecret] {
					return fmt.Errorf("secret question %q must name a connector the template installs", q.Name)
				}
			case !creds[q.Credential]:
				return fmt.Errorf("secret question %q must name a credential the template installs", q.Name)
			}
		} else if q.Credential != "" || q.WebhookSecret != "" {
			return fmt.Errorf("question %q names where a secret goes but is not secret", q.Name)
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
func Fill(r Recipe, answers map[string]string) (core.ArtifactBundle, Secrets, []string, error) {
	vals := map[string]any{}
	secrets := Secrets{Credentials: map[string]string{}, Webhooks: map[string]string{}}
	webhookSecrets := map[string]string{} // connector name as written, answer
	plain := map[string]string{}
	for _, q := range r.Questions {
		v := strings.TrimSpace(answers[q.Name])
		if v == "" {
			v = strings.TrimSpace(q.Default)
		}
		if v == "" {
			if q.Required {
				return core.ArtifactBundle{}, Secrets{}, nil, fmt.Errorf("%s is needed", q.Label)
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
				return core.ArtifactBundle{}, Secrets{}, nil, fmt.Errorf("%s must be one of %s", q.Label, strings.Join(q.Options, ", "))
			}
		}
		if (q.Kind == "url" || q.Kind == "http_url") && v != "" {
			lower := strings.ToLower(v)
			okScheme := strings.HasPrefix(lower, "https://") || (q.Kind == "http_url" && strings.HasPrefix(lower, "http://"))
			if !okScheme || strings.ContainsAny(v, " \"'<>") {
				if q.Kind == "url" {
					return core.ArtifactBundle{}, Secrets{}, nil, fmt.Errorf("%s must be an https address", q.Label)
				}
				return core.ArtifactBundle{}, Secrets{}, nil, fmt.Errorf("%s must be an http or https address", q.Label)
			}
			v = strings.TrimRight(v, "/")
		}
		if q.Secret {
			if q.WebhookSecret != "" {
				webhookSecrets[q.WebhookSecret] = v
			} else {
				secrets.Credentials[q.Credential] = v
			}
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
	for conn, v := range webhookSecrets {
		secrets.Webhooks[fillText(conn)] = v
	}
	var warnings []string
	for _, q := range r.Questions {
		if q.Helper == "" {
			continue
		}
		h, ok := LookupHelper(q.Helper)
		if !ok {
			return core.ArtifactBundle{}, Secrets{}, nil, fmt.Errorf("the helper %q is not on this gohort", q.Helper)
		}
		with := map[string]string{}
		for k, v := range q.With {
			with[k] = strings.TrimSpace(fillText(v))
		}
		out, warns, err := h.Run(plain[q.Name], with)
		if err != nil {
			return core.ArtifactBundle{}, Secrets{}, nil, fmt.Errorf("%s: %v", q.Label, err)
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

// ShownAs is the answer naming, for people, the connector a template makes:
// what its row and the pages that offer it show. Blank shows the template's
// title.
const ShownAs = "_shown_as"

// ConnectorNameQuestion is the question whose answer names the connector a
// template makes, {{question}} in the connector's name, or "" when none
// does. The name is gohort's handle for it, never asked of a person: left
// blank, Install makes one (namedFreely).
func ConnectorNameQuestion(r Recipe) string {
	for _, a := range r.Bundle.Artifacts {
		if a.Type != "connector" {
			continue
		}
		if q, ok := strings.CutPrefix(a.Name, "{{"); ok {
			if q, ok := strings.CutSuffix(q, "}}"); ok && !strings.ContainsAny(q, "{}.") {
				return q
			}
		}
	}
	return ""
}

// namedFreely is answers with the connector's name made when it is blank:
// the question's default (else the template's id), numbered past any
// connector already named so ("serper-search", "serper-search-2"), so a
// template added again adds another rather than finding the name taken.
func namedFreely(db core.Database, r Recipe, answers map[string]string) map[string]string {
	q := ConnectorNameQuestion(r)
	if q == "" || strings.TrimSpace(answers[q]) != "" {
		return answers
	}
	base := r.ID
	for _, question := range r.Questions {
		if question.Name == q && strings.TrimSpace(question.Default) != "" {
			base = strings.TrimSpace(question.Default)
		}
	}
	out := make(map[string]string, len(answers)+1)
	for k, v := range answers {
		out[k] = v
	}
	out[q] = core.FreeConnectorName(db, base)
	return out
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
	answers = namedFreely(db, r, answers)
	bundle, secrets, warnings, err := Fill(r, answers)
	if err != nil {
		return core.ArtifactImportResult{}, err
	}
	renamed, err := ownCredentials(db, owner, &bundle, secrets)
	if err != nil {
		return core.ArtifactImportResult{}, err
	}
	warnings = append(warnings, renamed...)
	data, err := json.Marshal(bundle)
	if err != nil {
		return core.ArtifactImportResult{}, err
	}
	res, err := core.ImportArtifactBundle(db, data, owner)
	if err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, warnings...)
	landedAs := func(typ, name string) bool {
		for _, o := range res.Outcomes {
			if o.Type == typ && o.Name == name && o.Status == "imported" {
				return true
			}
		}
		return false
	}
	for conn, secret := range secrets.Webhooks {
		if !landedAs("connector", conn) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the connector %q already existed, so the webhook secret you gave was not written into it", conn))
			continue
		}
		if err := messaging.SetWebhookSecret(conn, secret); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the webhook secret for %q could not be stored: %v", conn, err))
		}
	}
	for cred, secret := range secrets.Credentials {
		if !landedAs("credential", cred) {
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
	// A connector it made is shown as asked (ShownAs), else as the template
	// is, numbered apart from another shown the same.
	shown := strings.TrimSpace(answers[ShownAs])
	if shown == "" {
		shown = r.Title
	}
	for _, o := range res.Outcomes {
		if o.Type != "connector" || o.Status != "imported" {
			continue
		}
		if c, ok := core.GetConnector(db, o.Name); ok && strings.TrimSpace(c.DisplayName) == "" {
			core.SetConnectorDisplayName(db, o.Name, core.FreeDisplayName(db, o.Name, shown))
		}
	}
	core.Log("[recipes] %s added template %q: %d imported, %d skipped", owner, id, res.Imported, res.Skipped)
	return res, nil
}

// ownCredentials gives a credential the bundle brings a name of its own when
// one by that name is here already and something new in the bundle names it:
// a connector added under another name, say. Skipped on import, the
// existing one would be shared, made for another address and keeping its
// own secret, so the key given for this one would go nowhere and its calls
// would be refused or sent with the other's key. The new pieces are pointed
// at the new name; a bundle whose pieces are all here already (the template
// added again) is left as it is, and imports nothing. It says what it
// renamed.
func ownCredentials(db core.Database, owner string, bundle *core.ArtifactBundle, secrets Secrets) ([]string, error) {
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	preview, err := core.PreviewArtifactBundle(db, data, owner)
	if err != nil {
		return nil, err
	}
	importing := map[int]bool{}
	for i, item := range preview.Items {
		importing[i] = item.Action == "import"
	}
	taken := map[string]bool{}
	for _, a := range bundle.Artifacts {
		if a.Type == "credential" {
			taken[credentialName(a)] = true
		}
	}
	var notes []string
	for i, a := range bundle.Artifacts {
		name := credentialName(a)
		if a.Type != "credential" || name == "" {
			continue
		}
		if _, exists := core.Secure().Load(name); !exists {
			continue
		}
		var users []int
		for j, b := range bundle.Artifacts {
			if j != i && importing[j] && namesCredential(b.Recipe, name) {
				users = append(users, j)
			}
		}
		if len(users) == 0 {
			continue
		}
		fresh := name
		for n := 2; ; n++ {
			fresh = fmt.Sprintf("%s_%d", name, n)
			if _, exists := core.Secure().Load(fresh); !exists && !taken[fresh] {
				break
			}
		}
		taken[fresh] = true
		for _, j := range append(users, i) {
			rec, err := renameCredential(bundle.Artifacts[j].Recipe, name, fresh, j == i)
			if err != nil {
				return nil, err
			}
			bundle.Artifacts[j].Recipe = rec
		}
		bundle.Artifacts[i].Name = fresh
		if secret, ok := secrets.Credentials[name]; ok {
			delete(secrets.Credentials, name)
			secrets.Credentials[fresh] = secret
		}
		notes = append(notes, fmt.Sprintf("a credential named %q is here already, so this one is %q", name, fresh))
	}
	return notes, nil
}

// credentialName is the name a credential artifact's recipe gives it.
func credentialName(a core.PortableArtifact) string {
	var probe struct {
		Name string `json:"name"`
	}
	json.Unmarshal(a.Recipe, &probe)
	return strings.TrimSpace(probe.Name)
}

// credentialKeys are the fields a piece names a credential by.
var credentialKeys = map[string]bool{"credential": true, "secure_cred": true}

// namesCredential reports whether a piece's recipe names credential name.
func namesCredential(recipe json.RawMessage, name string) bool {
	var v any
	if json.Unmarshal(recipe, &v) != nil {
		return false
	}
	found := false
	walkCredentialRefs(v, func(ref string) string {
		found = found || ref == name
		return ref
	})
	return found
}

// renameCredential points a recipe's references to credential from at to,
// and, for the credential's own recipe (self), its name.
func renameCredential(recipe json.RawMessage, from, to string, self bool) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(recipe, &v); err != nil {
		return nil, err
	}
	walkCredentialRefs(v, func(ref string) string {
		if ref == from {
			return to
		}
		return ref
	})
	if m, ok := v.(map[string]any); ok && self {
		m["name"] = to
	}
	return json.Marshal(v)
}

// walkCredentialRefs calls fix on every string a credential is named by in
// v, at any depth, and puts back what it gives.
func walkCredentialRefs(v any, fix func(string) string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && credentialKeys[k] {
				x[k] = fix(strings.TrimSpace(s))
				continue
			}
			walkCredentialRefs(val, fix)
		}
	case []any:
		for _, val := range x {
			walkCredentialRefs(val, fix)
		}
	}
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

// TemplateConnectorSpec is the spec of the connector built-in template id
// makes, its install questions answered with their defaults: what a
// connection made from it now declares.
func TemplateConnectorSpec(id string) (json.RawMessage, bool) {
	for _, r := range builtins() {
		if r.ID == id {
			return connectorSpec(r, nil)
		}
	}
	return nil, false
}

// connectorSpec is the spec of the connector template r makes, answered.
func connectorSpec(r Recipe, answers map[string]string) (json.RawMessage, bool) {
	bundle, _, _, err := Fill(r, answers)
	if err != nil {
		return nil, false
	}
	for _, a := range bundle.Artifacts {
		if a.Type != "connector" {
			continue
		}
		var rec struct {
			Spec json.RawMessage `json:"spec"`
		}
		if json.Unmarshal(a.Recipe, &rec) == nil && len(rec.Spec) > 0 {
			return rec.Spec, true
		}
	}
	return nil, false
}

