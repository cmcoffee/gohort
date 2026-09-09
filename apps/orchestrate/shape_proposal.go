package orchestrate

import (
	"strings"
)

// The opening proposal: what a creation dialog shows first.
//
// A person who has never built an agent cannot answer "what should its purpose
// be?", and asking anyway is how the wizard ends up collecting a restatement
// of its own question. So the dialog does not open with a question. It matches
// what they typed to a shape, instantiates it, and shows them the agent that
// would be: what it does, what it can reach, what it will refuse. Then it asks
// the two or three things whose answers actually change the build.
//
// Everything here is derived from the record the shape ships, not written per
// shape. A hand-written blurb per shape is a second description of the same
// agent, which is the duplication the shape files just finished collapsing;
// it would also go stale the first time a tool was added to an allowlist.

// shapeProposal is a draft agent, with the plain-language account of it that a
// person reads before deciding anything.
type shapeProposal struct {
	Shape   string   `json:"shape"`
	Matched string   `json:"matched,omitempty"` // the phrase that chose this shape
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Points  []string `json:"points"`
	Asks    []string `json:"asks,omitempty"`

	// NeedsComposing marks a shape that ships no agent, because its subject is
	// not known yet: a watcher has to be told what to watch before there is
	// anything to run. There is no draft to look at, so the dialog opens with
	// what the shape IS and the questions it needs answered, and Builder
	// composes from the recipe once they are answered.
	NeedsComposing bool `json:"needs_composing,omitempty"`

	// Draft is the agent that would be created. Unsaved: a proposal is
	// something to react to, and creating on sight would leave a fleet full of
	// agents nobody agreed to.
	Draft AgentRecord `json:"-"`
}

// proposeAgent turns a typed request into an opening proposal, or reports that
// it has nothing worth proposing.
//
// Declining is a real answer. Opening with the wrong agent costs more than
// opening with a question, because the first screen is what tells someone
// whether this understood them.
func proposeAgent(request string) (shapeProposal, bool) {
	m, ok := matchShape(request)
	if !ok {
		return shapeProposal{}, false
	}
	// A shape with no record describes an agent whose subject is not known
	// yet. There is no draft to show, but knowing WHICH shape is most of the
	// value: the dialog can say what this kind of agent is, ask the questions
	// the recipe says matter, and hand the answers to Builder. Returning
	// nothing here would throw that away and start with a blank page.
	if m.Shape.Record == nil {
		return shapeProposal{
			Shape:          m.Shape.Slug,
			Matched:        m.Phrase,
			Name:           shapeTitle(m.Shape.Slug),
			Summary:        m.Shape.Summary,
			Asks:           m.Shape.Asks,
			NeedsComposing: true,
		}, true
	}
	draft, ok := shapeBaseRecord(m.Shape.Slug)
	if !ok {
		return shapeProposal{}, false
	}
	draft.ID = ""
	draft.Owner = ""
	draft.Hidden = false
	draft.Exposed = false
	draft.Fleet = false // the conductor toolset is a deliberate choice, not a default
	draft.ShapeID = m.Shape.Slug

	return shapeProposal{
		Shape:   m.Shape.Slug,
		Matched: m.Phrase,
		Name:    draft.Name,
		Summary: strings.TrimSpace(draft.Description),
		Points:  describeAgentPlainly(draft),
		Asks:    m.Shape.Asks,
		Draft:   draft,
	}, true
}

// shapeTitle is what to call a shape that ships no agent, since there is no
// record to take a name from. Derived from the slug rather than scraped from
// the recipe's heading: a heading is prose and may be phrased for a reader,
// while the slug is the shape's identity.
func shapeTitle(slug string) string {
	words := strings.Split(strings.ReplaceAll(slug, "_", " "), " ")
	for i, w := range words {
		if i == 0 && w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// describeAgentPlainly says what an agent does in the terms someone deciding
// about it cares about: what it can reach, what it refuses, what it remembers.
//
// Ordered by what a person checks first. Reach comes before habits, because
// "can it see the internet" is the question people actually ask, and a refusal
// is worth more than a capability: it is the part they cannot see by using it
// for five minutes.
func describeAgentPlainly(a AgentRecord) []string {
	var out []string

	if a.ForcePrivate {
		out = append(out, "Stays offline: no web, no other agents, nothing but what you give it.")
	}
	reach := reachableTools(a.AllowedTools)
	switch {
	case isNoToolsList(a.AllowedTools):
		out = append(out, "Uses no tools; it answers from the conversation.")
	case len(a.AllowedTools) == 0:
		if !a.ForcePrivate {
			out = append(out, "Reaches the standard set of tools, including web search and fetching pages.")
		}
	case len(reach) == 0:
		// "Stays offline" already said this, and saying it twice buries the
		// line that follows about what it CAN answer from.
		if !a.ForcePrivate {
			out = append(out, "Reaches nothing outside the conversation.")
		}
	default:
		out = append(out, "Reaches "+joinWords(reach)+".")
	}

	for _, rule := range strings.Split(a.Rules, "\n") {
		if r := strings.TrimSpace(rule); r != "" {
			out = append(out, r)
		}
	}

	switch {
	case a.DisableExplicit && a.DisableInferred:
		out = append(out, "Keeps no memory of you between conversations.")
	case a.IngestAttachments:
		out = append(out, "Files you upload become part of what it can answer from.")
	}
	if a.Cortex {
		out = append(out, "Keeps one ongoing thread, so scheduled work and alerts land somewhere you can read them.")
	}
	if a.GapCheck {
		out = append(out, "Checks its own answer covers the question before finishing.")
	}
	return out
}

// conversationMechanics are tools that are how an agent TALKS rather than what
// it can reach: asking a question, laying out a plan, choosing to stay quiet.
// Listing them as reach tells a person nothing they wanted to know and buries
// the one line that does, which is whether it can see the internet.
var conversationMechanics = map[string]bool{
	"ask_user":      true,
	"ask_user_form": true,
	"plan_set":      true,
	"stay_silent":   true,
	"keep_going":    true,
}

// reachableTools drops the mechanics, leaving what an agent can actually get at.
func reachableTools(tools []string) []string {
	var out []string
	for _, t := range tools {
		if name := strings.TrimSpace(t); name != "" && !conversationMechanics[name] {
			out = append(out, name)
		}
	}
	return out
}

// isNoToolsList reports the explicit no-tools sentinel, which means something
// different from an empty list: empty grants the default pool.
func isNoToolsList(tools []string) bool {
	return len(tools) == 1 && strings.TrimSpace(tools[0]) == noToolsSentinel
}
