// Publishing targets a person builds themselves.
//
// Confluence and the webhook are one per deployment and an admin sets them up;
// an agent destination is an admin's too. A TARGET is a user's own: one of
// their API integrations (or one of their agents), an instruction saying how to
// publish there, and a short intake form asked each time. Publishing runs a
// short agent pass that holds only that integration's API (see orchestrate's
// credential publisher) and reports where the document landed, so a new place
// to publish is a form, not code.
//
// Every target is its own publish kind, "target:<id>", served by ONE registered
// destination (the "target:" family in core/docs): a document keeps a publish
// record per kind, so each target gets its own record and its own Republish.
package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

const (
	targetTable      = "publish_targets"
	TargetKindPrefix = "target:"
)

// Target is one publishing target.
type Target struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	Desc         string        `json:"desc,omitempty"`
	Uses         string        `json:"uses"` // "api" or "agent"
	Credential   string        `json:"credential,omitempty"`
	Agent        string        `json:"agent,omitempty"`
	Instructions string        `json:"instructions"`
	Fields       []TargetField `json:"fields,omitempty"`
	Agents       []string      `json:"agents,omitempty"` // agent ids that may publish here with the publish tool
	Created      time.Time     `json:"created"`
}

// TargetField is one intake question, in the shape the rows editor edits:
// options as one comma-separated string, required as "yes".
type TargetField struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Type     string `json:"type,omitempty"`
	Options  string `json:"options,omitempty"`
	Required string `json:"required,omitempty"`
	Help     string `json:"help,omitempty"`
	// OptionsFrom is an API path on the target's integration, and optionally
	// the field of each item to show: "/wp-json/wp/v2/categories name". The
	// list is fetched when the Publish form opens, with no model.
	OptionsFrom string `json:"options_from,omitempty"`
}

func (f TargetField) field() docs.PublishField {
	var opts []string
	for _, o := range strings.Split(f.Options, ",") {
		if o = strings.TrimSpace(o); o != "" {
			opts = append(opts, o)
		}
	}
	typ := strings.TrimSpace(f.Type)
	if typ == "" {
		typ = "text"
	}
	from := strings.TrimSpace(f.OptionsFrom)
	if (len(opts) > 0 || from != "") && typ == "text" {
		typ = "select"
	}
	return docs.PublishField{Name: f.Name, Label: f.Label, Type: typ, Options: opts, OptionsFrom: from,
		Required: strings.EqualFold(strings.TrimSpace(f.Required), "yes"), Help: f.Help}
}

func (t Target) fields() []docs.PublishField {
	out := make([]docs.PublishField, 0, len(t.Fields))
	for _, f := range t.Fields {
		if strings.TrimSpace(f.Name) != "" {
			out = append(out, f.field())
		}
	}
	return out
}

// fieldName makes a question's key from what the person typed.
func fieldName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func (T *PublishApp) targetsDB(user string) Database {
	if T == nil || T.DB == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	return UserDB(T.DB, user)
}

