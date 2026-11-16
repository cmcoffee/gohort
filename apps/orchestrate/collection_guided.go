package orchestrate

// A collection created from answers: Knowledge's guided "+ New collection"
// (docs/design/knowledge-curation.md).
//
// The answers are not stored. They become what a collection already has: its
// description, which Auto-fill, research and the curator all read, so what the
// person said the collection is for keeps steering what is found; its filter
// rules, which steer the searches and turn away pages; its curator; and the
// agents it is attached to. A collection made this way is the same shape as
// one made from the plain form.

import (
	"encoding/json"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// guidedCollection is the guided setup's answers, every one optional.
type guidedCollection struct {
	// About is the person's own words for what it is for. The description,
	// drafted from it and edited, is what is kept; About stands in when
	// there is none.
	About string `json:"about"`
	// Currency is how current it has to be: current, recent or any.
	Currency string `json:"currency"`
	// Depth is overview or comprehensive.
	Depth string `json:"depth"`
	// Mostly is where its material comes from: web, files or both.
	Mostly string `json:"mostly"`
	// Prefer and Avoid are sources to favour and to leave out.
	Prefer json.RawMessage `json:"prefer"`
	Avoid  json.RawMessage `json:"avoid"`
	// Curation is how it is kept: hand, auto or agent (chooseCurator).
	// CuratorAgent is the person's pick for Curation by Agent.
	Curation     string `json:"curation"`
	CuratorAgent string `json:"curator_agent"`
	// Guided marks a request from the guided setup, whose switches apply.
	Guided json.RawMessage `json:"guided"`
	// Start is what the guided setup starts as it creates: "suggest" (or
	// nothing chosen) searches for sources to tick, "plan" drafts its plan, "none" starts nothing.
	Start string `json:"start"`
	// AttachTo is the person's agents that should read it.
	AttachTo json.RawMessage `json:"attach_to"`
}

var guidedCurrency = map[string]string{
	"current": "Current material matters most: prefer the latest, and treat older pages as likely out of date.",
	"recent":  "Recent material is enough; it need not be the very latest.",
}

var guidedDepth = map[string]string{
	"overview":      "Aim for a solid overview of the essentials rather than everything.",
	"comprehensive": "Aim to be comprehensive.",
}

var guidedMostly = map[string]string{
	"web":   "Its material comes mostly from the web.",
	"files": "Its material is mostly the owner's own files, uploaded rather than found.",
	"both":  "Its material comes from the web and from the owner's own files.",
}

// guidedAnswered reports whether any answer beyond a name and a description
// was given, so the plain form's create is left exactly as it was.
func (g guidedCollection) guidedAnswered() bool {
	return jsonTrue(g.Guided) || g.About != "" || g.Currency != "" || g.Depth != "" || g.Mostly != "" ||
		len(decodeWizardPicks(g.Prefer)) > 0 || len(decodeWizardPicks(g.Avoid)) > 0 || g.Curation != ""
}

// description is the collection's description from the answers: the
// description they settled on (drafted from their words), or their words
// when they settled on none, then a line for each answer.
func (g guidedCollection) description(given string) string {
	var parts []string
	if base := chFirst(strings.TrimSpace(given), strings.TrimSpace(g.About)); base != "" {
		parts = append(parts, base)
	}
	if s := guidedCurrency[g.Currency]; s != "" {
		parts = append(parts, s)
	}
	if s := guidedDepth[g.Depth]; s != "" {
		parts = append(parts, s)
	}
	if s := guidedMostly[g.Mostly]; s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// filterRules is one rule per source to prefer or leave out, the shape the
// collection page's rule list keeps.
func (g guidedCollection) filterRules() string {
	var rules []string
	for _, s := range decodeWizardPicks(g.Prefer) {
		if s = strings.TrimSpace(s); s != "" {
			rules = append(rules, "Prefer "+s)
		}
	}
	for _, s := range decodeWizardPicks(g.Avoid) {
		if s = strings.TrimSpace(s); s != "" {
			rules = append(rules, "Skip "+s)
		}
	}
	return strings.Join(rules, "\n")
}

// curator is the agent to put in charge, or "" for hand curated. A choice
// that does not resolve leaves it hand curated: at creation nothing is stored
// that the collection page would then report as missing.
func (g guidedCollection) curator(udb Database, user string) string {
	if g.Curation == "" {
		return ""
	}
	curator, _, err := chooseCurator(udb, user, g.Curation, g.CuratorAgent)
	if err != nil {
		return ""
	}
	return curator
}

// starts is what to start as the collection is created: for the guided
// setup, its "To start with" choice, Suggest sources when none was made; for
// the plain form, nothing.
func (g guidedCollection) starts() string {
	if !jsonTrue(g.Guided) {
		return ""
	}
	switch g.Start {
	case "plan", "none":
		return g.Start
	}
	return "suggest"
}

// jsonTrue reads a switch sent as true, or as "true" by a client that
// stringified it.
func jsonTrue(raw json.RawMessage) bool {
	return strings.Trim(strings.TrimSpace(string(raw)), `"`) == "true"
}

// attach adds the collection to each of the person's own agents named in
// AttachTo, and returns how many it was added to. An agent that is not theirs,
// or not found, is passed over.
func (g guidedCollection) attach(udb Database, user, collectionID string) int {
	n := 0
	for _, id := range decodeWizardPicks(g.AttachTo) {
		a, ok := loadAgent(udb, strings.TrimSpace(id))
		if !ok || a.Owner != user {
			continue
		}
		already := false
		for _, cid := range a.AttachedCollections {
			if cid == collectionID {
				already = true
				break
			}
		}
		if already {
			continue
		}
		a.AttachedCollections = append(a.AttachedCollections, collectionID)
		if _, err := saveAgent(udb, a); err != nil {
			Log("[orchestrate.collections] attach %s to agent=%s: %v", collectionID, a.ID, err)
			continue
		}
		n++
	}
	return n
}
