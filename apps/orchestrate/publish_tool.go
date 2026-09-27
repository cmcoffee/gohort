// The publish tool: how an agent publishes to one of its owner's publishing
// targets (apps/publish, targets.go). Offered only to an agent the owner
// switched on for at least one target, and it names only those, with each
// target's questions, so "publish this to the blog" is one call.
package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

func (t *chatTurn) publishToolDef() (AgentToolDef, bool) {
	_, owner := t.ownerView()
	if strings.TrimSpace(owner) == "" {
		return AgentToolDef{}, false
	}
	var mine []docs.PublishTargetSpec
	for _, s := range docs.PublishTargetSpecs(context.Background(), owner) {
		for _, a := range s.Agents {
			if a == t.agent.ID {
				mine = append(mine, s)
				break
			}
		}
	}
	if len(mine) == 0 {
		return AgentToolDef{}, false
	}
	var ids []string
	var b strings.Builder
	for _, s := range mine {
		ids = append(ids, s.Target.ID)
		fmt.Fprintf(&b, "\n- %s: %s", s.Target.ID, s.Target.Title)
		if s.Target.Desc != "" {
			b.WriteString(" (" + s.Target.Desc + ")")
		}
		var asks []string
		for _, f := range s.Fields {
			a := f.Name
			if len(f.Options) > 0 {
				a += " [" + strings.Join(f.Options, " | ") + "]"
			}
			if f.Required {
				a += " required"
			}
			asks = append(asks, a)
		}
		if len(asks) > 0 {
			b.WriteString(". answers: " + strings.Join(asks, ", "))
		}
	}
	return AgentToolDef{
		Tool: Tool{
			Name: "publish",
			Description: "Publish a finished piece of writing to one of the places your owner set up. The place's own instruction says how it is posted; you supply the text, a title, and its answers. It returns where it landed. Only when the user asked for it to be published there." +
				"\nPlaces:" + b.String(),
			Parameters: map[string]ToolParam{
				"target":  {Type: "string", Enum: ids, Description: "The place to publish to."},
				"title":   {Type: "string", Description: "The title it goes up under."},
				"content": {Type: "string", Description: "The whole text to publish, in markdown."},
				"answers": {Type: "object", Description: "The place's questions, by name, e.g. {\"category\": \"News\"}. Required ones must be given."},
			},
			Required: []string{"target", "title", "content"},
			Caps:     []Capability{CapWrite, CapNetwork},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(stringArg(args, "target"))
			var spec docs.PublishTargetSpec
			for _, s := range mine {
				if s.Target.ID == id {
					spec = s
				}
			}
			if spec.Kind == "" {
				return "", fmt.Errorf("%q is not a place you may publish to; places: %s", id, strings.Join(ids, ", "))
			}
			answers := map[string]string{}
			if m, ok := args["answers"].(map[string]any); ok {
				for k, v := range m {
					answers[k] = strings.TrimSpace(fmt.Sprint(v))
				}
			}
			if missing := docs.MissingAnswers(spec.Fields, answers); len(missing) > 0 {
				return "", fmt.Errorf("%s needs: %s", spec.Target.Title, strings.Join(missing, ", "))
			}
			title := strings.TrimSpace(stringArg(args, "title"))
			content := strings.TrimSpace(stringArg(args, "content"))
			if content == "" {
				return "", fmt.Errorf("content is empty: pass the whole text to publish")
			}
			res, err := docs.PublishDocument(ctx, owner, spec.Kind, docs.PublishRequest{
				Target: spec.Target.ID, Title: title, Answers: answers,
				Doc: docs.PublishDoc{Title: title, Markdown: content, SourceKind: "agent", SourceID: t.agent.ID},
			})
			if err != nil {
				return "", err
			}
			out := "Published to " + spec.Target.Title + "."
			if res.URL != "" {
				out += " It is at " + res.URL
			}
			if res.Label != "" {
				out += "\n" + res.Label
			}
			return out, nil
		},
	}, true
}
