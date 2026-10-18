package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// An agent suggesting one of its own rules.
//
// Rules are the owner's: the always-applied operating policy at the top of the
// agent's prompt. An agent learns things its owner would want there (a thread
// it keeps re-commenting on, a mistake it made twice), and its own notes are
// where it wrote them down, which is advisory and was ignored. So it may
// propose a rule, and the owner adds it or not.
//
// It never writes one itself, even add-only. A new rule can undo an old one
// without removing it ("the limit on links does not apply to reports"), and an
// agent that reads outside content all day would carry an injected sentence
// at the top of every prompt from then on. Proposed, the owner reads it first,
// told when the turn that proposed it read outside content or was talking to
// someone else.

// Reserved so a temp tool cannot take the name and shadow it.
func init() { RegisterReservedToolName("propose_rule") }

// proposeRuleAction is the approvals-queue entry for a proposed rule.
const proposeRuleAction = "propose_rule"

// proposeRulePendingMax bounds an agent's unanswered proposals, so one that
// proposes on every turn fills a few rows of the owner's queue, not a page.
const proposeRulePendingMax = 5

// proposeRuleMaxRunes keeps a proposal to one rule.
const proposeRuleMaxRunes = 400

func (t *chatTurn) proposeRuleToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "propose_rule",
			Description: "Suggest a rule for your owner to add to your Rules, the operating policy you follow on every turn. Use it when you learn something that should bind every future turn and keep catching yourself on it: a mistake you made twice, a line you were told not to cross, a thread or contact to leave alone. You do not add it: your owner reads it and adds it or not, and until they do it is not one of your rules. One rule per call, in the words it should have, plus why. Never use it to relax or work around a rule you have.",
			Parameters: map[string]ToolParam{
				"rule": {Type: "string", Description: "The rule as it should read in your Rules, one or two sentences, e.g. \"Before replying on a thread, check it against ENGAGED in your notes; a listed thread gets no new comment.\""},
				"why":  {Type: "string", Description: "What happened that makes it worth a rule, in a sentence."},
			},
			Required: []string{"rule", "why"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return t.proposeRule(stringArg(args, "rule"), stringArg(args, "why"))
		},
	}
}

// proposeRule asks the owner on the spot when they are the one in the
// conversation, and otherwise files the proposal in their approvals.
func (t *chatTurn) proposeRule(rule, why string) (string, error) {
	rule, why = strings.TrimSpace(rule), strings.TrimSpace(why)
	if rule == "" {
		return "", fmt.Errorf("rule is required: the rule as it should read")
	}
	if len([]rune(rule)) > proposeRuleMaxRunes {
		return "", fmt.Errorf("that is more than one rule (over %d characters): propose them one at a time", proposeRuleMaxRunes)
	}
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	rec, ok := loadAgent(db, t.agent.ID)
	if !ok {
		return "", fmt.Errorf("your agent record could not be read, so nothing was proposed")
	}
	if hasRule(rec.Rules, rule) {
		return "That is already one of your rules.", nil
	}
	flags := t.proposalFlags()
	if t.requester().Owner && (t.sse != nil || t.askVia != nil) {
		detail := rule
		if why != "" {
			detail += "\n\nWhy: " + why
		}
		if flags != "" {
			detail += "\n\n" + flags
		}
		detail += "\n\nIt joins the agent's Rules, which apply on every turn."
		if t.askInChat(fmt.Sprintf("Add a rule %s suggested?", chFirst(t.agent.Name, "this agent")), detail, []string{"Add it"}) == "" {
			return "Not added: the owner declined it. Do not propose it again in this conversation; carry on under your current rules.", nil
		}
		if err := addAgentRule(db, t.agent.ID, rule); err != nil {
			return "", err
		}
		return "Added to your Rules. It applies from your next turn.", nil
	}
	owner := chFirst(strings.TrimSpace(t.ownerUser), t.user)
	pending := 0
	for _, a := range ListAuthorizations(RootDB, owner) {
		if a.Action != proposeRuleAction || a.Agent != t.agent.ID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Brief), rule) {
			return "You have already proposed that; it is waiting for your owner.", nil
		}
		pending++
	}
	if pending >= proposeRulePendingMax {
		return fmt.Sprintf("Not proposed: %d of your proposals are already waiting for your owner. Carry on under your current rules.", pending), nil
	}
	text := why
	if flags != "" {
		text = strings.TrimSpace(text + " " + flags)
	}
	SaveAuthorization(RootDB, Authorization{
		Owner:  owner,
		Action: proposeRuleAction,
		Agent:  t.agent.ID,
		Brief:  rule,
		Text:   text,
	})
	Log("[orchestrate.rules] %s: %s proposed a rule, queued for the owner", owner, t.agent.ID)
	return "Proposed. It is waiting in your owner's approvals and is not one of your rules until they add it, so work under your current rules meanwhile.", nil
}

// proposalFlags says what the owner should know about where a proposal came
// from: a turn that read outside content, or one with someone else.
func (t *chatTurn) proposalFlags() string {
	var notes []string
	if t.turnTainted() {
		notes = append(notes, "The turn that proposed it read content from outside the system, which can carry instructions meant for the agent.")
	}
	if !t.requester().Owner {
		notes = append(notes, "It was proposed in a conversation with someone other than you.")
	}
	return strings.Join(notes, " ")
}

// hasRule reports whether rules already holds rule as one of its lines.
func hasRule(rules, rule string) bool {
	want := strings.ToLower(strings.TrimSpace(rule))
	for _, line := range strings.Split(rules, "\n") {
		if strings.ToLower(strings.TrimSpace(line)) == want {
			return true
		}
	}
	return false
}

// addAgentRule appends rule to the agent's Rules. The save is filed in the
// agent's history as a rule it suggested, which is where an owner reading
// back sees which rules came from the agent.
func addAgentRule(db Database, agentID, rule string) error {
	rec, ok := loadAgent(db, agentID)
	if !ok {
		return fmt.Errorf("no agent %q", agentID)
	}
	if hasRule(rec.Rules, rule) {
		return nil
	}
	if r := strings.TrimRight(rec.Rules, "\n "); r != "" {
		rec.Rules = r + "\n" + strings.TrimSpace(rule)
	} else {
		rec.Rules = strings.TrimSpace(rule)
	}
	if _, err := saveAgentAs(db, rec, "added a rule the agent suggested"); err != nil {
		return fmt.Errorf("saving the rule: %w", err)
	}
	return nil
}
