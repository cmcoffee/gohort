package orchestrate

// The review half of "Hybrid" (AgentRecord.LeadReview): the worker runs the turn, and
// before the reply goes out the lead reads it with what the turn ran and got
// back, then lets it go or sends it back with the changes it needs.
//
// The cheapest way to put the stronger model's judgment on every turn that
// matters. It reads once, at the end, and sees no tool list and no history:
// the request, the reply, and the record of the turn's calls with excerpts
// of what they returned. It never rewrites the reply; a reviewer that could
// would just be the lead writing every reply with an extra step. The loop
// side (core leadReview) sends a reply back at most twice and reviews only
// turns that did work.

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// leadReviewRouteKey routes the review. Its own stage, like consult's, so an
// administrator sees it on the routing page and can send it to the worker.
const leadReviewRouteKey = "app.orchestrate.lead_review"

func init() {
	RegisterRouteStage(RouteStage{
		Key:   leadReviewRouteKey,
		Label: "Agents: Lead review (reads a finished reply)",
		// One call per reviewed turn, carrying the request, the reply and
		// excerpts of what the turn's calls returned, and nothing else. NOT
		// Private, so it can reach the lead; it falls back to the worker
		// when no lead is configured.
		Default: "lead",
		Group:   "Agents",
	})
}

// leadReviewSysPrompt is the reviewer's whole brief.
const leadReviewSysPrompt = `You review an assistant's finished turn before its reply reaches the person who asked. You see what they asked, the reply, and what the turn ran and what those calls returned. You do not see the conversation before this message, and you cannot run anything.

Send the reply back only for something that matters to the person:
- it does not answer what was asked, or answers a different question;
- it leaves out part of what was asked;
- it states something the returned results contradict or do not support;
- it reports work as done that the results show failed or never ran;
- it stops short of a step the request plainly needed and the turn could have taken with the tools it was already using.

Do not send it back for style, tone, length, or wording you would have chosen differently, and do not ask for detail the person did not ask for. When in doubt, approve.

Answer with JSON only: {"verdict": "APPROVE"} or {"verdict": "SEND_BACK", "changes": "a numbered list; each item says what is wrong and what the returned results show, specific enough to act on"}.`

// leadReviewer is the turn's TurnReview hook, or nil when this agent's turns
// are not reviewed: not chosen, already on the lead, or unable to reach it.
func (t *chatTurn) leadReviewer(ctx context.Context) func(TurnClaimEvidence) (bool, string, bool) {
	if t == nil || t.app == nil || !t.agent.LeadReview || t.agent.LeadModel || !t.leadPermitted() {
		return nil
	}
	return func(ev TurnClaimEvidence) (bool, string, bool) {
		return t.app.reviewTurn(ctx, t.leadGate(), ev)
	}
}

// reviewTurn is one review: the brief, the evidence, the lead's verdict.
func (T *OrchestrateApp) reviewTurn(ctx context.Context, g leadGate, ev TurnClaimEvidence) (bool, string, bool) {
	agentID := g.agentID
	started := time.Now()
	resp, err := T.leadChatGated(ctx, g, "review", []Message{{Role: "user", Content: leadReviewEvidence(ev)}},
		WithSystemPrompt(leadReviewSysPrompt), WithJSONMode(), WithRouteKey(leadReviewRouteKey))
	took := time.Since(started).Round(100 * time.Millisecond)
	if err != nil || resp == nil {
		Log("[lead-review] agent=%s: no review (%v); the reply goes out as written", agentID, err)
		return false, "", false
	}
	var out struct {
		Verdict string `json:"verdict"`
		Changes string `json:"changes"`
	}
	if derr := DecodeJSON(resp.Content, &out); derr != nil {
		fields, ok := salvageJudgeJSON(resp.Content, []string{"verdict", "changes"})
		if !ok {
			Log("[lead-review] agent=%s: unreadable verdict %q; the reply goes out as written", agentID, truncateObs(resp.Content, 120))
			return false, "", false
		}
		out.Verdict, out.Changes = fields["verdict"], fields["changes"]
	}
	verdict := strings.ToUpper(strings.TrimSpace(out.Verdict))
	changes := strings.TrimSpace(out.Changes)
	if verdict != "SEND_BACK" || changes == "" {
		Log("[lead-review] agent=%s: approved in %s (tools=%d, tier=%s)", agentID, took, len(ev.ToolCalls), resp.Tier)
		return false, "", true
	}
	Log("[lead-review] agent=%s: SENT BACK in %s (tier=%s): %s", agentID, took, resp.Tier, truncateObs(changes, 200))
	return true, changes, true
}

