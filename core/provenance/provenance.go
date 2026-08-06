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

package provenance

import (
	"math"
	"strings"
	"time"
)

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

// ClaimDomain says whether the person who stated a claim is in a position to
// SETTLE it. It is the axis MemSource is missing: source records who put the
// claim into memory, domain records whether that makes it true.
//
// The two fuse badly under user_stated. Someone telling you their own name,
// what they prefer, or what they want built IS the evidence — there is nothing
// to check it against, and checking would be both impossible and insulting.
// Someone telling you a server is up, a library has a function, or a database
// sits on NFS has made a claim that is true or false independently of them
// saying it, and accepting it because they said it is how a guess becomes a
// fact the agent later cites as established.
type ClaimDomain uint8

const (
	// ClaimDomainUnknown is a row written before the domain was recorded, or one
	// nobody classified. Treated as the cautious case wherever the distinction
	// decides something: unclassified is not evidence of authority.
	ClaimDomainUnknown ClaimDomain = iota
	// ClaimSelf is about the speaker: identity, preferences, intent, goals,
	// their own history. They are the authority; there is no better source.
	ClaimSelf
	// ClaimWorld is checkable independently of who said it. A statement here is
	// a LEAD until something confirms it, however confidently it was stated and
	// however often it is repeated.
	ClaimWorld
)

// SpeakerAuthoritative reports whether the person who stated this claim settles
// it by having said so. True only for what someone asserts about THEMSELVES:
// every other combination leaves the claim standing on evidence it may not have.
func SpeakerAuthoritative(src MemSource, dom ClaimDomain) bool {
	return src == MemSourceUserStated && dom == ClaimSelf
}

// NeedsAttribution reports whether recall must name where a claim came from
// rather than stating it flat. Rendered bare, a world-claim nothing checked is
// indistinguishable from one that was verified — and three turns later it is
// quoted back as established fact.
//
// The test is the CLAIM, not the speaker. It was tempting to scope this to
// user_stated, but the path that actually launders is the model writing down
// what it just heard: store_fact records those as OBSERVED, so a remark about a
// production database becomes a flat line in every future prompt with nothing
// marking it as hearsay.
//
// Retrieved and imported are exempt: both came from a source at save time
// rather than from conversation. Self-claims are exempt at any source — there
// is nothing to check a preference against, and hedging one reads as doubting
// the person who holds it.
func NeedsAttribution(src MemSource, dom ClaimDomain) bool {
	if dom != ClaimWorld {
		return false
	}
	switch src {
	case MemSourceRetrieved, MemSourceImported:
		return false
	}
	return true
}

// AttributionPhrase renders HOW a claim reached memory, so the marker says
// something true rather than blaming the user for the model's own inference.
func AttributionPhrase(src MemSource) string {
	switch src {
	case MemSourceInferred:
		return "your own inference"
	case MemSourceUserStated:
		return "told to you"
	default:
		return "recorded from conversation"
	}
}

// SpeakerLabel names who said something, for prose. Empty for the principal and
// for anyone unnamed — "told to you by someone" is worse than "told to you",
// since it implies the record knows something it does not.
//
// Reads the display NAME, which is the sender's to choose. That is the right
// trade for prose and the wrong one for authority, which is why nothing here
// decides anything: SpeakerIsOwner is settled from the handle at write time.
func SpeakerLabel(p MemoryProvenance) string {
	if p.SpeakerIsOwner {
		return ""
	}
	return strings.TrimSpace(p.Speaker)
}

// ClaimAuthority ranks claims for GROUNDING — whether one may be asserted as
// fact. Deliberately not SourceTrust, which orders origins for merge and
// supersession and must keep ranking a human's entry above a machine's.
//
// Here a told world-claim sits BELOW a retrieved one, because the question is
// no longer "whose row survives a merge" but "did anything actually check
// this". Same rows, different question, so a separate ordering rather than
// bending the one the merge path depends on.
func ClaimAuthority(src MemSource, dom ClaimDomain) int {
	if SpeakerAuthoritative(src, dom) {
		return 5
	}
	switch src {
	case MemSourceRetrieved:
		return 4
	case MemSourceObserved:
		return 3
	case MemSourceUserStated: // a world-claim, or unclassified: told, not checked
		return 2
	case MemSourceImported:
		return 2
	case MemSourceInferred:
		return 1
	default:
		return 0
	}
}

