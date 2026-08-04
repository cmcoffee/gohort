// The end-of-turn judge: does this reply claim anything the turn didn't do?
//
// The phrase-list guards in the loop each match a shape somebody thought of
// after watching it fail. This one reads the turn instead. It gets what was
// asked, what actually ran, what came out, and what is about to be said, and
// answers one question about the relationship between them.
//
// Run on the worker model with thinking off and JSON mode on — the same shape
// and the same route as the channel gatekeeper, which makes a comparably small
// judgement thousands of times a day. It is a cheap call on a turn the
// framework already has reason to doubt, not a tax on every reply.

package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// turnJudgeSysPrompt is deliberately narrow.
//
// Every line of it exists to stop the judge doing something other than its job.
// A model handed a conversation and asked to evaluate it will happily grade the
// tone, the helpfulness, or whether it agrees with the answer — and each of
// those produces convictions the framework then acts on, re-prompting replies
// that were fine. The question is not "is this a good reply". It is "does this
// reply describe things that happened".
const turnJudgeSysPrompt = `You check one thing: whether an assistant's reply is TRUE about what its turn actually did.

You are given the user's request, the list of tools the turn ran (possibly empty), how many of them failed, how many files are being delivered with the reply, and the reply itself.

Answer UNKEPT only when the reply states or clearly implies that the assistant DID something, or IS ABOUT TO do something, that the evidence shows did not happen and was not started. Examples of UNKEPT:
- The reply presents a picture, file or document ("here you go", "here's you in the garage", "attached", a caption written as if a photo sits under it) and 0 files are being delivered.
- The reply says the work is underway or imminent ("on it", "let me grab those", "I'll blend them now") and the turn ran no tool and started nothing.
- The reply reports a result that a failed tool never returned.

Answer KEPT for everything else, including:
- Any reply that only ANSWERS, explains, opines, jokes, greets or asks a question. Saying nothing about your own actions cannot be a false claim about them.
- A reply that says it COULD NOT do something, or asks the user for something before proceeding. Refusing and asking are honest outcomes.
- A reply describing work the evidence supports, even loosely.
- A reply you merely find unhelpful, rude, short, wrong on the facts, or badly written. NOT YOUR JOB. Only claims about the assistant's own actions count.

When in doubt, answer KEPT. A wrong UNKEPT makes the assistant retract a reply that was fine, which is worse than letting one slip.

Reply with JSON only: {"verdict":"KEPT"|"UNKEPT","claim":"<the exact sentence from the reply that is untrue, copied verbatim, empty if KEPT>","why":"<one short line naming what actually happened instead, empty if KEPT>"}`

// judgeTurnClaims is the TurnClaimJudge implementation. ok=false on any doubt
// about the JUDGEMENT ITSELF — a model error, an unparseable answer, a verdict
// with nothing to quote. The loop treats that as no opinion rather than as an
// acquittal, which is right: a judge that could not answer has not cleared
// anything.
func (T *OrchestrateApp) judgeTurnClaims(ctx context.Context, ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
	if T == nil || T.LLM == nil {
		return TurnClaimVerdict{}, false
	}
	ran := "none"
	if len(ev.ToolCalls) > 0 {
		ran = strings.Join(ev.ToolCalls, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "USER ASKED:\n%s\n\n", truncateObs(strings.TrimSpace(ev.Request), 800))
	fmt.Fprintf(&b, "TOOLS THE TURN RAN: %s\n", ran)
	fmt.Fprintf(&b, "TOOL CALLS THAT FAILED: %d\n", ev.ToolErrors)
	if ev.LastToolError != "" {
		fmt.Fprintf(&b, "MOST RECENT TOOL ERROR: %s\n", truncateObs(oneLineError(ev.LastToolError, 300), 300))
	}
	fmt.Fprintf(&b, "FILES BEING DELIVERED WITH THIS REPLY: %d\n\n", ev.Delivered)
	fmt.Fprintf(&b, "THE REPLY:\n%s\n", truncateObs(strings.TrimSpace(ev.Reply), 2000))

	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: b.String()}},
		WithSystemPrompt(turnJudgeSysPrompt), WithJSONMode(),
		WithRouteKey("app.orchestrate.worker"), WithThink(false))
	if err != nil {
		Debug("[turn-judge] LLM error: %v — no opinion", err)
		return TurnClaimVerdict{}, false
	}
	var out struct {
		Verdict string `json:"verdict"`
		Claim   string `json:"claim"`
		Why     string `json:"why"`
	}
	if derr := DecodeJSON(resp.Content, &out); derr != nil {
		// No text fallback, unlike the gatekeeper. There a scan for "YES" is a
		// reasonable guess because the cost of guessing wrong is one message
		// not delivered; here it is a retracted reply and a burnt round.
		Debug("[turn-judge] unparseable verdict %q — no opinion", truncateObs(resp.Content, 120))
		return TurnClaimVerdict{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(out.Verdict), "UNKEPT") {
		return TurnClaimVerdict{}, true // a clean acquittal, and the loop needs to know it ran
	}
	claim := strings.TrimSpace(out.Claim)
	if claim == "" {
		// UNKEPT with nothing quoted is a verdict the correction cannot use —
		// it would tell the model "your reply says: """. Treat as no opinion.
		Debug("[turn-judge] UNKEPT with no claim quoted — no opinion")
		return TurnClaimVerdict{}, false
	}
	why := strings.TrimSpace(out.Why)
	if why == "" {
		why = "the turn did not do it"
	}
	Log("[turn-judge] UNKEPT — claim=%q why=%q (tools=%d errors=%d delivered=%d)",
		truncateObs(claim, 100), truncateObs(why, 100), len(ev.ToolCalls), ev.ToolErrors, ev.Delivered)
	return TurnClaimVerdict{Unkept: true, Claim: claim, Why: why}, true
}

// turnClaimJudge binds the judge to one turn's context, or returns nil when the
// feature is off — a nil hook is how the loop skips it entirely.
func (T *OrchestrateApp) turnClaimJudge(ctx context.Context) TurnClaimJudge {
	if T == nil || T.LLM == nil {
		return nil
	}
	return func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return T.judgeTurnClaims(ctx, ev)
	}
}
