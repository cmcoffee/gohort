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
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
		// Firm: a second copy is worse than a failed update. The person
		// then has two pages, and every later update finds the wrong one.
		fail := "call report_published with ok=false saying why"
		if t.Uses == "agent" {
			fail = "say so and why"
		}
		b.WriteString("\n\nThis document is ALREADY published here, at " + prev + ". Update that page in place: find it from this address (its id is usually in it), read it, and change it. Never create a new one. If you cannot find or update it, change nothing and " + fail + ".")
		// The page's own table of contents (a destination's macro or block)
		// is kept on an update: either it was named last time and goes out
		// again by itself, or this run is asked to name it.
		if req.Nav.Contents != "" && t.Uses != "agent" {
			if req.NavContents != nil {
				b.WriteString(" Its table of contents and its links to its own sections go out as they did last time; call set_document_links only to change them.")
			} else {
				b.WriteString(" Keep the page's table of contents: if it has this destination's own table-of-contents block, call set_document_links with that same markup as contents before you send the document, so the update does not replace it with a plain list.")
			}
		} else if req.Nav.Contents != "" {
			b.WriteString(" Keep the page's table of contents as this destination's own table-of-contents block, as it is on the page now.")
		}
	} else if t.Uses == "agent" {
		b.WriteString("\n\nWhen it is published, say where: give its address (a link) in your reply, so the next update changes that page instead of making another.")
	}
	// An integration's run gets the document by reference (docs.ReferenceInstruction):
	// it writes a placeholder where the body goes, and never types it out.
	// An agent is handed the text, since its tools are its own.
	if t.Uses != "agent" {
		b.WriteString("\n\n" + docs.ReferenceInstruction(req))
		return b.String()
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
	var said, url, contentsNote string
	var navLinks map[string]string
	var navContents *string
	var err error
	if t.Uses == "agent" {
		// An update is checked like every other: the page as it stands is
		// read first, so an edit made there since the last publish is not
		// overwritten. The agent reads it, since its tools are its own.
		if prev := strings.TrimSpace(req.ExternalID); prev != "" && docs.GuardsUpdate(ctx) {
			docs.PublishStep(ctx, "Asking the agent %s to read the page as it stands first", t.Agent)
			cur, rerr := d.readViaAgent(ctx, user, t, req.Title, prev)
			if rerr != nil {
				return docs.PublishResult{}, fmt.Errorf("could not read %s as it stands before updating it, so it was not overwritten: %w", prev, rerr)
			}
			if gerr := docs.CheckBeforeUpdate(ctx, cur); gerr != nil {
				return docs.PublishResult{}, gerr
			}
		}
		docs.PublishStep(ctx, "Handed to the agent %s", t.Agent)
		said, err = docs.PublishViaAgent(ctx, user, t.Agent, instruction)
		if err == nil {
			docs.PublishStep(ctx, "The agent said: %s", clipToLabel(said))
			// Where it put it, so the next publish updates that page. An
			// agent's run returned only what it said, so every republish was
			// a fresh publish: no update, no check before it.
			url = chFirst(strings.TrimSpace(req.ExternalID), firstLink(said))
			if url != "" && docs.WantsReadBack(ctx) {
				if cur, rerr := d.readViaAgent(ctx, user, t, req.Title, url); rerr == nil {
					docs.ReportReadBack(ctx, cur)
				}
			}
		}
	} else {
		bctx := docs.WithPublishBody(ctx, req)
		said, url, err = docs.PublishViaCredential(bctx, user, t.Credential, instruction)
		navLinks, navContents = docs.PublishBodyFrom(bctx).Navigation()
		// The document's own contents list went out as a plain list: say
		// so, rather than a bare "Published" over a page whose table of
		// contents is gone.
		if err == nil && req.Nav.Contents != "" && navContents == nil {
			contentsNote = "its table of contents went out as a plain list, not this destination's own: publish again and it is asked for it"
			docs.PublishStep(ctx, "Published, but %s", contentsNote)
		}
	}
	if err != nil {
		return docs.PublishResult{}, err
	}
	Log("[publish.target] user=%q target=%q published %q", user, t.Label, req.Title)
	label := t.Label
	if note := clipToLabel(said); note != "" {
		label += " - " + note
	}
	if contentsNote != "" {
		label += " (" + contentsNote + ")"
	}
	// The address rides back as the ExternalID too, so a Republish hands it to
	// the next pass as "update this one".
	return docs.PublishResult{URL: url, ExternalID: url, Label: label, Updated: strings.TrimSpace(req.ExternalID) != "",
		NavLinks: navLinks, NavContents: navContents}, nil
}

// ReadPublished reads a copy published to one of the person's targets, through
// the target's own integration. A target that hands its documents to an agent
// cannot: what that agent made is its own business.
func (d *targetsDest) ReadPublished(ctx context.Context, user string, rec docs.PublishRecord) (docs.PublishedCopy, error) {
	t, ok := d.app.loadTarget(user, strings.TrimPrefix(rec.Kind, TargetKindPrefix))
	if !ok {
		return docs.PublishedCopy{}, fmt.Errorf("no publishing target %q", rec.Kind)
	}
	where := chFirst(strings.TrimSpace(rec.URL), strings.TrimSpace(rec.ExternalID))
	if t.Uses == "agent" {
		if where == "" {
			return docs.PublishedCopy{}, docs.ErrCannotReadBack
		}
		return d.readViaAgent(ctx, user, t, rec.Title, where)
	}
	var b strings.Builder
	b.WriteString("Read a document published through this integration, as it stands now. How documents are published here:\n\n")
	b.WriteString(t.Instructions)
	b.WriteString("\n\nThe document: " + chFirst(rec.Title, "(untitled)") + ", published at " + where + ".")
	return docs.ReadViaCredential(ctx, user, t.Credential, b.String())
}