// reviewedReplyLabel is what a revision the review asked for is shown under,
// as core's loop shows its own.
const reviewedReplyLabel = "Revised after review"

// plannedRevisionNote is what the write-up is told when the review sends a
// planned turn's reply back. It has no tools, so it works from the findings.
func plannedRevisionNote(changes string) string {
	return frameworkNoteTag + "A reviewer read your reply against what this turn's steps ran and got back, and sends it back:\n" + strings.TrimSpace(changes) +
		"\n\nMake these changes from the worker findings above, and give the complete reply again. Where the findings do not support a change, say plainly what is missing. Do not mention the review, and do not apologize."
}

// plannedTurnEvidence is the review's view of a planned turn: what its steps
// ran and returned, each step's findings, and what goes out with the reply.
func (t *chatTurn) plannedTurnEvidence(request, reply string, steps []PlanStep) TurnClaimEvidence {
	ev := TurnClaimEvidence{Request: request, Reply: reply, Now: CurrentContextStampIn(UserLocation(t.user))}
	t.toolMu.Lock()
	for _, rec := range t.toolCalls {
		if rec.Framework {
			continue
		}
		label := rec.Name
		if rec.Err != "" {
			label += " [FAILED: " + truncateObs(rec.Err, 160) + "]"
			ev.ToolErrors++
		}
		ev.ToolCalls = append(ev.ToolCalls, label)
		ev.ToolOutputs = append(ev.ToolOutputs, label+": "+reviewExcerpt(cmp.Or(rec.Result, rec.Err)))
	}
	t.toolMu.Unlock()
	for _, s := range steps {
		ev.PriorWork = append(ev.PriorWork, fmt.Sprintf("step %d, %s: %s", s.ID, s.Title, reviewExcerpt(s.Output)))
	}
	t.attMu.Lock()
	ev.Delivered = len(t.deliveredAtt) + len(t.deliveredFiles)
	t.attMu.Unlock()
	return ev
}

// reviewExcerpt is the head and tail of a result: what a call says it did is
// often in the notes it ends with.
func reviewExcerpt(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 600 {
		return string(r)
	}
	return string(r[:420]) + " … " + string(r[len(r)-160:])
}

// leadReviewEvidence is the reviewer's view of the turn. Built for this
// question rather than borrowed from the truth check's message, which carries
// instructions about what that judge must call KEPT.
func leadReviewEvidence(ev TurnClaimEvidence) string {
	ev.ToolOutputs = keptLocalOutputs(ev.ToolOutputs)
	var b strings.Builder
	fmt.Fprintf(&b, "WHAT THEY ASKED:\n%s\n\n", truncateObs(strings.TrimSpace(ev.Request), 2000))
	if now := strings.TrimSpace(ev.Now); now != "" {
		fmt.Fprintf(&b, "THE CURRENT TIME, AS THE ASSISTANT WAS GIVEN IT: %s\n\n", now)
	}
	ran := "none"
	if len(ev.ToolCalls) > 0 {
		ran = strings.Join(ev.ToolCalls, ", ")
	}
	fmt.Fprintf(&b, "WHAT THE TURN RAN, IN ORDER: %s\n", ran)
	if outs := ev.ReturnsBlock(); outs != "" {
		b.WriteString(outs)
	}
	if len(ev.PriorWork) > 0 {
		fmt.Fprintf(&b, "ALSO DONE FOR THIS TURN BEFORE THE ASSISTANT ANSWERED: %s\n", strings.Join(ev.PriorWork, "; "))
	}
	if len(ev.PriorTurnWork) > 0 {
		fmt.Fprintf(&b, "WHAT EARLIER TURNS OF THIS CONVERSATION RAN (not this turn): %s\n", strings.Join(ev.PriorTurnWork, ", "))
	}
	fmt.Fprintf(&b, "CALLS THAT FAILED: %d\n", ev.ToolErrors)
	fmt.Fprintf(&b, "FILES GOING OUT WITH THE REPLY: %d\n\n", ev.Delivered)
	fmt.Fprintf(&b, "THE REPLY:\n%s\n", strings.TrimSpace(ev.Reply))
	return b.String()
}