// sourceTrust ranks origins for "which provenance wins" decisions: a sweep
// merging a user_stated fact with an observed one must not launder both to
// unknown, and a machine re-link must not downgrade a hand-curated edge.
// Higher = more trustworthy. The grounding split (SourcedFactCorpus) is the
// authority on WHICH sources license grounding; this only orders them.
func SourceTrust(s MemSource) int {
	switch s {
	case MemSourceUserStated:
		return 5
	case MemSourceRetrieved:
		return 4
	case MemSourceObserved:
		return 3
	case MemSourceImported:
		return 2
	case MemSourceInferred:
		return 1
	default:
		return 0
	}
}

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
	// Domain says whether the source's word SETTLES the claim — see ClaimDomain.
	// Zero (unknown) is the cautious reading everywhere it decides something,
	// so legacy rows gain no authority they were never granted.
	Domain ClaimDomain `json:"dom,omitempty"`

	// --- Speaker: WHO said it, when that is not simply "the user" ---
	//
	// A room has several people in it, and "the user prefers X" is a lie about
	// most of them. Empty Speaker means the principal — a one-to-one with the
	// owner, which is every non-channel turn — and leaves recall unchanged.
	//
	// The two fields are NOT interchangeable and must not be collapsed.
	// Speaker is a display name the sender chose for themselves, kept because
	// it is what a human reads. SpeakerHandle is the transport's attribution,
	// which the sender cannot set. Anything that grants authority reads the
	// handle; anything that renders prose reads the name.
	Speaker       string `json:"spk,omitempty"`
	SpeakerHandle string `json:"spk_h,omitempty"`
	// SpeakerIsOwner is derived from the HANDLE at write time, never from the
	// name. Renaming yourself "Craig" must not make your claims the owner's.
	SpeakerIsOwner bool `json:"spk_own,omitempty"`
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

// Staleness classifies how aged a claim is relative to its Volatility. Used at
// PULL time (recall / search), where relative-time labeling is fine. The
// always-in-prompt facts block deliberately does NOT use this: it renders the
// stable absolute AsOf date instead, so the cached preamble doesn't churn as
// facts age (date-on-user-turn / deterministic-payload rules).
type Staleness uint8

const (
	Fresh Staleness = iota // within the half-life: trust silently
	Aging                  // past the half-life: surface with an as-of note
	Stale                  // past 2x the half-life: re-verify before relying
)

// Staleness half-life tunables (days). A volatile fact is Aging once older than
// its half-life and Stale past 2x it; slow facts likewise. Stable facts never
// age. 0 disables aging for that class. Registered in factstore.go's init.
const (
	TunableStaleSlowDays     = "tune_fact_stale_slow_days"
	TunableStaleVolatileDays = "tune_fact_stale_volatile_days"
)

// Staleness reports how aged the claim is at `now`, from Volatility + AsOf. A
// future AsOf (clock skew) or a missing one reads as Fresh.
func (p MemoryProvenance) Staleness(now time.Time) Staleness {
	var half int
	switch p.Volatility {
	case VolVolatile:
		half = tuneInt(TunableStaleVolatileDays)
	case VolSlow:
		half = tuneInt(TunableStaleSlowDays)
	default:
		return Fresh // stable / unknown never ages
	}
	if half <= 0 || p.AsOf.IsZero() {
		return Fresh
	}
	ageDays := int(now.Sub(p.AsOf).Hours()) / 24
	switch {
	case ageDays >= 2*half:
		return Stale
	case ageDays >= half:
		return Aging
	default:
		return Fresh
	}
}

// TunableRecencyWeight scales how much recall down-weights aged claims against
// their semantic score. 0 = pure semantic ranking (age ignored); 1 = full bite.
// It never DROPS a hit — a stale claim is scaled by at least (1-strength), so a
// strong old match still surfaces, just below an equally-strong fresh one.
// Registered in tunables.go (Category "Retrieval").
const TunableRecencyWeight = "tune_recency_weight"

// RecencyWeight is the configured recency strength (0..1).
func RecencyWeight() float64 { return tuneFloat(TunableRecencyWeight) }

// RecencyMultiplier returns a factor in [1-strength, 1] that scales a hit's
// semantic score by how aged the claim is, so fresher claims rank ahead of
// equally-relevant stale ones. It reuses the Volatility half-lives that drive
// Staleness: a stable claim never decays (always 1.0), volatile claims halve in
// days, slow ones in months. `strength` (0..1) caps the maximum down-weight — 0
// returns 1.0 always (recency off), 1 lets a very old claim approach 0. The
// curve is a smooth exponential (0.5^(age/half-life)), not the Staleness bucket,
// so ordering doesn't jump at the half-life boundary.
func (p MemoryProvenance) RecencyMultiplier(now time.Time, strength float64) float64 {
	if strength <= 0 {
		return 1
	}
	if strength > 1 {
		strength = 1
	}
	var half int
	switch p.Volatility {
	case VolVolatile:
		half = tuneInt(TunableStaleVolatileDays)
	case VolSlow:
		half = tuneInt(TunableStaleSlowDays)
	default:
		return 1 // stable / unknown never decays
	}
	if half <= 0 || p.AsOf.IsZero() {
		return 1
	}
	ageDays := now.Sub(p.AsOf).Hours() / 24
	if ageDays <= 0 {
		return 1 // future AsOf (clock skew) or just-written → no decay
	}
	decay := math.Pow(0.5, ageDays/float64(half)) // 1 at age 0, → 0 as age grows
	return 1 - strength*(1-decay)
}
