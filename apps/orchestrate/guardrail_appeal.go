// Guardrail appeals — the third answer to a blocked action, after "no" and
// "try again".
//
// The warden judges one candidate in fresh context. That is what makes it hard
// to talk out of, and it is also why a rule carrying a PRECONDITION defeats it:
// "don't tell a joke unless it's requested twice" is unjudgeable from a joke,
// because the thing that would excuse the joke is in turns the warden never
// sees. Observed: the agent counted correctly, called get_joke on the second
// ask, and was blocked; the correctable rewrite met the same blind warden and
// was blocked again; the user asked twice and got a decline.
//
// Correctable cannot help there, and the reason is worth stating exactly: it
// offers another attempt at SATISFYING the rule, which buys nothing when the
// rule was already satisfied. The missing move is "this rule does not apply
// here" — plus something other than the warden's credence to settle it.
//
// THE DESIGN RULE: an appeal is a CITATION, never an ARGUMENT.
//
// Argument must not work. A socially-engineered agent is the more fluent one —
// the engineering is what gave it a story — so a channel where the better case
// wins is a channel that inverts under attack. A citation is different: the
// agent names something to look up, this file looks it up deterministically,
// and the warden is told what the FRAMEWORK found. The agent chooses the
// question and never touches the answer. Its best move when wrong is to cite
// something irrelevant, which costs it a round and buys nothing.
//
// Admissible sources are USER turns only. An assistant turn is the agent
// quoting itself, which would launder its own earlier claim into corroboration
// — the same hole as agent-written memory, one hop further round.

package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// guardrailAppealOffer is a live invitation to dispute one block.
type guardrailAppealOffer struct {
	Rule      string // the rule as the warden named it
	Hook      string // where it fired
	Candidate string // what was judged, so the re-check judges the same thing
}

// appealQuoteMinLen is the shortest quote worth resolving. A one- or two-letter
// fragment matches everywhere and would let an appeal "verify" anything; the
// floor makes a citation name something a person actually said.
const appealQuoteMinLen = 4

// offerGuardrailAppeal records that a contestable rule just blocked, so the
// tool has something to act on, and returns the sentence inviting the appeal.
// Empty when the rule is not contestable or the turn already spent its appeal —
// in which case the block message stays exactly as it was.
func (t *chatTurn) offerGuardrailAppeal(rule, hook, candidate string) string {
	if t == nil || t.appealSpent || !ruleIsContestable(t.agent, rule) {
		return ""
	}
	t.appealOffer = &guardrailAppealOffer{Rule: rule, Hook: hook, Candidate: candidate}
	return " If this rule genuinely does not apply here — its condition was already met — you may say so ONCE by calling guardrail_appeal with a quote from the user's own words that shows it. A quote, not an explanation: the framework looks it up and decides. If you have no such quote, comply instead."
}

// appealCleared reports whether an appeal has already cleared this rule for the
// rest of the turn.
func (t *chatTurn) appealCleared(rule string) bool {
	if t == nil || len(t.appealWon) == 0 {
		return false
	}
	want := normalizeRuleText(rule)
	return want != "" && t.appealWon[want]
}

// countUserQuote returns how many of the session's USER turns contain the
// quote, and one matching excerpt for the warden to read.
//
// Deterministic and case-insensitive, with whitespace folded so a quote broken
// across a line still matches. No LLM anywhere in this path — that is the whole
// security property: a quote that was never said is simply not found, and no
// amount of conviction in the appeal changes the count.
func (t *chatTurn) countUserQuote(quote string) (int, string) {
	needle := strings.Join(strings.Fields(strings.ToLower(quote)), " ")
	if len(needle) < appealQuoteMinLen || t == nil || t.session == nil {
		return 0, ""
	}
	n, excerpt := 0, ""
	for _, m := range t.session.Messages {
		// USER turns only — see the header. An assistant turn matching its own
		// words proves the agent repeated itself, nothing more.
		if m.Role != "user" {
			continue
		}
		hay := strings.Join(strings.Fields(strings.ToLower(m.Content)), " ")
		if !strings.Contains(hay, needle) {
			continue
		}
		n++
		if excerpt == "" {
			excerpt = strings.TrimSpace(m.Content)
			if len(excerpt) > 300 {
				excerpt = excerpt[:300] + "…"
			}
		}
	}
	return n, excerpt
}

