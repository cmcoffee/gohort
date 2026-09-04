// The grounding judge: did the reply state something nobody checked?
//
// Mirrors turn_judge.go in shape — worker-tier model, JSON verdict, no opinion
// on any doubt — and differs in what it reads. That one gets the actions; this
// one gets the sentences the memory block marked as unchecked and asks whether
// the reply presented one as established.
package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// groundingJudgeSysPrompt is deliberately narrow. A judge given "is this reply
// well grounded?" convicts anything confident, and the correction it triggers
// teaches hedging — which is the failure this whole design is most often
// mistaken for. So it is asked one question about one list of sentences.
const groundingJudgeSysPrompt = `You check whether a reply overstates what is KNOWN.

You are given: the notes the assistant holds that are NOT independently verified, the tools this turn ran, and the reply about to be sent.

Answer ASSERTED only when ALL of these hold:
1. The reply states one of those unverified notes as established fact — flatly, as though confirmed.
2. The turn ran nothing that would have confirmed it. If a tool call could have checked it, the reply may state what it found.
3. The claim matters to the answer. An aside is not worth a correction.

Answer CLEAN for everything else, including:
- The reply repeats an unverified note WITH attribution ("you mentioned…", "per your notes…"). That is exactly what is wanted.
- The reply is confident about anything else. Only the listed notes are in scope.
- The reply asserts something the turn verified this turn.
- The reply is about the user's own preferences, goals or identity. They are the authority on those.
- The reply DESCRIBES material it was shown rather than endorsing it: "the picture shows X", "he posted a meme saying X". Reporting what something contains is not asserting that its contents are true.
- The note was plainly never offered as fact (a joke, a meme, teasing, obvious exaggeration) and the reply treats it that way. Playing along with a joke is not asserting it. Only a reply that carries the joke's CONTENT forward as real is ASSERTED.

Quote the offending sentence from the REPLY verbatim as "claim", and the note it traces to verbatim as "basis".

Reply with JSON only: {"verdict":"ASSERTED"|"CLEAN","claim":"…","basis":"…"}`

// judgeTurnGrounding is the TurnGroundingJudge implementation. ok=false on any
// doubt about the judgement itself, which the loop reads as no opinion.
func (T *OrchestrateApp) judgeTurnGrounding(ctx context.Context, ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
	if T == nil || T.LLM == nil {
		return TurnGroundingVerdict{}, false
	}
	ran := "none"
	if len(ev.ToolCalls) > 0 {
		ran = strings.Join(ev.ToolCalls, ", ")
	}
	var b strings.Builder
	b.WriteString("NOTES THE ASSISTANT HOLDS THAT ARE NOT INDEPENDENTLY VERIFIED:\n")
	for _, n := range ev.Unchecked {
		fmt.Fprintf(&b, "- %s\n", truncateObs(strings.TrimSpace(n), 300))
	}
	fmt.Fprintf(&b, "\nTOOLS THE TURN RAN: %s\n\n", ran)
	fmt.Fprintf(&b, "THE REPLY:\n%s\n", truncateObs(strings.TrimSpace(ev.Reply), 2000))

	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: b.String()}},
		WithSystemPrompt(groundingJudgeSysPrompt), WithJSONMode(),
		WithRouteKey("app.orchestrate.worker"), WithThink(false))
	if err != nil {
		Debug("[grounding-judge] LLM error: %v — no opinion", err)
		return TurnGroundingVerdict{}, false
	}
	var out struct {
		Verdict string `json:"verdict"`
		Claim   string `json:"claim"`
		Basis   string `json:"basis"`
	}
	if derr := DecodeJSON(resp.Content, &out); derr != nil {
		// A quoted claim usually carries its own quotation marks, unescaped;
		// see salvageJudgeJSON. Only text with no verdict at all is given up on.
		fields, ok := salvageJudgeJSON(resp.Content, []string{"verdict", "claim", "basis"})
		if !ok {
			Debug("[grounding-judge] unparseable verdict %q — no opinion", truncateObs(resp.Content, 120))
			return TurnGroundingVerdict{}, false
		}
		out.Verdict, out.Claim, out.Basis = fields["verdict"], fields["claim"], fields["basis"]
	}
	if !strings.EqualFold(strings.TrimSpace(out.Verdict), "ASSERTED") {
		Debug("[grounding-judge] CLEAN (%d unverified note(s) in scope)", len(ev.Unchecked))
		return TurnGroundingVerdict{}, true
	}
	claim, basis := strings.TrimSpace(out.Claim), strings.TrimSpace(out.Basis)
	if claim == "" || basis == "" {
		// The correction quotes BOTH — the sentence to rewrite and the note it
		// came from. Without either it would tell the model its reply says ""
		// or that the claim traces to nothing, so this is no opinion rather
		// than a conviction nobody can act on.
		Debug("[grounding-judge] ASSERTED with nothing to quote — no opinion")
		return TurnGroundingVerdict{}, false
	}
	Log("[grounding-judge] ASSERTED — %q traces to unverified note %q", truncateObs(claim, 100), truncateObs(basis, 100))
	return TurnGroundingVerdict{Asserted: true, Claim: claim, Basis: basis}, true
}

// turnGroundingJudge binds the app + context into the loop's judge slot, and
// returns nil when there is no model to ask — the loop then skips it entirely
// rather than treating an absent judge as an acquittal.
func (T *OrchestrateApp) turnGroundingJudge(ctx context.Context) TurnGroundingJudge {
	if T == nil || T.LLM == nil {
		return nil
	}
	return func(ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
		return T.judgeTurnGrounding(ctx, ev)
	}
}
