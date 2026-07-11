// MemoryProvenance is the lifecycle envelope embedded in every stored memory
// row (MemoryFact, GraphEntity, EmbeddedChunk). It answers three things about a
// claim: where it came from, how fast its content goes stale, and — once it
// leaves the live set — why and where it went.
//
// The zero value is a live row of unknown origin that never decays, so the
// struct is additive: existing rows and every caller that constructs these
// types with named fields keep working untouched. Only the fact layer populates
// and acts on it today (supersession, eviction, merge); graph and vector embed
// it so a future hygiene pass there inherits the same vocabulary instead of
// inventing a second one.

package core

import "time"

// MemSource records HOW a stored claim entered memory. It drives the honesty
// signal at recall time: a user_stated fact carries more weight than an inferred
// one. Zero (MemSourceUnknown) means the origin was not recorded.
type MemSource uint8

const (
	MemSourceUnknown    MemSource = iota // origin not recorded (legacy / unclassified)
	MemSourceUserStated                  // the user asserted it directly
	MemSourceObserved                    // derived from something that happened in-session
	MemSourceRetrieved                   // pulled from a tool / search / document at save time
	MemSourceInferred                    // the model concluded it (lowest trust)
	MemSourceImported                    // bulk-loaded from an external store
)

// Volatility is how fast a claim's CONTENT (not the row) goes stale. Set at
// write time from the claim's category, NOT from the model's confidence — the
// facts a model is most sure of are exactly the stale priors it should trust
// least, so confidence is the wrong signal. Zero (VolStable) is the safe default:
// an unclassified fact is treated as never-decaying, so it is never flagged stale.
type Volatility uint8

const (
	VolStable   Volatility = iota // effectively never stale (names, definitions, preferences). Default.
	VolSlow                       // changes over months (employer, city)
	VolVolatile                   // changes in days or faster (prices, live status, standings)
)

// RetireReason records why a memory row left the live set. Zero (RetireLive)
// means the row is still active — the common case and the sole liveness test.
// The three non-zero values are the only ways a row leaves: a newer row replaced
// it, the cap evicted it for space, or a sweep merged it into a combined row.
type RetireReason uint8

const (
	RetireLive       RetireReason = iota // still live (the common case)
	RetireSuperseded                     // a newer row replaced this attribute (Successor set)
	RetireEvicted                        // the hard cap dropped it for space (no Successor)
	RetireMerged                         // a sweep folded it into a combined row (Successor = that row)
)

// MemoryProvenance is embedded (anonymously) into the stored memory types, so
// its fields flatten into their JSON and promote onto the outer type. All fields
// are omitempty: an unset envelope adds nothing to the wire.
type MemoryProvenance struct {
	// --- Origin: set once at write time ---
	Source     MemSource  `json:"src,omitempty"`
	Volatility Volatility `json:"vol,omitempty"`
	// AsOf is when the claim was last CONFIRMED true, distinct from a row's
	// Created (first write) and Updated (any mutation). A re-verification bumps
	// AsOf without rewriting the note. Staleness is measured from AsOf.
	AsOf time.Time `json:"as_of,omitempty"`

	// --- Retirement: set when the row leaves the live set. Zero = live. ---
	Reason    RetireReason `json:"reason,omitempty"`
	RetiredAt time.Time    `json:"retired_at,omitempty"` // when it left the live set
	Successor string       `json:"successor,omitempty"`  // ID of the row that replaced/absorbed it; empty for evicted
}

// Retired reports whether the row has left the live set. It is the single
// liveness test used by the store's live/history split.
func (p MemoryProvenance) Retired() bool { return p.Reason != RetireLive }

// RetireReasonLabel renders a retirement reason as user-facing prose, so recall
// can explain a hole ("you had X; it was <label> on <date>") when a live query
// finds nothing.
func RetireReasonLabel(r RetireReason) string {
	switch r {
	case RetireSuperseded:
		return "superseded by a newer note"
	case RetireEvicted:
		return "dropped to stay under the memory cap"
	case RetireMerged:
		return "merged into a combined note"
	default:
		return "retired"
	}
}
