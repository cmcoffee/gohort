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
	"strconv"
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
	// Kind is what sort of collection it is (collectionKinds): reference,
	// topic or files set its schedule, documents per run, a rule and what
	// starts; custom takes Refresh, DocsPerRun and Start as answered.
	Kind    string `json:"kind"`
	Refresh string `json:"refresh"`
	// DocsPerRun is raw: a form sends a number it was given as a string
	// when the field was left at its default.
	DocsPerRun json.RawMessage `json:"docs_per_run"`
	// Curation is the older answer for how it is kept: "auto" runs its plan
	// every week. Read only when no Kind was given.
	Curation string `json:"curation"`
	// Guided marks a request from the guided setup, whose switches apply.
	Guided json.RawMessage `json:"guided"`
	// Start is what the guided setup starts as it creates: "suggest" (or
	// nothing chosen) searches for sources to tick, "plan" drafts its plan, "none" starts nothing.
	Start string `json:"start"`
	// AttachTo is the person's agents that should read it.
	AttachTo json.RawMessage `json:"attach_to"`
}

// collectionKind is what a kind of collection starts with. Each is only the
// first values of settings every collection has, all of them changed on the
// collection's page afterwards; nothing records which kind it was.
type collectionKind struct {
	Refresh    string // "week", "month", or "" for no schedule
	DocsPerRun int
	Rule       string // one filter rule, or ""
	Start      string // "plan" or "none"
}

// collectionKinds are the guided setup's kinds (docs/design/knowledge-curation.md).
// Reference is the standard practice: fill it from a plan you check, then
// look each month for what changed. A topic moves faster and is about what is
// new. Files are uploaded, never searched for.
var collectionKinds = map[string]collectionKind{
	"reference": {Refresh: "month", DocsPerRun: 20, Start: "plan",
		Rule: "Prefer the official documentation and the maker's or standards body's own pages"},
	"topic": {Refresh: "week", DocsPerRun: 10, Start: "plan",
		Rule: "Skip pages more than a year old"},
	"files": {Start: "none"},
}

// kind is the chosen kind's starting values, and whether a preset kind
// applies (custom does not). The guided setup with no kind picked is the
// usual kind: Reference where the server can search the web, My own files
// where it cannot, since then nothing could look for material. A request
// that is not the guided setup, or names the older curation answer, picks
// no kind.
func (g guidedCollection) kind() (collectionKind, bool) {
	name := strings.TrimSpace(g.Kind)
	if name == "" && jsonTrue(g.Guided) && strings.TrimSpace(g.Curation) == "" {
		name = "files"
		if WebSearchAvailable() {
			name = "reference"
		}
	}
	k, ok := collectionKinds[name]
	return k, ok
}

// docsPerRun is how many documents a run adds to start with: the kind's, a
// custom answer held to 1 to 100, or 0 to leave the plan's default.
func (g guidedCollection) docsPerRun() int {
	if k, ok := g.kind(); ok {
		return k.DocsPerRun
	}
	if strings.TrimSpace(g.Kind) != "custom" {
		return 0
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(g.DocsPerRun)), `"`))
	if err != nil || n <= 0 {
		return 0
	}
	return min(n, 100)
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
		len(decodeWizardPicks(g.Prefer)) > 0 || len(decodeWizardPicks(g.Avoid)) > 0 || g.Curation != "" || g.Kind != ""
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
	if k, ok := g.kind(); ok && k.Rule != "" {
		rules = append(rules, k.Rule)
	}
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

// autoCuration is the schedule Auto-Curation starts on: the kind's, a custom
// answer of week or month, or every week for the older "auto"; "" for none.
func (g guidedCollection) autoCuration() string {
	if k, ok := g.kind(); ok {
		return k.Refresh
	}
	if strings.TrimSpace(g.Kind) == "custom" {
		switch r := strings.TrimSpace(g.Refresh); r {
		case "week", "month":
			return r
		}
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(g.Curation), "auto") {
		return "week"
	}
	return ""
}

// starts is what to start as the collection is created: for the guided
// setup, its "To start with" choice, Suggest sources when none was made; for
// the plain form, nothing.
func (g guidedCollection) starts() string {
	if !jsonTrue(g.Guided) {
		return ""
	}
	if k, ok := g.kind(); ok {
		return k.Start
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
