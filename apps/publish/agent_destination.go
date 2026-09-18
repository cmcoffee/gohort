// A destination that is an AGENT rather than an endpoint.
//
// Confluence and the webhook both answer "where does this go" with a URL. Some
// deployments cannot: publishing there means filing a ticket in the house
// format, opening a pull request against a docs repo, or handing the thing to
// whoever owns that area. There is no endpoint for that, only a job
// description — and the thing that already carries out a job description is an
// agent.
//
// The interesting half is the PHRASING. A destination is not just an address,
// it is how the request should be put: the same document goes to the tickets
// agent as "file this as a documentation task" and to the review agent as
// "open a PR adding this page and request review from the area owner". So each
// configured destination owns a prompt, and that prompt is the destination.
//
// Several of these can exist at once, unlike Confluence and the webhook, which
// are one per deployment. That asymmetry is the point rather than an
// inconsistency: the reason to have this kind at all is to have SEVERAL named
// places, and each one is a label plus an agent plus a sentence, with no
// credential or endpoint to name twice.
package publish

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// AgentKindPrefix namespaces the kinds these destinations register under, so a
// configured destination called "tickets" is kind "agent:tickets" and cannot
// collide with a built-in kind.
const AgentKindPrefix = "agent:"

// AgentDestination is one configured agent-backed destination.
type AgentDestination struct {
	// Slug is the stable part of the kind. Renaming the Label leaves published
	// records still pointing at the right destination; changing the Slug does
	// not, which is why the two are separate fields.
	Slug string `json:"slug"`
	// Label is what a person picks from the Publish dialog ("File a ticket").
	Label string `json:"label"`
	// Agent names the agent to hand the document to.
	Agent string `json:"agent"`
	// Prompt is how this destination phrases the job. The document is appended
	// after it, so the prompt is an instruction and not a template with the
	// body wedged into the middle of it. Placeholders {title} and {target} are
	// substituted where they appear.
	Prompt string `json:"prompt"`
	// Targets is an optional fixed list of places within this destination
	// (queues, repositories, areas). Empty means the destination takes no
	// target, and the agent is simply asked to publish.
	Targets []AgentDestinationTarget `json:"targets,omitempty"`
}

// AgentDestinationTarget is one choice inside an agent destination.
type AgentDestinationTarget struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Desc  string `json:"desc,omitempty"`
}

// agentDest adapts one configured AgentDestination to the registry.
type agentDest struct {
	app  *PublishApp
	slug string
}

func (d *agentDest) Kind() string { return AgentKindPrefix + d.slug }

func (d *agentDest) spec() (AgentDestination, bool) {
	for _, a := range d.app.config().Agents {
		if a.Slug == d.slug {
			return a, true
		}
	}
	return AgentDestination{}, false
}

func (d *agentDest) Label() string {
	if a, ok := d.spec(); ok {
		if l := strings.TrimSpace(a.Label); l != "" {
			return l
		}
	}
	return d.slug
}

func (d *agentDest) Available(user string) (bool, string) {
	a, ok := d.spec()
	if !ok {
		return false, "this destination is no longer configured: an admin sets it up in Admin > Publishing"
	}
	if strings.TrimSpace(a.Agent) == "" {
		return false, "no agent is named for this destination: an admin sets one in Admin > Publishing"
	}
	// Said as a REASON rather than discovered at the moment of publishing: a
	// destination that is going to fail should say so while somebody is still
	// choosing, not after they have picked it.
	if !docs.AgentPublisherReady() {
		return false, "this deployment cannot hand a document to an agent"
	}
	return true, ""
}

// Targets returns the destination's configured choices. An empty list is the
// registry's signal that this destination takes no target, which is the normal
// case: most agent destinations are a single job.
func (d *agentDest) Targets(ctx context.Context, user string) ([]docs.PublishTarget, error) {
	a, ok := d.spec()
	if !ok {
		return nil, nil
	}
	out := make([]docs.PublishTarget, 0, len(a.Targets))
	for _, t := range a.Targets {
		if strings.TrimSpace(t.ID) == "" {
			continue
		}
		title := strings.TrimSpace(t.Title)
		if title == "" {
			title = t.ID
		}
		out = append(out, docs.PublishTarget{ID: t.ID, Title: title, Desc: t.Desc, Group: a.Label})
	}
	return out, nil
}

