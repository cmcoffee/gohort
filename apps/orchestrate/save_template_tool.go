package orchestrate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/recipes"
)

// save_template is Builder's way to package what it built as a template: the
// same thing Admin > Templates > Save as template does by hand. "Integrate
// Acme's API and make it a template" is then one conversation: Builder drafts
// the credential and tools, and saves them as a template whose questions are
// the values that differ between deployments.
//
// It happens only when an administrator means it, and that is enforced, not
// asked of the model:
//
//   - The tool is in Builder's catalog only when the person talking to it is
//     an administrator, and the handler checks again.
//   - Every call stops on a confirmation card showing the title, pieces and
//     questions, and waits for the administrator to approve. There is no
//     "always allow", and a run with nobody watching (a schedule, a
//     dispatch) is denied, because nobody is there to approve it.
//
// What lands is a template, not a capability: it installs nothing until
// somebody adds it, and adding it goes through the ordinary draft gates
// (credentials disabled, tools pending, connectors unapproved).

const saveTemplateToolName = "save_template"

func init() {
	RegisterReservedToolName(saveTemplateToolName)
	frameworkToolConfirmations[saveTemplateToolName] = &ToolConfirmation{
		Prompt:        "Save this as a template in Admin > Templates? Any administrator can then add it, or export it to share.",
		Scope:         "builder-save-template",
		NeverRemember: true,
	}
}

func saveTemplateToolDef(user string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: saveTemplateToolName,
			Caps: []Capability{CapWrite},
			Description: "Save things already built here (an API credential, its tools, a connector, a skill, an agent) as a TEMPLATE in Admin > Templates, so the integration can be added again or exported to another oddjob. ONLY when the user asks for a template; building an integration does not imply one. " +
				"Each value that differs between deployments (a site address, an account id) becomes a question, asked when the template is added. A credential's SECRET never goes in: a secret question names the credential whose secret it asks for. " +
				"The user is shown the call and approves it before anything is saved. The pieces must exist first; what each needs (the credential a tool uses) comes along.",
			Parameters: map[string]ToolParam{
				"title":       {Type: "string", Description: "The template's title, e.g. \"Acme wiki\"."},
				"description": {Type: "string", Description: "What it sets up, one sentence."},
				"category":    {Type: "string", Description: "(optional) A grouping, e.g. \"Project tracking\"."},
				"setup_notes": {Type: "string", Description: "(optional) Shown when someone adds it: where to get a token, what to enable afterwards."},
				"pieces": {Type: "array", Description: "What goes in, each {type, name}: type is credential, tool, connector, skill, agent or pipeline; name is its name here.",
					Items: &ToolParam{Type: "object"}},
				"questions": {Type: "array", Description: "(optional) Each {name, label, value, help?, kind?, required?} replaces that literal value everywhere in the pieces with the answer. kind: url (https), http_url (a server on the local network), long (multi-line). For a credential's secret: {name, label, secret: true, credential: \"<credential name>\"}.",
					Items: &ToolParam{Type: "object"}},
			},
			Required: []string{"title", "pieces"},
		},
		Confirmation:       frameworkToolConfirmations[saveTemplateToolName],
		SingleFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if nonOwnerRequester(ctx) || !UserIsAdmin(user) {
				return "Not done: saving a template is an administrator's to do. Tell the user it needs an administrator, who can ask you or use Admin > Templates > Save as template.", nil
			}
			title := strings.TrimSpace(stringArg(args, "title"))
			if title == "" {
				return "", fmt.Errorf("title is required")
			}
			sels, err := resolveTemplatePieces(user, args["pieces"])
			if err != nil {
				return "", err
			}
			qs, err := templateQuestionsArg(args["questions"])
			if err != nil {
				return "", err
			}
			rec, err := recipes.Save(RootDB, recipes.Recipe{
				ID: templateSlug(title), Title: title,
				Description: strings.TrimSpace(stringArg(args, "description")),
				Category:    strings.TrimSpace(stringArg(args, "category")),
				SetupNotes:  strings.TrimSpace(stringArg(args, "setup_notes")),
			}, sels, qs, user)
			if err != nil {
				return "", err
			}
			var pieces []string
			for _, a := range rec.Bundle.Artifacts {
				pieces = append(pieces, a.Name+" ("+a.Type+")")
			}
			return fmt.Sprintf("Saved the template %q (id %s): %s, with %d question(s). It is in Admin > Templates, where it can be added or exported. Nothing was installed.",
				rec.Title, rec.ID, strings.Join(pieces, ", "), len(rec.Questions)), nil
		},
	}
}

