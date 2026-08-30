// Reading the turn instead of guessing at its wording.
//
// Four guards in this file decide whether a reply is honest about what the turn
// did, and every one of them works by matching phrases: a cue list plus an
// attachment noun (replyClaimsAttachment), a first-person commitment regex
// (replyStalledOnAPromise), a trailing colon (endsWithCallAnnouncement), and one
// disabled outright for noise (containsActionPromise). Each has a blind spot,
// and each blind spot was found the same way — in production, by a person.
//
//   - "Here's you, wasting away in the garage like Craig ordered." Slipped the
//     noun rule, because a caption names what is IN the picture and never the
//     picture.
//   - "Got it — let me create this. I'll blend Alex onto the picture." Slipped
//     the phrase list, because "create" and "blend" weren't on it.
//   - "Here's the update_agent call to implement these changes" without the
//     colon. Given up on deliberately: indistinguishable in form from "Here's
//     the answer."
//
// The lists cannot be finished. There is no enumeration of the ways a sentence
// can imply work that didn't happen, and every extension of one traded a missed
// lie for a re-prompted honest answer.
//
// A judge doesn't have that failure mode, because it isn't matching anything.
// It gets the request, what actually ran, what came out, and the reply — and
// answers one question: does this reply claim or promise something the turn did
// not do? Wording is the thing it reads rather than the thing it depends on.

package core

import "strings"

// TurnClaimEvidence is everything the judge needs to tell a reply that reports
// what happened from one that reports what didn't.
//
// It is deliberately about ACTIONS, not content. Whether a summary is faithful
// to the page it summarized is a different question needing different evidence;
// this is scoped to "did the thing this reply describes actually occur".
type TurnClaimEvidence struct {
	// Request is what the user asked for, in their words.
	Request string
	// Reply is what is about to be delivered.
	Reply string
	// ToolCalls names every tool this turn ran, in order, with duplicates —
	// three image calls are three attempts and the judge should see all three.
	//
	// Each entry is the tool's LABEL: "name" for a plain tool, "name/action"
	// for a grouped one. The action is the load-bearing half. A grouped tool
	// puts reading and writing behind a single name, so a list of bare names
	// cannot distinguish a turn that fetched four feeds from one that posted
	// four comments — and a reply claiming the posts is consistent with it.
	// Observed on a scheduled fire: nine moltbook calls, every one a read,
	// reported as three comments posted with invented ids and 201s.
	ToolCalls []string
	// PriorWork is work done FOR this turn before its loop began, which the
	// loop therefore never sees: a machine step that searched, a delegated
	// step, a pipeline phase. Each entry names what ran.
	//
	// Without it the judge convicts a reply for reporting work that genuinely
	// happened. A machine's step runs during system-prompt assembly, on a
	// session of its own, and hands its findings to the turn as state — so a
	// reply that opens "Based on the Confluence research" is TRUE, while the
	// loop's own tool list is empty and every arm of the evidence says nothing
	// happened. Reported live, and it is the whole class: any turn whose work
	// was done by a step rather than by the model answering.
	PriorWork []string
	// ToolErrors counts the calls that failed.
	ToolErrors int
	// LastToolError is the most recent failure text, so the judge can tell a
	// reply that reports the real reason from one that invents a different one.
	LastToolError string
	// Delivered is how many attachments actually go out with this reply.
	Delivered int
	// Backgrounded is set when the turn started a detached job. It makes "I'll
	// report back when it's done" TRUE, and that reply is one the framework
	// explicitly asks for — see detachedNotice.
	Backgrounded bool
	// GivenEstimate is the wait the framework itself offered for that job
	// ("about 13 seconds"), empty when it offered none.
	//
	// The machinery rule distinguishes a duration the assistant INVENTED from
	// one it was handed, and that distinction is unanswerable from the reply
	// alone — the sentence reads identically either way. Without this the judge
	// had a rule it could not apply, so it flagged the compliant reply and the
	// loop retracted a message the framework had asked for in those words.
	GivenEstimate string
	// Unattended marks a turn nobody is reading as it happens. It widens the
	// pre-filter rather than the verdict: the judge asks the same question and
	// applies the same standard, it just gets asked on turns the evidence
	// alone would have let through. See turnClaimWorthJudging.
	Unattended bool
}

// TurnClaimVerdict is the judge's answer.
type TurnClaimVerdict struct {
	// Unkept is true when the reply says the turn did or will do something it
	// did not do and did not start.
	Unkept bool
	// Claim quotes the offending sentence from the reply. Quoting rather than
	// paraphrasing matters for the same reason the commitment ledger stores the
	// promise verbatim: a model will argue with a paraphrase.
	Claim string
	// Why is one line naming what actually happened instead.
	Why string
	// Machinery quotes a sentence that explains the framework's own plumbing to
	// someone who did not ask about it — a task id, that work is running in the
	// background, an invitation to check back or wait. Empty when clean.
	//
	// Independent of Unkept, and it has to be: the replies that leak plumbing
	// are usually perfectly TRUE. "The image edit task is still running in the
	// background (task a79c771f5f35a9f6ef0489d0)" was an accurate account of
	// the situation and still nothing the person asked to hear.
	Machinery string
}