// guardrailAppealToolDef is the appeal channel. Mounted only for agents that
// have a contestable rule, and refuses unless a block is actually pending —
// "only after a block" is enforced here rather than left to the description,
// because a tool that merely ASKS not to be used speculatively will be.
func (t *chatTurn) guardrailAppealToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "guardrail_appeal",
			Description: "Dispute a block you have just received, when the rule's own condition was already met. " +
				"Only usable right after a block that invited an appeal, and only once per turn. " +
				"You must cite the user's OWN WORDS: pass `quote` with the exact phrase they used. " +
				"The framework searches the conversation for it and decides — an explanation of why you are right is not evidence and will not be looked at. " +
				"If you cannot point at something the user actually said, comply with the block instead.",
			Parameters: map[string]ToolParam{
				"claim": {
					Type:        "string",
					Description: "One line: which condition of the rule is already satisfied. Example: \"the user requested a joke twice\".",
				},
				"quote": {
					Type:        "string",
					Description: "Exact words the USER wrote that establish it, e.g. \"tell me a joke\". Short and verbatim beats long and paraphrased — a paraphrase will not be found.",
				},
			},
			Required: []string{"claim", "quote"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			offer := t.appealOffer
			if offer == nil {
				return "There is nothing to appeal — no guardrail has blocked you. Carry on with the work.", nil
			}
			if t.appealSpent {
				return "You have already appealed once this turn. The block stands; comply with it.", nil
			}
			// Spent on ATTEMPT, not on success. An appeal that can be retried
			// until it lands is a negotiation, which is the thing this must not
			// become.
			t.appealSpent = true
			t.appealOffer = nil

			claim := strings.TrimSpace(stringArg(args, "claim"))
			quote := strings.TrimSpace(stringArg(args, "quote"))
			if len(strings.Join(strings.Fields(quote), " ")) < appealQuoteMinLen {
				t.turnDiag("guardrail-appeal-failed", fmt.Sprintf(
					"Appeal against %q offered no usable quote — the block stands.", offer.Rule))
				return "That is not a citation. Quote the user's own words, or comply with the block.", nil
			}
			n, excerpt := t.countUserQuote(quote)
			if n == 0 {
				t.turnDiag("guardrail-appeal-failed", fmt.Sprintf(
					"Appeal against %q cited %q, which appears nowhere in the user's turns — the block stands.", offer.Rule, quote))
				Log("[orchestrate.guardrail] agent=%s appeal FAILED (rule=%q, quote not found)", t.agent.ID, offer.Rule)
				return "That quote does not appear anywhere in the user's messages, so it establishes nothing. The block stands — comply with it.", nil
			}

			// The finding is the framework's, phrased as fact. The warden is
			// re-run rather than the block simply lifted: whether the count
			// satisfies THIS rule is a reading of the rule, and that judgment
			// stays with the judge. All this changes is that it is no longer
			// being made blind.
			finding := fmt.Sprintf(
				"the phrase %q appears in %d separate USER message(s) in this conversation (the agent claims: %s). One of them reads: %q",
				quote, n, claim, excerpt)
			verdicts, err := t.app.runWardenWithFinding(t.ctx, t.agent, offer.Hook, offer.Candidate, t.requester(), finding)
			if err != nil {
				t.turnDiag("guardrail-appeal-failed", fmt.Sprintf(
					"Appeal against %q could not be re-checked (%v) — the block stands.", offer.Rule, err))
				return "The appeal could not be checked. The block stands — comply with it.", nil
			}
			if worstVerdict(verdicts) == guardViolate {
				_, reason := firstViolation(verdicts)
				t.turnDiag("guardrail-appeal-failed", fmt.Sprintf(
					"Appeal against %q was re-checked with the verified quote (%d user message(s)) and still violates: %s", offer.Rule, n, reason))
				Log("[orchestrate.guardrail] agent=%s appeal REJECTED (rule=%q, matches=%d)", t.agent.ID, offer.Rule, n)
				return "Re-checked with your quote, and it still breaks the rule. The block stands — comply with it.", nil
			}

			if t.appealWon == nil {
				t.appealWon = map[string]bool{}
			}
			t.appealWon[normalizeRuleText(offer.Rule)] = true
			// An overturned guardrail is exactly the thing an owner should see
			// in the trail — it is the one outcome where enforcement was WRONG,
			// and it is invisible from the reply, which just looks like a normal
			// answer.
			t.turnDiag("guardrail-appeal-upheld", fmt.Sprintf(
				"Guardrail %q blocked a %s check, and the agent appealed: it cited %q, which the framework found in %d user message(s). Re-checked with that finding, the rule reads as satisfied and the block was lifted for the rest of this turn.",
				offer.Rule, offer.Hook, quote, n))
			Log("[orchestrate.guardrail] agent=%s appeal UPHELD (rule=%q, matches=%d)", t.agent.ID, offer.Rule, n)
			return fmt.Sprintf("Appeal upheld — %q appears in %d of the user's messages, so the rule's condition is met. Go ahead with what you were doing.", quote, n), nil
		},
	}
}

// agentHasContestableRule reports whether any of this agent's rules may be
// appealed, which is what decides whether the tool is mounted at all.
func agentHasContestableRule(agent AgentRecord) bool {
	for _, r := range guardrailRules(agent) {
		if r.Contestable {
			return true
		}
	}
	return false
}
