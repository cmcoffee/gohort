// Asking whether a reply stated something nobody checked.
//
// Everything the grounding work has shipped so far is the model choosing to
// comply: recall marks an unchecked claim, the block explains what a marker
// means, and then the reply says whatever it says. Nothing reads the reply.
// That is the same position the claim guards were in before the turn judge —
// rules stated, compliance assumed, failures found in production by a person.
//
// This is deliberately a SECOND judge rather than another arm of the first.
// turn_judge.go is scoped to actions ("did the thing this reply describes
// actually occur") and says so in its own header; whether a sentence overstates
// what is KNOWN is a different question over different evidence, and fusing
// them would give one prompt two jobs and one verdict two meanings.
//
// The failure it looks for is narrow and specific: the turn held a claim marked
// as unchecked, ran nothing that would settle it, and the reply asserted it
// flat — "the server runs 22.04" rather than "you mentioned the server runs
// 22.04". Repeating an unchecked claim is fine WITH attribution; that is what
// the rule asks for, and a judge that convicted it would teach the model to
// stop mentioning what it knows.
package core

import "strings"

// TurnGroundingEvidence is what the judge needs to tell a reply that reports
// what is known from one that overstates it.
type TurnGroundingEvidence struct {
	// Reply is what is about to be delivered.
	Reply string
	// Unchecked are the notes that were in this turn's prompt carrying an
	// unchecked marker — the only claims this judge has any opinion about.
	// Everything else in the reply is out of scope.
	Unchecked []string
	// ToolCalls names what the turn ran. A turn that actually went and looked
	// may assert what it found, so the judge has to see that it looked.
	ToolCalls []string
}

// TurnGroundingVerdict is the judge's answer.
type TurnGroundingVerdict struct {
	// Asserted is true when the reply states an unchecked claim as established
	// fact, with nothing in the turn having checked it.
	Asserted bool
	// Claim quotes the offending sentence from the REPLY, for the same reason
	// the claim judge quotes rather than paraphrases: a model will argue with a
	// paraphrase of its own words.
	Claim string
	// Basis quotes the stored note the claim traces back to, so the correction
	// can name where it came from instead of asserting the reply is wrong —
	// which the framework does not know and must not claim.
	Basis string
}

// TurnGroundingJudge reads a finished turn and reports whether its reply
// overstates what is known. ok is false when no judgement could be reached, and
// the loop then does nothing: a judge that cannot answer has not cleared
// anything.
//
// Nil disables it, which is every host that has not opted in.
type TurnGroundingJudge func(TurnGroundingEvidence) (TurnGroundingVerdict, bool)

// turnGroundingWorthJudging decides whether a model call is warranted.
//
// Same asymmetry the claim judge relies on: a false positive here costs one
// small model call that comes back clean, so the filter can be sloppy. It is
// three cheap facts — there is a reply, there is something unchecked in scope,
// and the reply appears to touch it.
//
// The touch test is wording-shaped, which the claim judge's filter deliberately
// avoids. It is right here for a reason that does not apply there: this judge's
// subject is a KNOWN LIST OF SENTENCES, not an open set of ways to imply work.
// A reply sharing no distinctive word with any of them is not discussing them.
func turnGroundingWorthJudging(ev TurnGroundingEvidence) bool {
	if strings.TrimSpace(ev.Reply) == "" || len(ev.Unchecked) == 0 {
		return false
	}
	reply := strings.ToLower(ev.Reply)
	for _, note := range ev.Unchecked {
		if claimTouchesReply(reply, note) {
			return true
		}
	}
	return false
}

// claimTouchesReply reports whether the reply uses any distinctive word from a
// note. Short and common words are skipped: matching on "the" or "is" would
// make every reply worth judging, which is the same as having no filter.
func claimTouchesReply(lowerReply, note string) bool {
	for _, w := range strings.Fields(strings.ToLower(note)) {
		w = strings.Trim(w, ".,;:!?\"'()[]")
		if len(w) < 5 || groundingStopWords[w] {
			continue
		}
		if strings.Contains(lowerReply, w) {
			return true
		}
	}
	return false
}

// groundingStopWords are long-but-empty words that would otherwise make any two
// sentences look related.
var groundingStopWords = map[string]bool{
	"about": true, "after": true, "again": true, "their": true, "there": true,
	"these": true, "those": true, "which": true, "while": true, "would": true,
	"could": true, "should": true, "because": true, "before": true, "being": true,
	"other": true, "under": true, "where": true, "every": true, "still": true,
	"user": true, "users": true,
}

// withLiveClaim adds THIS message to the unchecked list when it came from
// someone who is not the principal.
//
// The stored-claim path cannot see it: nothing has classified or marked the
// message, because it is not in memory and may never be. In a room that is
// exactly the claim that does damage — one participant asserts something, the
// agent adopts it inside the same turn, and repeats it to everyone else as
// fact.
//
// Deliberately NOT added for the owner. They are the principal; treating their
// message as an unverified claim would make the agent hedge instructions it was
// given, which is a different failure and a worse one.
func withLiveClaim(stored []string, speaker, message string) []string {
	note := liveClaimNote(speaker, message)
	if note == "" {
		return stored
	}
	out := make([]string, 0, len(stored)+1)
	out = append(out, stored...)
	return append(out, note)
}

// liveClaimMarker joins the speaker to what they said. Split out from the note
// so the correction can recognise its own wording and tell a LIVE claim from a
// stored one, and worded so it does not pre-decide the question: "asserted"
// (what this used to say) frames a posted meme as a factual assertion before
// any judge has looked at it, and the correction then asks the model to account
// for a joke as though it were evidence.
const liveClaimMarker = " said this in the conversation just now, and nothing has verified it: "

// liveClaimNote renders the live message as an unchecked note, or "" when there
// is no non-principal speaker or nothing was said.
func liveClaimNote(speaker, message string) string {
	speaker, message = strings.TrimSpace(speaker), strings.TrimSpace(message)
	if speaker == "" || message == "" {
		return ""
	}
	return speaker + liveClaimMarker + message
}

// basisIsLiveClaim reports whether a judge's quoted basis came from the live
// message rather than the stored notes. The judge quotes verbatim but may quote
// only the said-part, so containment is checked both ways.
func basisIsLiveClaim(liveNote, basis string) bool {
	basis = strings.TrimSpace(basis)
	if liveNote == "" || basis == "" {
		return false
	}
	return strings.Contains(basis, liveClaimMarker) || strings.Contains(liveNote, basis)
}

// judgeTurnGrounding runs the app's judge when the evidence warrants it, and
// returns a verdict only on a conviction with something to quote. A conviction
// with no quoted claim is unusable: the correction would tell the model its
// reply says "".
func judgeTurnGrounding(cfg AgentLoopConfig, ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
	if cfg.TurnGroundingJudge == nil || !turnGroundingWorthJudging(ev) {
		return TurnGroundingVerdict{}, false
	}
	v, ok := cfg.TurnGroundingJudge(ev)
	if !ok || !v.Asserted || strings.TrimSpace(v.Claim) == "" {
		return TurnGroundingVerdict{}, false
	}
	return v, true
}
