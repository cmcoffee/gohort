// Ongoing-conversation compaction for the Operator (orchestrator-mode agents).
//
// The Operator is one continuous thread, so its history would grow unbounded.
// compactOperatorHistory gives the RUN a bounded view — a running-summary
// message + the recent verbatim tail — while STORAGE keeps the full history
// (the caller saved it before calling this). When the thread grows past the
// threshold it folds the aging turns into the summary (worker LLM) and evicts
// durable facts into the agent's memory. Built on the shared core engine
// (core/conversation_compaction.go); gated to orchestrator agents at the call
// site in handleSend.

package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const operatorCompactStateTable = "operator_compact_state"

func compactStateKey(agentID, sessID string) string { return agentID + ":" + sessID }

func loadCompactState(db Database, agentID, sessID string) CompactState {
	var st CompactState
	if db != nil {
		db.Get(operatorCompactStateTable, compactStateKey(agentID, sessID), &st)
	}
	return st
}

func saveCompactState(db Database, agentID, sessID string, st CompactState) {
	if db != nil {
		db.Set(operatorCompactStateTable, compactStateKey(agentID, sessID), st)
	}
}

// compactOperatorHistory returns the bounded run-view of the conversation: a
// leading running-summary message (when one exists) + the recent tail. Folds +
// stores facts when the thread has grown past the trigger. Run-only — storage
// keeps the full history.
func (T *OrchestrateApp) compactOperatorHistory(udb Database, owner string, agent AgentRecord, sessID string, msgs []ChatMessage) []ChatMessage {
	if T.LLM == nil || len(msgs) == 0 {
		return msgs
	}
	cm := make([]Message, len(msgs))
	for i, m := range msgs {
		cm[i] = Message{Role: m.Role, Content: m.Content}
	}
	st := loadCompactState(udb, agent.ID, sessID)
	cfg := CompactionConfig{} // defaults
	fold := func(ctx context.Context, aging []Message, prior string) (string, []string, error) {
		return T.operatorFold(ctx, aging, prior)
	}
	newSt, facts, changed, err := CompactConversation(context.Background(), cm, st, cfg, fold)
	if err == nil && changed {
		saveCompactState(udb, agent.ID, sessID, newSt)
		for _, f := range facts {
			if f = strings.TrimSpace(f); f != "" {
				StoreMemoryFact(udb, factsNamespace(agent.ID), f)
			}
		}
		st = newSt
	}

	block, _ := CompactedView(cm, st, cfg)
	through := st.SummarizedThrough
	if through < 0 {
		through = 0
	}
	if through > len(msgs) {
		through = len(msgs)
	}
	tail := msgs[through:]
	out := make([]ChatMessage, 0, len(tail)+1)
	if strings.TrimSpace(block) != "" {
		out = append(out, ChatMessage{Role: "user", Content: block})
	}
	out = append(out, tail...)
	return out
}

// operatorFold is the worker-LLM summarizer: extend the running summary with
// the aging span and surface a few durable facts.
func (T *OrchestrateApp) operatorFold(ctx context.Context, aging []Message, prior string) (string, []string, error) {
	var sb strings.Builder
	if p := strings.TrimSpace(prior); p != "" {
		sb.WriteString("EXISTING SUMMARY:\n")
		sb.WriteString(p)
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString("NEW EXCHANGES TO FOLD IN (oldest first):\n\n")
	for _, m := range aging {
		t := strings.TrimSpace(m.Content)
		if t == "" {
			continue
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&sb, "[%s] %s\n", role, t)
	}

	sys := "You maintain a running summary of an ongoing operator conversation. EXTEND the existing summary with the new exchanges (do not restart). Preserve: decisions, standing jobs created or changed, facts about the user's systems, and open threads. Drop chitchat and exact wording. Keep the summary under ~500 tokens. Then add a final line beginning with 'FACTS:' listing 0-5 short durable facts worth remembering verbatim, separated by semicolons (or 'FACTS: none')."
	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: sb.String()}}, WithSystemPrompt(sys), WithMaxTokens(700))
	if err != nil || resp == nil {
		return "", nil, err
	}
	summary, facts := splitSummaryAndFacts(resp.Content)
	return summary, facts, nil
}

// splitSummaryAndFacts pulls the trailing "FACTS: a; b; c" line off the model
// output, returning the summary body + parsed facts.
func splitSummaryAndFacts(out string) (string, []string) {
	out = strings.TrimSpace(out)
	idx := strings.LastIndex(out, "FACTS:")
	if idx < 0 {
		return out, nil
	}
	summary := strings.TrimSpace(out[:idx])
	factLine := strings.TrimSpace(out[idx+len("FACTS:"):])
	if factLine == "" || strings.EqualFold(factLine, "none") {
		return summary, nil
	}
	var facts []string
	for _, f := range strings.Split(factLine, ";") {
		if f = strings.TrimSpace(f); f != "" && !strings.EqualFold(f, "none") {
			facts = append(facts, f)
		}
	}
	return summary, facts
}
