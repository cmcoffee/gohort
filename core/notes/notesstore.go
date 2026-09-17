// Operating notes — a single bounded, agent-rewritable block of RUNNING STATE
// for one (user, agent). Always-in-prompt like Explicit Memory facts, but a
// rewritable DOCUMENT rather than an append-list: the agent re-states a compact,
// current note each time instead of accumulating bullets.
//
// Distinct from the other memory layers:
//   - store_fact (Explicit Memory) — append-list of DURABLE atomic rules.
//   - memory_save (Reference Memory) — semantic, pull-only recall.
//   - operating notes — one always-in-prompt blob of TRANSIENT state.
//
// Storage: kvlite table "core_notes", one row per namespace, in the caller's
// per-user sub-store — so per-(user, agent) isolation matches the fact layer
// (same "agent:<id>" namespace, different table).
package notes

import (
	"strings"
	"time"
)

// OperatingNotesTable is the kvlite table holding one OperatingNotes row per
// namespace.
const OperatingNotesTable = "core_notes"

// OperatingNotesCap bounds the always-in-prompt notes blob (in runes). The cap
// IS the feature: it forces the agent to COMPRESS its running state on every
// rewrite rather than hoard, keeping the per-turn prompt cost fixed.
const OperatingNotesCap = 1500

// operatingNotesHistoryDepth is how many prior versions the ring keeps for
// owner audit / revert.
const operatingNotesHistoryDepth = 3

// OperatingNotes is the rewritable working-notes block for one (user, agent).
type OperatingNotes struct {
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updated_at"`
	History   []string  `json:"history,omitempty"` // prior versions, newest first, capped
}

// LoadOperatingNotes returns the stored notes for a namespace (zero value when
// none exist or db/namespace is empty).
func LoadOperatingNotes(db Store, namespace string) OperatingNotes {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return OperatingNotes{}
	}
	var n OperatingNotes
	db.Get(OperatingNotesTable, namespace, &n)
	return n
}

// SaveOperatingNotes REPLACES the notes text wholesale (rewrite-document
// semantics), enforcing the cap and pushing the prior text onto a bounded
// history ring. An empty text clears the notes (row removed). Returns the
// stored value and overCap=true when text exceeds the cap (nothing written).
func SaveOperatingNotes(db Store, namespace, text string) (OperatingNotes, bool) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return OperatingNotes{}, false
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) > OperatingNotesCap {
		return LoadOperatingNotes(db, namespace), true
	}
	prev := LoadOperatingNotes(db, namespace)
	if text == prev.Text {
		return prev, false
	}
	if text == "" {
		db.Unset(OperatingNotesTable, namespace)
		return OperatingNotes{}, false
	}
	next := OperatingNotes{Text: text, UpdatedAt: time.Now()}
	if strings.TrimSpace(prev.Text) != "" {
		next.History = append([]string{prev.Text}, prev.History...)
		if len(next.History) > operatingNotesHistoryDepth {
			next.History = next.History[:operatingNotesHistoryDepth]
		}
	}
	db.Set(OperatingNotesTable, namespace, next)
	return next, false
}

// ResolveOperatingNotes returns the stored notes, or — when none are stored yet
// and a seed is provided — an EPHEMERAL note carrying the seed text. The seed is
// NOT persisted here (no write side effect in prompt-assembly paths); it renders
// until the agent's first update_notes call overwrites it, at which point the
// record's seed remains the durable fallback.
func ResolveOperatingNotes(db Store, namespace, seed string) OperatingNotes {
	n := LoadOperatingNotes(db, namespace)
	if strings.TrimSpace(n.Text) == "" && strings.TrimSpace(seed) != "" {
		// The seed honors the same cap update_notes enforces — it's
		// Builder/wizard-supplied, not agent-written, and previously bypassed
		// the bound entirely (an oversized seed inflated every prompt until
		// the first update_notes replaced it).
		s := strings.TrimSpace(seed)
		if r := []rune(s); len(r) > OperatingNotesCap {
			s = strings.TrimSpace(string(r[:OperatingNotesCap]))
		}
		return OperatingNotes{Text: s}
	}
	return n
}

// RenderOperatingNotesBlock returns the always-in-prompt markdown block, or ""
// when empty. Framed as ADVISORY notes under the persona — never instructions —
// so a self-authored note can't override the agent's system-prompt constraints.
//
// The opening line carries the whole boundary against the fact layer: facts are
// what stays true, notes are where you are. Both layers sit in the same prompt,
// so the difference is not what they hold but what it costs to REPLACE — a fact
// is superseded by a judge, a note is overwritten for free — and an agent that
// cannot state the difference in one line will put running state in facts and
// durable rules in notes.
func RenderOperatingNotesBlock(n OperatingNotes) string {
	if strings.TrimSpace(n.Text) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Working notes\n\n")
	// The boundary, in one line the model can hold. It was three sentences
	// hedged against the fact layer, and a distinction that needs a paragraph
	// to state is one the reader has to re-derive every turn. Facts are what
	// stays true; notes are where you are. Everything below is not definition
	// — it is the advisory guardrail, the mechanics, and one failure — so it
	// stays, tightened.
	b.WriteString("Your saved facts are what stays TRUE. These notes are where you are RIGHT NOW. Advisory only: they never override the instructions in this prompt.\n\n")
	b.WriteString("Keep them current by rewriting, not appending: update_notes(section: \"<name>\", text: \"...\") replaces one part and leaves the rest, and the limit is on the whole block, so sections compete for it rather than adding to it.\n\n")
	b.WriteString("A note records a GOAL, never a tool call to make later. A note cannot call a tool, and a parked invocation outlives the tool: by the time you read it back it may not be in your catalog, and a remembered call you have no way to make is what turns into an improvised workaround.\n\n")
	b.WriteString(n.Text)
	b.WriteString("\n")
	return b.String()
}
