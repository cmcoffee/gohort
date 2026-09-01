package factcheck

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

	// Audit records what happened when the claim was checked back
	// against the source material the report was written from. Empty
	// means the claim was never audited (legacy records, or a pipeline
	// that doesn't run the audit pass) — which is NOT the same as
	// passing, and downstream consumers should not read it as such.
	//
	//   ClaimSupported   — a verbatim passage in the corpus carries it
	//   ClaimNarrowed    — the corpus supported a narrower version; the
	//                      claim text and scope have been tightened
	//   ClaimUnsupported — no passage in the corpus carries it
	//   ClaimUnchecked   — the audit ran but could not confirm support
	//                      (no quotable passage, or a quote that did not
	//                      appear in the corpus)
	//
	// A prose generator composing from the ledger should treat
	// ClaimUnsupported as unusable as a headline claim, and carry the
	// hedge that the audit wrote into the report rather than restating
	// the claim flat.
	Audit string `json:"audit,omitempty"`
}

// Audit verdicts for Claim.Audit.
const (
	ClaimSupported   = "supported"
	ClaimNarrowed    = "narrowed"
	ClaimUnsupported = "unsupported"
	ClaimUnchecked   = "unchecked"
)