// resolveTemplatePieces turns {type, name} pairs into artifact selections.
// A name is matched within its type; when several owners hold one by that
// name, the requester's own wins, and otherwise the call is refused with the
// owners named rather than guessing.
func resolveTemplatePieces(user string, raw any) ([]ArtifactSel, error) {
	list, _ := raw.([]any)
	if len(list) == 0 {
		return nil, fmt.Errorf("pieces is required: each {type, name}, e.g. {\"type\": \"tool\", \"name\": \"acme_get_order\"}")
	}
	var sels []ArtifactSel
	for _, item := range list {
		m, _ := item.(map[string]any)
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["type"])))
		name := strings.TrimSpace(fmt.Sprint(m["name"]))
		if m == nil || typ == "" || typ == "<nil>" || name == "" || name == "<nil>" {
			return nil, fmt.Errorf("each piece needs a type and a name, got %v", item)
		}
		var matches []ArtifactSel
		var names []string
		for _, s := range ArtifactSelectionForTypes(RootDB, typ) {
			if s.Type != typ {
				continue
			}
			names = append(names, s.Name)
			if s.Name == name {
				matches = append(matches, s)
			}
		}
		switch {
		case len(matches) == 0 && typ == "tool" && pendingToolNamed(user, name):
			// A tool waiting for review is not in the listing, and saying
			// "no tool at all" about one Builder just wrote sends it off to
			// build a second. A template carries tools that run, so it
			// carries reviewed ones.
			return nil, fmt.Errorf("the tool %q is waiting for an administrator's approval (Admin > Tools, Pending); approve it, then save the template", name)
		case len(matches) == 0:
			sort.Strings(names)
			if len(names) > 25 {
				names = append(names[:25], "…")
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("there is no %s named %q, and no %s at all: build it first", typ, name, typ)
			}
			return nil, fmt.Errorf("there is no %s named %q; the %ss here are: %s", typ, name, typ, strings.Join(names, ", "))
		case len(matches) == 1:
			sels = append(sels, matches[0])
		default:
			var mine *ArtifactSel
			var owners []string
			for i := range matches {
				owners = append(owners, matches[i].Owner)
				if matches[i].Owner == user {
					mine = &matches[i]
				}
			}
			if mine == nil {
				return nil, fmt.Errorf("several %ss are named %q (owned by %s); ask the user which", typ, name, strings.Join(owners, ", "))
			}
			sels = append(sels, *mine)
		}
	}
	return sels, nil
}

// pendingToolNamed reports whether user has a tool of that name waiting for
// review.
func pendingToolNamed(user, name string) bool {
	for _, p := range LoadPendingTempTools(RootDB, user) {
		if p.Tool.Name == name {
			return true
		}
	}
	return false
}

// templateQuestionsArg reads the questions the model wrote.
func templateQuestionsArg(raw any) ([]recipes.SaveQuestion, error) {
	list, _ := raw.([]any)
	var out []recipes.SaveQuestion
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each question is an object, got %v", item)
		}
		str := func(k string) string {
			if v, ok := m[k].(string); ok {
				return strings.TrimSpace(v)
			}
			return ""
		}
		yes := func(k string) bool {
			switch v := m[k].(type) {
			case bool:
				return v
			case string:
				return strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
			}
			return false
		}
		name := str("name")
		if name == "" {
			return nil, fmt.Errorf("each question needs a name, got %v", item)
		}
		label := str("label")
		if label == "" {
			label = name
		}
		q := recipes.SaveQuestion{Question: recipes.Question{
			Name: name, Label: label, Help: str("help"), Kind: str("kind"),
			Required: yes("required"), Secret: yes("secret"), Credential: str("credential"),
		}}
		if !q.Secret {
			q.Value = str("value")
			if q.Value == "" {
				return nil, fmt.Errorf("question %q needs the value it replaces in the pieces (or secret: true and the credential it asks for)", name)
			}
		}
		out = append(out, q)
	}
	return out, nil
}

// templateSlug makes a template id from its title: lowercase letters, digits
// and dashes.
func templateSlug(title string) string {
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