// TurnClaimJudge reads a finished turn and reports whether its reply is true
// about what the turn did. ok is false when no judgement could be reached — a
// model error, a timeout — and the loop then does nothing, because a judge that
// cannot answer must not be read as an acquittal OR a conviction.
//
// Nil (the default) disables the judge entirely, which is what every host had
// before it, phrase-list guards and all. The judge does not replace them: they
// are cheap, they run every round, and they catch the shapes they know. It
// backstops them on the shapes nobody has thought of yet.
type TurnClaimJudge func(TurnClaimEvidence) (TurnClaimVerdict, bool)

// turnClaimWorthJudging decides whether the judge is worth a model call.
//
// The asymmetry that makes this safe: the phrase-list guards must be PRECISE,
// because they act directly — a false positive re-prompts an honest answer. A
// pre-filter can be sloppy, because all a false positive costs is one small
// model call that comes back "kept". So this is deliberately over-inclusive
// where the guards are careful.
//
// It is also evidence-shaped rather than wording-shaped, which is the whole
// point. Gating the judge on the same phrases the guards use would hand it
// exactly the turns they already catch and hide the ones they miss.
// TurnDidWork reports whether anything ran for this turn at all, in the loop
// or before it. The distinction the pre-filter and the trigger label both need:
// "no tools ran" is a reason to look closer only when nothing ran ANYWHERE.
func (ev TurnClaimEvidence) TurnDidWork() bool {
	return len(ev.ToolCalls) > 0 || len(ev.PriorWork) > 0
}

func turnClaimWorthJudging(ev TurnClaimEvidence) bool {
	if strings.TrimSpace(ev.Reply) == "" {
		return false
	}
	// A started background job used to skip the judge outright, on the grounds
	// that "I'll report back" is TRUE and convicting it would flag the exact
	// reply detachedNotice asks for. That reasoning holds for the CLAIM, and
	// it made the judge blind to the other thing these turns do: a detach is
	// where plumbing leaks, because the model has just been handed a task id
	// and told a great deal about how the work is being run.
	//
	// So it is judged, and the evidence carries the fact that a job started —
	// the claim arm suppresses itself on that fact rather than on this branch,
	// which leaves the machinery arm free to look.
	if ev.Backgrounded {
		return true
	}
	// Said something, did nothing. The largest class by far, and the one the
	// guards keep half-missing: "Wiwee, try again" answered in 66 characters
	// with zero tool calls.
	//
	// Work done BEFORE the loop counts as having done something — a turn
	// whose step went and searched is not a turn that sat still, and putting
	// it in front of the judge on those grounds is asking a question whose
	// premise is already false.
	if !ev.TurnDidWork() {
		return true
	}
	// Did something and it failed. A reply after a failed tool is either an
	// honest report or a claim of a result that never arrived.
	if ev.ToolErrors > 0 {
		return true
	}
	// Ran something whose job is producing a file, and nothing is going out
	// with the reply.
	if ev.Delivered == 0 && turnRanProducer(ev.ToolCalls) {
		return true
	}
	// Nobody is reading. Every arm above asks "does the framework have reason
	// to doubt THIS turn"; a turn that ran cleanly and failed nothing has none,
	// which is the right answer when a person is looking at the reply and can
	// say "you didn't attach anything". Unattended, that check does not exist:
	// a fire that reads nine times and reports three finished posts leaves a
	// transcript nothing disputes and a run ledger recording a success.
	//
	// This is the loosest arm by a distance, and it is affordable for the
	// reason the whole pre-filter is allowed to be sloppy — a false positive
	// costs one small model call that comes back KEPT. Fires are a small
	// fraction of turns, so the ceiling on that cost is low and known.
	if ev.Unattended {
		return true
	}
	return false
}

// turnRanProducer reports whether any call was to a tool that exists to make
// something for the user. Kept here rather than taking the app's list because
// the loop needs an answer before any app hook is consulted; hosts with other
// producers get them covered by the no-tools and tool-error arms above.
func turnRanProducer(calls []string) bool {
	for _, c := range calls {
		// Entries are labels now ("image/edit"), so match the tool half. Left
		// as an exact switch on the name rather than a prefix test: "video"
		// must not be matched by a tool called "videoconference".
		if i := strings.IndexByte(c, '/'); i >= 0 {
			c = c[:i]
		}
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "image", "generate_image", "find_image", "fetch_image", "video", "download_video", "create_docx":
			return true
		}
	}
	return false
}

// judgeTurnClaim runs the app's judge when the evidence warrants it. Returns a
// verdict only when the judge actually convicted.
func judgeTurnClaim(cfg AgentLoopConfig, ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
	if cfg.TurnClaimJudge == nil || !turnClaimWorthJudging(ev) {
		return TurnClaimVerdict{}, false
	}
	v, ok := cfg.TurnClaimJudge(ev)
	if !ok || (!v.Unkept && strings.TrimSpace(v.Machinery) == "") {
		return TurnClaimVerdict{}, false
	}
	return v, true
}