// Publish hands the document to the destination's agent and records what it
// said it did.
//
// There is no ExternalID or Version to return. This kind cannot honestly claim
// a remote id — the agent may have filed a ticket, opened a pull request or
// done nothing at all, and inventing an id would make the NEXT publish read as
// an update of something the framework never saw. A producer therefore gets a
// fresh publish each time, which is the truthful behaviour.
func (d *agentDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	a, ok := d.spec()
	if !ok {
		return docs.PublishResult{}, fmt.Errorf("this destination is no longer configured")
	}
	said, err := docs.PublishViaAgent(ctx, user, a.Agent, agentInstruction(a, req))
	if err != nil {
		return docs.PublishResult{}, err
	}
	Log("[publish.agent] user=%q destination=%q agent=%q published %q",
		user, d.slug, a.Agent, req.Title)
	// The agent's own account of what it did becomes the "where it went" line,
	// because it is the only account that exists. Clipped to one line: an agent
	// reports in prose and a label is a row in a table.
	label := a.Label
	if note := clipToLabel(said); note != "" {
		if label != "" {
			label += " · " + note
		} else {
			label = note
		}
	}
	return docs.PublishResult{Label: label}, nil
}

// clipToLabel reduces an agent's reply to something that fits a label.
func clipToLabel(s string) string {
	s = firstLine(s)
	const max = 160
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// agentInstruction builds what the agent is asked to do: the destination's own
// phrasing, then the document.
//
// The document goes LAST and whole. An instruction that interpolates the body
// into the middle of a sentence buries it, and a body that arrives after the
// instruction reads the way an attachment does.
func agentInstruction(a AgentDestination, req docs.PublishRequest) string {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Doc.Title)
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		prompt = "Publish this document."
	}
	prompt = strings.ReplaceAll(prompt, "{title}", title)
	prompt = strings.ReplaceAll(prompt, "{target}", strings.TrimSpace(req.Target))

	var b strings.Builder
	b.WriteString(prompt)
	if t := strings.TrimSpace(req.Target); t != "" && !strings.Contains(a.Prompt, "{target}") {
		b.WriteString("\n\nWhere: " + t)
	}
	b.WriteString("\n\nTitle: " + title)
	b.WriteString("\n\n---\n\n")
	b.WriteString(req.Doc.Markdown)
	return b.String()
}

// registerAgentDestinations registers one destination per configured entry.
// Called at startup and again whenever the config is saved, because a
// destination added in the admin form has to appear without a restart.
func (T *PublishApp) registerAgentDestinations() {
	seen := map[string]bool{}
	for _, a := range T.config().Agents {
		slug := strings.TrimSpace(a.Slug)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		docs.RegisterPublishDestination(&agentDest{app: T, slug: slug})
	}
}

// normalizeAgentDestinations cleans what an admin form sends: trimmed fields,
// no blank slugs, no duplicate slugs.
//
// A duplicate slug is dropped rather than merged or renamed, because the slug
// is what already-published records point at: quietly renaming one would leave
// those records naming a destination that no longer exists, and merging two
// would send a document to whichever agent happened to be second in the list.
func normalizeAgentDestinations(in []AgentDestination) []AgentDestination {
	out := make([]AgentDestination, 0, len(in))
	seen := map[string]bool{}
	for _, a := range in {
		a.Slug = strings.TrimSpace(a.Slug)
		a.Label = strings.TrimSpace(a.Label)
		a.Agent = strings.TrimSpace(a.Agent)
		a.Prompt = strings.TrimSpace(a.Prompt)
		if a.Slug == "" || seen[a.Slug] {
			continue
		}
		seen[a.Slug] = true
		targets := make([]AgentDestinationTarget, 0, len(a.Targets))
		for _, t := range a.Targets {
			t.ID = strings.TrimSpace(t.ID)
			t.Title = strings.TrimSpace(t.Title)
			if t.ID == "" {
				continue
			}
			targets = append(targets, t)
		}
		a.Targets = targets
		out = append(out, a)
	}
	return out
}
