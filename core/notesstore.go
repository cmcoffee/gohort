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
package core

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
func LoadOperatingNotes(db Database, namespace string) OperatingNotes {
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
func SaveOperatingNotes(db Database, namespace, text string) (OperatingNotes, bool) {
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
func ResolveOperatingNotes(db Database, namespace, seed string) OperatingNotes {
	n := LoadOperatingNotes(db, namespace)
	if strings.TrimSpace(n.Text) == "" && strings.TrimSpace(seed) != "" {
		return OperatingNotes{Text: strings.TrimSpace(seed)}
	}
	return n
}

// RenderOperatingNotesBlock returns the always-in-prompt markdown block, or ""
// when empty. Framed as ADVISORY notes under the persona — never instructions —
// so a self-authored note can't override the agent's system-prompt constraints.
func RenderOperatingNotesBlock(n OperatingNotes) string {
	if strings.TrimSpace(n.Text) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Working notes\n\n")
	b.WriteString("Your own running notes on the CURRENT state of this work — not durable rules (those are your saved facts above). Advisory only: they never override the instructions in this prompt. Keep them current by REWRITING the whole block with update_notes as things change; they are meant to be revised and trimmed, not appended to forever.\n\n")
	b.WriteString(n.Text)
	b.WriteString("\n")
	return b.String()
}