// readViaAgent asks a target's agent for a published copy as it stands, word
// for word, changing nothing. The reply carries the document between two
// marker lines; one without them is not taken as the page, since what an
// agent says about a page is not the page.
func (d *targetsDest) readViaAgent(ctx context.Context, user string, t Target, title, where string) (docs.PublishedCopy, error) {
	instruction := t.Instructions + "\n\nDo NOT change anything. Read the document " + chFirst(strings.TrimSpace(title), "(untitled)") +
		" as it stands now at " + where + ", and reply with its full text in Markdown, word for word, between a line reading " +
		agentReadOpen + " and a line reading " + agentReadClose + ". Nothing else goes between them. If you cannot read it, say so and why, without the markers."
	said, err := docs.PublishViaAgent(ctx, user, t.Agent, instruction)
	if err != nil {
		return docs.PublishedCopy{}, err
	}
	body, ok := betweenMarkers(said, agentReadOpen, agentReadClose)
	if !ok {
		return docs.PublishedCopy{}, fmt.Errorf("the agent did not return the page: %s", clipToLabel(said))
	}
	return docs.PublishedCopy{Body: body}, nil
}

const (
	agentReadOpen  = "<<<DOCUMENT"
	agentReadClose = "DOCUMENT>>>"
)

// betweenMarkers is the text between the first open marker and the close
// after it, trimmed.
func betweenMarkers(s, open, close string) (string, bool) {
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:j]), true
}

// firstLink is the first http(s) address in s, less trailing punctuation.
func firstLink(s string) string {
	for _, f := range strings.Fields(s) {
		if i := strings.Index(f, "http"); i >= 0 {
			u := strings.TrimRight(f[i:], ".,;:!?)]}>\"'`")
			if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
				return u
			}
		}
	}
	return ""
}

// --- HTTP: the Publishing targets section --------------------------------

// handleTargets is the user's targets, as the section's table shows them.
func (T *PublishApp) handleTargets(c *jsonapi.Call) (any, error) {
	user := c.User
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
	return rows, nil
}

// handleTargetSave creates a target, or saves the one it names. The pills own
// who may use it, so a save keeps them.
func (T *PublishApp) handleTargetSave(c *jsonapi.Call, t Target) (any, error) {
	user := c.User
	t, err := normalizeTarget(t)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if prev, ok := T.loadTarget(user, t.ID); ok {
		t.Agents, t.Created = prev.Agents, prev.Created // the pills own who may use it
	} else {
		t.ID, t.Created, t.Agents = UUIDv4(), time.Now(), nil
	}
	T.targetsDB(user).Set(targetTable, t.ID, t)
	Log("[publish.target] user=%q saved target %q", user, t.Label)
	return map[string]any{"id": t.ID}, nil
}

// targetIn is the user's target the path names.
func (T *PublishApp) targetIn(c *jsonapi.Call) (Target, error) {
	t, found := T.loadTarget(c.User, c.R.PathValue("id"))
	if !found {
		return Target{}, jsonapi.NotFound("no such target")
	}
	return t, nil
}

// handleTarget is one target.
func (T *PublishApp) handleTarget(c *jsonapi.Call) (any, error) {
	return T.targetIn(c)
}

// handleTargetDelete removes one target.
func (T *PublishApp) handleTargetDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	t, err := T.targetIn(c)
	if err != nil {
		return nil, err
	}
	T.targetsDB(c.User).Unset(targetTable, t.ID)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleTargetOptions is a field's options, asked of the system it names.
func (T *PublishApp) handleTargetOptions(c *jsonapi.Call) (any, error) {
	t, err := T.targetIn(c)
	if err != nil {
		return nil, err
	}
	opts, err := T.liveOptions(c.R.Context(), c.User, t, c.R.URL.Query().Get("field"))
	if err != nil {
		return nil, jsonapi.Status(http.StatusBadGateway, err.Error())
	}
	return map[string]any{"options": opts}, nil
}

// handleTargetAgents is the pills for which agents may publish to this target
// with the publish tool.
func (T *PublishApp) handleTargetAgents(c *jsonapi.Call) (any, error) {
	t, err := T.targetIn(c)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, o := range AgentNameOptions(c.User) {
		on := false
		for _, a := range t.Agents {
			on = on || a == o.Value
		}
		items = append(items, map[string]any{"key": o.Value, "label": o.Label, "on": on})
	}
	return map[string]any{"items": items,
		"note": "An agent switched on here gets a publish tool naming this target, with its form as the tool's arguments. You can always publish to it yourself from Scribe."}, nil
}

// handleTargetAgentSet switches one agent's pill.
func (T *PublishApp) handleTargetAgentSet(c *jsonapi.Call, one struct {
	Target string `json:"target"`
	On     bool   `json:"on"`
}) (any, error) {
	t, err := T.targetIn(c)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(one.Target) == "" {
		return nil, jsonapi.BadRequest("bad request")
	}
	known := false
	for _, o := range AgentNameOptions(c.User) {
		known = known || o.Value == one.Target
	}
	if !known {
		return nil, jsonapi.NotFound("no such agent")
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
	T.targetsDB(c.User).Set(targetTable, t.ID, t)
	return jsonapi.Empty(http.StatusNoContent), nil
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
