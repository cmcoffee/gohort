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
	if len(opts) > 0 && typ == "text" {
		typ = "select"
	}
	return docs.PublishField{Name: f.Name, Label: f.Label, Type: typ, Options: opts,
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
		said, err = docs.PublishViaAgent(ctx, user, t.Agent, instruction)
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
