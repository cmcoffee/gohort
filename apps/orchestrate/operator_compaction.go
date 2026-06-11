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

// deleteCompactState drops a session's rolling summary + fold cursor. Without
// this, clearing the chat history leaves the summary behind — and since the
// summary is prepended to every turn, the thread keeps its old "personality"
// even after a wipe (e.g. a flood of one monitor's wakes that crystallized into
// the summary). Called from deleteChatSession so "clear thread" is complete.
// Harmless no-op for non-orchestrator agents (their sessions have no row here).
func deleteCompactState(db Database, agentID, sessID string) {
	if db != nil {
		db.Unset(operatorCompactStateTable, compactStateKey(agentID, sessID))
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
	cfg := CompactionConfig{
		// Archive each folded span into a searchable history index so aged
		// content stays recoverable via recall_history / expand_history,
		// instead of surviving only as the lossy running summary.
		OnFold: func(folded []Message, firstIndex int) {
			T.archiveOperatorSpan(udb, agent.ID, sessID, folded, firstIndex)
		},
	}
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
	tail := collapseMonitorWakes(msgs[through:], 3)
	out := make([]ChatMessage, 0, len(tail)+1)
	if strings.TrimSpace(block) != "" {
		out = append(out, ChatMessage{Role: "user", Content: block})
	}
	out = append(out, tail...)
	return out
}

// monitorWakePrefix marks a turn injected by an event-monitor wake (see the
// waker in operator_wake.go). A frequently-firing monitor would otherwise
// flood the verbatim tail with near-identical wake turns, which trains the
// worker model to mimic the wake pattern — reflexive list_runs + a canned
// greeting — instead of engaging the user on their next real message. That is
// exactly the "channel crystallized" failure. We collapse long runs.
const monitorWakePrefix = "[EVENT — monitor"

// collapseMonitorWakes replaces a maximal CONTIGUOUS run of monitor-wake turns
// (each a wake user-message plus its assistant reply) with a single marker,
// keeping only the most recent keepRecent wakes verbatim. Non-wake turns and
// short runs pass through untouched; order is preserved. Only contiguous runs
// collapse, so a wake the user actually replied to between is never folded.
func collapseMonitorWakes(msgs []ChatMessage, keepRecent int) []ChatMessage {
	if keepRecent < 0 {
		keepRecent = 0
	}
	isWakeUser := func(m ChatMessage) bool {
		return m.Role == "user" && strings.HasPrefix(strings.TrimSpace(m.Content), monitorWakePrefix)
	}
	out := make([]ChatMessage, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		if !isWakeUser(msgs[i]) {
			out = append(out, msgs[i])
			i++
			continue
		}
		// Gather a maximal contiguous run of wake units (wake user + its reply).
		var units [][]ChatMessage
		for i < len(msgs) && isWakeUser(msgs[i]) {
			end := i + 1
			if end < len(msgs) && msgs[end].Role == "assistant" {
				end++
			}
			units = append(units, msgs[i:end])
			i = end
		}
		if len(units) <= keepRecent {
			for _, u := range units {
				out = append(out, u...)
			}
			continue
		}
		omitted := len(units) - keepRecent
		out = append(out, ChatMessage{Role: "user",
			Content: fmt.Sprintf("[%d earlier monitor wakes omitted — already handled, nothing pending]", omitted)})
		for _, u := range units[len(units)-keepRecent:] {
			out = append(out, u...)
		}
	}
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

// operatorLCMSource is the vector-store source tag for one operator thread's
// archived history spans — stable per (agent, session) so recall can filter to
// exactly this thread's continuity archive.
func operatorLCMSource(agentID, sessID string) string {
	return "lcm:" + agentID + ":" + sessID
}

// archiveOperatorSpan ingests a folded conversation span into the searchable
// history index (the LCM-style "continuity archive"), so content that has left
// both the verbatim window and the running summary stays recoverable via
// recall_history / expand_history. Stored in the SAME per-user db as the session
// and its facts. Messages that look like they carry a credential are redacted
// before indexing (defense-in-depth — the thread is the owner's, but tool output
// can contain secrets, mirroring the MaskDebugOutput posture).
func (T *OrchestrateApp) archiveOperatorSpan(udb Database, agentID, sessID string, folded []Message, firstIndex int) {
	if udb == nil || len(folded) == 0 {
		return
	}
	var b strings.Builder
	for _, m := range folded {
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&b, "[%s] %s\n\n", role, text)
	}
	body := strings.TrimSpace(b.String())
	if body == "" {
		return
	}
	source := operatorLCMSource(agentID, sessID)
	reportID := fmt.Sprintf("%s#%d", source, firstIndex)
	title := fmt.Sprintf("operator history — messages %d–%d", firstIndex, firstIndex+len(folded)-1)
	// Shared core primitive: redacts secret-shaped lines, then ingests.
	IngestRecallSpan(context.Background(), udb, source, reportID, title, body, "lcm")
	Log("[operator.lcm] archived span %s (%d msgs)", reportID, len(folded))
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
