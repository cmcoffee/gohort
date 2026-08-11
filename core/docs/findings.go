// Findings — the producer side of documentation, without a destination.
//
// AppendToDocument (document_writer.go) asks the producer for a document and a
// section title. That works when a HUMAN pressed a button: choosing the guide
// and naming the section is the editorial act, and they made it.
//
// It works badly when the producer is an agent. A probe worker deciding, mid
// investigation, which of the user's guides a finding belongs in and what the
// section should be called is making three editorial judgments — is this worth
// documenting, where does it go, what is it called — none of which it is in a
// position to make. It can see one probe. It cannot see the corpus, or the other
// findings from the same run, so it cannot know that its finding duplicates a
// section, contradicts one, or is the third report of the same thing.
//
// So a finding names no destination. It says what was learned and where it came
// from; something with a view of the whole corpus decides the rest. See
// docs/guides-curator.md.
package docs

import (
	"fmt"
	"sort"
	"strings"
)

// Confidence levels a producer may attach to a finding. Deliberately coarse:
// the distinction that matters downstream is whether one observation is being
// reported as fact, not a numeric score nobody can calibrate.
const (
	ConfidenceVerified   = "verified"           // checked directly, more than once or from more than one angle
	ConfidenceProbable   = "probable"           // consistent with what was seen, not separately confirmed
	ConfidenceSingleShot = "single-observation" // seen once; may be a transient or a coincidence
)

// Origin records where a finding came from, so a claim in a document can be
// traced back to the thing that asserted it. SourceKind/ItemID deliberately
// reuse the ReferenceSource vocabulary ("system" + appliance id), so a finding's
// origin and a document's attached sources are the same coordinates.
type Origin struct {
	SourceKind string `json:"source_kind"`          // e.g. "system"
	ItemID     string `json:"item_id"`              // e.g. the appliance id
	ItemLabel  string `json:"item_label,omitempty"` // human name at time of observation
	RunID      string `json:"run_id,omitempty"`     // the investigation/session that produced it
	Observed   string `json:"observed"`             // RFC3339
}

// Finding is one thing learned, reported for documentation.
type Finding struct {
	ID      string `json:"id"`
	Content string `json:"content"` // markdown; what was learned
	// Topic is the producer's one-line framing — NOT a section title. A producer
	// naming a section has already chosen a shape for the document; naming a
	// topic reports what the finding is about, which is the thing it knows.
	Topic      string `json:"topic"`
	Confidence string `json:"confidence,omitempty"`
	Origin     Origin `json:"origin"`
	Submitted  string `json:"submitted"` // RFC3339
	// Submitter is the user the finding was produced FOR. A finding is filed
	// into that user's inbox; the curator later runs in the owning context of
	// whatever document it decides on.
	Submitter string `json:"submitter,omitempty"`
}

// FindingTarget is an optional interface a DocumentTarget implements to accept
// findings as well as placed sections. A target that does not implement it
// simply cannot be reported to — SubmitFinding says so rather than silently
// falling back to an append, because an append is exactly the destination-naming
// behavior a finding exists to avoid.
type FindingTarget interface {
	// SubmitFinding queues a finding for later curation. Returns the assigned
	// finding id. The target owns the queue, in its own store: same shape as
	// DocumentTarget, where the producer never sees the target's database.
	SubmitFinding(user string, f Finding) (string, error)
	// PendingFindings reports how many are waiting, so a batch trigger can fire
	// on a threshold without the scheduler knowing what a finding is.
	PendingFindings(user string) int
}

// SubmitFinding routes a finding to the registered target for kind.
func SubmitFinding(user, kind string, f Finding) (string, error) {
	if strings.TrimSpace(user) == "" {
		return "", fmt.Errorf("a finding needs a user to file it for")
	}
	if strings.TrimSpace(f.Content) == "" {
		return "", fmt.Errorf("a finding needs content")
	}
	docTargetsMu.RLock()
	t := docTargets[kind]
	docTargetsMu.RUnlock()
	ft, ok := t.(FindingTarget)
	if t == nil || !ok {
		return "", fmt.Errorf("nothing accepts findings of kind %q on this deployment", kind)
	}
	f.Submitter = user
	return ft.SubmitFinding(user, f)
}

// PendingFindings returns how many findings are waiting for one kind, or 0 when
// the kind is unregistered or does not accept findings.
func PendingFindings(user, kind string) int {
	docTargetsMu.RLock()
	t := docTargets[kind]
	docTargetsMu.RUnlock()
	if ft, ok := t.(FindingTarget); ok && t != nil {
		return ft.PendingFindings(user)
	}
	return 0
}

// FindingKinds returns the registered targets that accept findings (ID = kind,
// Title = label), sorted by label — for a producer choosing where to report.
func FindingKinds() []DocItem {
	docTargetsMu.RLock()
	defer docTargetsMu.RUnlock()
	out := make([]DocItem, 0, len(docTargets))
	for kind, t := range docTargets {
		if _, ok := t.(FindingTarget); !ok {
			continue
		}
		out = append(out, DocItem{ID: kind, Title: t.Label()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// AcceptsFindings reports whether anything on this deployment can receive a
// finding of the given kind — the gate a producer uses to decide whether to
// offer the tool at all, rather than offering one that always errors.
func AcceptsFindings(kind string) bool {
	docTargetsMu.RLock()
	t := docTargets[kind]
	docTargetsMu.RUnlock()
	_, ok := t.(FindingTarget)
	return t != nil && ok
}

// NormalizeConfidence maps a producer's free-text confidence onto the three
// stored levels, defaulting to single-observation. The default is deliberately
// the WEAKEST: an unstated confidence is an unmade claim, and treating it as
// verified would let every producer that ignores the field promote its guesses.
func NormalizeConfidence(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ConfidenceVerified, "confirmed", "certain":
		return ConfidenceVerified
	case ConfidenceProbable, "likely":
		return ConfidenceProbable
	default:
		return ConfidenceSingleShot
	}
}
