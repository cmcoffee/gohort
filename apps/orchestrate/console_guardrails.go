package orchestrate

// The fleet-wide guardrail review.
//
// The per-agent log answers "what has THIS agent been stopped from doing",
// which is the right question while you are editing that agent's rules. It is
// the wrong question for "is anything being blocked that shouldn't be" — that
// one is about the fleet, and answering it by opening every agent's Rules modal
// in turn is how it stops being answered at all.
//
// So the same records, collated across the owner's agents, newest first, in the
// console beside the other fleet views. Read-only: a block already happened,
// and there is nothing to do to it from here except go and look at the rule.

import (
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// consoleGuardrailRow is one block, flattened for the card layout.
type consoleGuardrailRow struct {
	// Name is the card title: the rule, because that is what the reader is
	// scanning for. The agent goes on the detail line — a rule blocking on the
	// wrong agent is visible either way, and a list titled by agent buries the
	// one rule doing all the work among its siblings.
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	Where  string `json:"where,omitempty"`  // hook + surface + contact
	Reason string `json:"reason,omitempty"` // what the check objected to
	At     string `json:"at"`               // RFC3339; the console renders it
	ID     string `json:"_id"`              // hidden — agent id, so a future row action has a target
}

// consoleGuardrailLimit caps the collated list. Long enough to show a pattern
// across a fleet, short enough that the view stays scannable; the per-agent log
// keeps more, and is where you go once you know which agent to look at.
const consoleGuardrailLimit = 60

// handleConsoleGuardrails lists recent guardrail blocks across every agent the
// user owns.
//
// Scoped to the requesting user's own store, like every other console view, so
// one user can never read another's blocks — and a block record names a rule
// and a contact, which is exactly the pairing that must not leak sideways.
func (T *OrchestrateApp) handleConsoleGuardrails(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// An agent filter, so the same endpoint backs a per-agent view if one is
	// ever wanted. Absent = the whole fleet, which is the point of this view.
	only := strings.TrimSpace(r.URL.Query().Get("agent"))

	type entry struct {
		block GuardrailBlock
		agent AgentRecord
	}
	var all []entry
	for _, a := range listAgents(udb, user) {
		if only != "" && a.ID != only {
			continue
		}
		for _, b := range listGuardrailBlocks(udb, a.ID, consoleGuardrailLimit) {
			all = append(all, entry{block: b, agent: a})
		}
	}
	// Newest first across the whole fleet — the per-agent lists arrive already
	// ordered, but interleaving them is the entire job of this view.
	sort.SliceStable(all, func(i, j int) bool { return all[i].block.At.After(all[j].block.At) })
	if len(all) > consoleGuardrailLimit {
		all = all[:consoleGuardrailLimit]
	}

	rows := []consoleGuardrailRow{}
	for _, e := range all {
		rows = append(rows, consoleGuardrailRow{
			Name:   guardrailRowTitle(e.block.Rule),
			Agent:  chFirst(e.agent.Name, e.agent.ID),
			Where:  guardrailRowWhere(e.block),
			Reason: e.block.Reason,
			At:     e.block.At.Format(time.RFC3339),
			ID:     e.agent.ID,
		})
	}
	writeJSON(w, rows)
}

// guardrailRowTitle renders the rule as a card title, trimmed so one long rule
// doesn't push everything else off the card.
func guardrailRowTitle(rule string) string {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return "(rule not recorded)"
	}
	// Counted and cut in RUNES, not bytes. A rule is prose an owner typed, so
	// it can hold anything — a byte slice lands mid-character and renders the
	// title as a replacement glyph, which reads as corruption rather than as a
	// trim.
	const max = 90
	r := []rune(rule)
	if len(r) <= max {
		return rule
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// guardrailRowWhere collapses hook + surface + contact into the detail line.
// The contact's name is SELF-REPORTED (they choose it) and is shown as what it
// is rather than as an identification.
func guardrailRowWhere(b GuardrailBlock) string {
	parts := make([]string, 0, 4)
	if h := strings.TrimSpace(b.Hook); h != "" {
		parts = append(parts, h)
	}
	// The tool comes right after the hook, because on a tool_result row it is
	// the thing being identified: "which feed carried it" is the first question
	// a detection raises and the one that decides what to do about it.
	if tl := strings.TrimSpace(b.Tool); tl != "" {
		parts = append(parts, tl)
	}
	if c := strings.TrimSpace(b.Channel); c != "" {
		parts = append(parts, c)
	}
	if s := strings.TrimSpace(b.Sender); s != "" {
		parts = append(parts, "from "+s)
	}
	return strings.Join(parts, " · ")
}
