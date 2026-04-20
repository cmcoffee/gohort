package core

// Claim is a structured fact extracted from a synthesis report. The
// prose report is the human-facing output; the claim ledger is the
// machine-facing canonical form that downstream consumers (prose
// generators, audit tooling) use to compose new prose without the
// drift that free-form rewriting introduces.
//
// Invariants intended for callers:
//   - Text is verbatim from the report's prose, trimmed.
//   - Scope names the specific population / domain the claim applies
//     to. "unspecified" is used only when the report genuinely does
//     not assert one.
//   - Citations are the [N] numbers attached to this claim in the
//     report, as extracted by the ledger pass.
//
// When a downstream writer consumes a Claim, Scope must be carried
// into any prose that uses the claim's outcome — otherwise a
// scope-narrow finding gets silently widened to the whole topic.
type Claim struct {
	Text      string `json:"text"`
	Scope     string `json:"scope"`
	Citations []int  `json:"citations,omitempty"`
	Kind      string `json:"kind,omitempty"` // "outcome" | "motivation" | "correlation" | "definition" | "counter"
}