func (T *PublishApp) listTargets(user string) []Target {
	db := T.targetsDB(user)
	if db == nil {
		return nil
	}
	var out []Target
	for _, k := range db.Keys(targetTable) {
		var t Target
		if db.Get(targetTable, k, &t) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

func (T *PublishApp) loadTarget(user, id string) (Target, bool) {
	db := T.targetsDB(user)
	var t Target
	if db == nil || strings.TrimSpace(id) == "" || !db.Get(targetTable, strings.TrimSpace(id), &t) {
		return Target{}, false
	}
	return t, true
}

// normalizeTarget cleans a posted target and says what is missing.
func normalizeTarget(t Target) (Target, error) {
	t.Label, t.Desc = strings.TrimSpace(t.Label), strings.TrimSpace(t.Desc)
	t.Instructions = strings.TrimSpace(t.Instructions)
	t.Credential, t.Agent = strings.TrimSpace(t.Credential), strings.TrimSpace(t.Agent)
	if t.Uses != "agent" {
		t.Uses = "api"
	}
	switch {
	case t.Label == "":
		return t, fmt.Errorf("give the target a name")
	case t.Uses == "api" && t.Credential == "":
		return t, fmt.Errorf("pick the API integration this target publishes through")
	case t.Uses == "agent" && t.Agent == "":
		return t, fmt.Errorf("pick the agent that publishes for this target")
	case t.Instructions == "":
		return t, fmt.Errorf("say how to publish there: the instruction is what the publish follows")
	}
	fields := t.Fields[:0:0]
	seen := map[string]bool{}
	for _, f := range t.Fields {
		f.Name = fieldName(chFirst(f.Name, f.Label))
		if f.Name == "" || seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		f.Label = strings.TrimSpace(chFirst(f.Label, f.Name))
		fields = append(fields, f)
	}
	t.Fields = fields
	return t, nil
}

func chFirst(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// targetInstruction is what the publishing pass is handed: the target's
// instruction, the title, the answers, then the document.
func targetInstruction(t Target, req docs.PublishRequest) string {
	title := chFirst(strings.TrimSpace(req.Title), strings.TrimSpace(req.Doc.Title))
	var b strings.Builder
	b.WriteString(t.Instructions)
	b.WriteString("\n\nTitle: " + title)
	for _, f := range t.fields() {
		if v := strings.TrimSpace(req.Answers[f.Name]); v != "" {
			b.WriteString("\n" + chFirst(f.Label, f.Name) + ": " + v)
		}
	}
	if prev := strings.TrimSpace(req.ExternalID); prev != "" {
		b.WriteString("\n\nThis document was published here before, at " + prev + ". Update that one rather than making a new one, if the API allows it.")
	}
	// The document's links to its own sections, rebuilt the destination's way.
	if nav := docs.NavInstruction(req.Nav); nav != "" {
		b.WriteString("\n\n" + nav)
	}
	b.WriteString("\n\n---\n\n")
	b.WriteString(req.Doc.Markdown)
	return b.String()
}

// targetsDest serves every "target:<id>" kind.
type targetsDest struct{ app *PublishApp }

func (d *targetsDest) Kind() string  { return TargetKindPrefix }
func (d *targetsDest) Label() string { return "Your publishing targets" }

func (d *targetsDest) Available(user string) (bool, string) {
	if len(d.app.listTargets(user)) == 0 {
		return false, "you have no publishing targets yet: make one in Extensions, Publishing targets"
	}
	return true, ""
}

func (d *targetsDest) Targets(ctx context.Context, user string) ([]docs.PublishTarget, error) {
	var out []docs.PublishTarget
	for _, t := range d.app.listTargets(user) {
		out = append(out, docs.PublishTarget{ID: t.ID, Title: t.Label, Desc: t.Desc, Group: "Your targets"})
	}
	return out, nil
}

func (d *targetsDest) TargetSpecs(ctx context.Context, user string) []docs.PublishTargetSpec {
	var out []docs.PublishTargetSpec
	for _, t := range d.app.listTargets(user) {
		out = append(out, docs.PublishTargetSpec{
			Kind:   TargetKindPrefix + t.ID,
			Target: docs.PublishTarget{ID: t.ID, Title: t.Label, Desc: t.Desc, Group: "Your targets"},
			Fields: t.fields(), Agents: t.Agents,
		})
	}
	return out
}

func (d *targetsDest) FieldOptions(ctx context.Context, user, kind, field string) ([]string, error) {
	t, ok := d.app.loadTarget(user, strings.TrimPrefix(kind, TargetKindPrefix))
	if !ok {
		return nil, fmt.Errorf("no publishing target %q", kind)
	}
	return d.app.liveOptions(ctx, user, t, field)
}

func (d *targetsDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	t, ok := d.app.loadTarget(user, req.Target)
	if !ok {
		return docs.PublishResult{}, fmt.Errorf("no publishing target %q", req.Target)
	}
	if missing := docs.MissingAnswers(t.fields(), req.Answers); len(missing) > 0 {
		return docs.PublishResult{}, fmt.Errorf("%s needs: %s", t.Label, strings.Join(missing, ", "))
	}
	instruction := targetInstruction(t, req)
	var said, url string
	var err error
	if t.Uses == "agent" {
		docs.PublishStep(ctx, "Handed to the agent %s", t.Agent)
		said, err = docs.PublishViaAgent(ctx, user, t.Agent, instruction)
		if err == nil {
			docs.PublishStep(ctx, "The agent said: %s", clipToLabel(said))
		}
	} else {
		said, url, err = docs.PublishViaCredential(ctx, user, t.Credential, instruction)
	}
	if err != nil {
		return docs.PublishResult{}, err
	}
	Log("[publish.target] user=%q target=%q published %q", user, t.Label, req.Title)
	label := t.Label
	if note := clipToLabel(said); note != "" {
		label += " - " + note
	}
	// The address rides back as the ExternalID too, so a Republish hands it to
	// the next pass as "update this one".
	return docs.PublishResult{URL: url, ExternalID: url, Label: label, Updated: strings.TrimSpace(req.ExternalID) != ""}, nil
}

// --- HTTP: the Publishing targets section --------------------------------

func (T *PublishApp) handleTargets(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	db := T.targetsDB(user)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/targets"), "/")
	id, action, _ := strings.Cut(rest, "/")
	switch {
	case id == "" && r.Method == http.MethodGet:
		rows := []map[string]any{}
		names := map[string]string{}
		for _, o := range AgentNameOptions(user) {
			names[o.Value] = o.Label
		}
		for _, t := range T.listTargets(user) {
			uses := "API: " + t.Credential
			if t.Uses == "agent" {
				uses = "Agent: " + chFirst(names[t.Agent], t.Agent)
			}
			var asks, who []string
			for _, f := range t.Fields {
				asks = append(asks, chFirst(f.Label, f.Name))
			}
			for _, a := range t.Agents {
				who = append(who, chFirst(names[a], a))
			}
			rows = append(rows, map[string]any{
				"id": t.ID, "label": t.Label, "desc": t.Desc, "uses": uses,
				"asks":   chFirst(strings.Join(asks, ", "), "nothing"),
				"agents": chFirst(strings.Join(who, ", "), "only you, from Scribe"),
			})
		}
		writeTargetJSON(w, rows)
	case id == "" && r.Method == http.MethodPost:
		var t Target
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&t); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		t, err := normalizeTarget(t)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if prev, ok := T.loadTarget(user, t.ID); ok {
			t.Agents, t.Created = prev.Agents, prev.Created // the pills own who may use it
		} else {
			t.ID, t.Created, t.Agents = UUIDv4(), time.Now(), nil
		}
		db.Set(targetTable, t.ID, t)
		Log("[publish.target] user=%q saved target %q", user, t.Label)
		writeTargetJSON(w, map[string]any{"id": t.ID})
	case id != "" && action == "":
		t, found := T.loadTarget(user, id)
		if !found {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeTargetJSON(w, t)
		case http.MethodDelete:
			db.Unset(targetTable, t.ID)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case id != "" && action == "agents":
		T.targetAgentPills(w, r, user, id)
	case id != "" && action == "options" && r.Method == http.MethodGet:
		t, found := T.loadTarget(user, id)
		if !found {
			http.NotFound(w, r)
			return
		}
		opts, err := T.liveOptions(r.Context(), user, t, r.URL.Query().Get("field"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeTargetJSON(w, map[string]any{"options": opts})
	default:
		http.NotFound(w, r)
	}
}

// targetAgentPills serves and applies the pills for which agents may publish
// to this target with the publish tool.
func (T *PublishApp) targetAgentPills(w http.ResponseWriter, r *http.Request, user, id string) {
	t, found := T.loadTarget(user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		items := []map[string]any{}
		for _, o := range AgentNameOptions(user) {
			on := false
			for _, a := range t.Agents {
				on = on || a == o.Value
			}
			items = append(items, map[string]any{"key": o.Value, "label": o.Label, "on": on})
		}
		writeTargetJSON(w, map[string]any{"items": items,
			"note": "An agent switched on here gets a publish tool naming this target, with its form as the tool's arguments. You can always publish to it yourself from Scribe."})
	case http.MethodPost:
		var one struct {
			Target string `json:"target"`
			On     bool   `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&one); err != nil || strings.TrimSpace(one.Target) == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		known := false
		for _, o := range AgentNameOptions(user) {
			known = known || o.Value == one.Target
		}
		if !known {
			http.Error(w, "no such agent", http.StatusNotFound)
			return
		}
		out := t.Agents[:0:0]
		for _, a := range t.Agents {
			if a != one.Target {
				out = append(out, a)
			}
		}
		if one.On {
			out = append(out, one.Target)
		}
		t.Agents = out
		T.targetsDB(user).Set(targetTable, t.ID, t)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeTargetJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// maxLiveOptions bounds a fetched list: a form is not a search box.
const maxLiveOptions = 200

// liveOptions fetches one question's options from the target's integration:
// a GET of its OptionsFrom path, the list found in the JSON, the named field
// (or a likely one) of each item. No model, and the same credential rules as
// any call through it (its base URL, allowed paths, audit).
func (T *PublishApp) liveOptions(ctx context.Context, user string, t Target, fieldKey string) ([]string, error) {
	var f TargetField
	for _, c := range t.Fields {
		if c.Name == strings.TrimSpace(fieldKey) {
			f = c
		}
	}
	from := strings.Fields(strings.TrimSpace(f.OptionsFrom))
	switch {
	case f.Name == "":
		return nil, fmt.Errorf("%s has no question %q", t.Label, fieldKey)
	case len(from) == 0:
		return nil, fmt.Errorf("that question has no options to fetch")
	case t.Uses != "api":
		return nil, fmt.Errorf("options come from an API integration, and %s publishes through an agent", t.Label)
	case strings.HasPrefix(t.Credential, docs.MCPIntegrationPrefix):
		return nil, fmt.Errorf("options come from an API integration's GET, and %s publishes through an MCP server: list them in Options instead", t.Label)
	}
	out, err := Secure().DispatchToolCallArgs(&ToolSession{Username: user}, t.Credential,
		map[string]any{"url": from[0], "method": "GET", "__pipe_following": true})
	if err != nil {
		return nil, err
	}
	status, body := "", out
	if strings.HasPrefix(out, "HTTP ") {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			status, body = out[:i], out[i+1:]
		}
	}
	if status != "" && !strings.HasPrefix(status, "HTTP 2") {
		return nil, fmt.Errorf("%s answered %s", t.Credential, strings.TrimSpace(status))
	}
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &v); err != nil {
		return nil, fmt.Errorf("%s did not answer with JSON: %v", from[0], err)
	}
	key := ""
	if len(from) > 1 {
		key = from[1]
	}
	opts := extractOptions(v, key)
	if len(opts) == 0 {
		return nil, fmt.Errorf("%s returned no list to pick from", from[0])
	}
	return opts, nil
}

// extractOptions finds the list in a JSON answer (the whole answer, or the
// first list under a usual key) and reads each item: a string as it is, an
// object's named field, or its first likely one.
func extractOptions(v any, key string) []string {
	list, ok := v.([]any)
	if !ok {
		if m, isMap := v.(map[string]any); isMap {
			for _, k := range []string{"data", "items", "results", "values", "records", "entries"} {
				if l, isList := m[k].([]any); isList {
					list, ok = l, true
					break
				}
			}
			if !ok {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if l, isList := m[k].([]any); isList {
						list, ok = l, true
						break
					}
				}
			}
		}
	}
	candidates := []string{key, "name", "title", "label", "key", "slug", "value", "id"}
	seen := map[string]bool{}
	var out []string
	for _, item := range list {
		s := ""
		switch it := item.(type) {
		case string:
			s = it
		case float64:
			s = fmt.Sprint(it)
		case map[string]any:
			for _, c := range candidates {
				if c == "" {
					continue
				}
				switch val := it[c].(type) {
				case string:
					s = val
				case float64:
					s = fmt.Sprint(val)
				case map[string]any: // WordPress-style {"rendered": "..."}
					if r, ok := val["rendered"].(string); ok {
						s = r
					}
				}
				if strings.TrimSpace(s) != "" {
					break
				}
			}
		}
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
			if len(out) >= maxLiveOptions {
				break
			}
		}
	}
	return out
}
