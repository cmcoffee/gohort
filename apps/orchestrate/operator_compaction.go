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

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_compaction_fold_trigger_pct",
		Category: "Limits",
		Label:    "Compaction fold trigger (% of context depth)",
		Help:     "How far a persistent thread's unsummarized tail may grow, as a percent of the agent's Context Depth, before the rolling summary folds it back down to the depth. 150 = fold at 1.5x depth, so the prompt floats between depth and 1.5x depth (e.g. depth 100 → prompt stays 100-150). Higher folds less often (looser prompt, fewer summary LLM calls); lower keeps the prompt tighter at the cost of more frequent folds. Was effectively 300 (3x) before this was configurable.",
		Kind:     KindInt,
		Default:  150,
		Min:      110,
		Max:      300,
	})
}

// foldTriggerPct is the unsummarized-tail size (as a percent of the agent's
// context depth) at which the rolling summary folds. Replaces the old hardcoded
// 3x (300%) core default so "context depth N" keeps the prompt near N instead of
// drifting to 3N before the first fold.
func foldTriggerPct() int { return TuneInt("tune_compaction_fold_trigger_pct") }

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
	// Per-agent context depth = the verbatim recent-message tail (0 = the
	// framework default of 12). Applies to any persistent thread the agent
	// runs — its Cortex home thread and each Channel room alike.
	keepRecent := agent.ContextDepth
	// Compaction off: no rolling summary. Keep the recent tail verbatim and
	// forget older messages (still bounded — storage keeps the full thread;
	// this just chooses forget-old over summarize-old).
	if agent.DisableCompaction {
		k := keepRecent
		if k <= 0 {
			k = 12
		}
		if len(msgs) > k {
			return msgs[len(msgs)-k:]
		}
		return msgs
	}
	cm := make([]Message, len(msgs))
	for i, m := range msgs {
		cm[i] = Message{Role: m.Role, Content: m.Content}
	}
	st := loadCompactState(udb, agent.ID, sessID)
	// Self-heal corrupted state: SummarizedThrough counts leading messages
	// already folded into the summary, so it must never exceed the stored
	// history length. If it does, the stored thread was truncated BELOW the
	// cursor out from under us (the symptom of an earlier bug that wrote the
	// bounded run-view back to storage). Left as-is, clampThrough would pin
	// the cursor to len(msgs) and the verbatim tail — including the user's
	// just-typed message — would render empty, so the model answers from the
	// stale summary alone. Reset the cursor to 0 (KEEP the summary as recall)
	// so every retained message re-enters the verbatim tail. Mild, one-time
	// overlap between the summary and the shown tail is far cheaper than
	// silently dropping the live turn.
	if st.SummarizedThrough > len(cm) {
		Log("[operator.compact] cursor %d > history %d for %s:%s — resetting fold cursor (corrupted state recovery)",
			st.SummarizedThrough, len(cm), agent.ID, sessID)
		st.SummarizedThrough = 0
		saveCompactState(udb, agent.ID, sessID, st)
	}
	// Trigger = fold when the unsummarized tail reaches foldTriggerPct% of the
	// verbatim depth. Set explicitly so core's withDefaults doesn't fall back to
	// its 3x default (which let a depth-100 thread grow to ~300 before folding —
	// the "why is the prompt 186 when depth is 100" surprise). Compute off the
	// EFFECTIVE depth (12 when ContextDepth is unset) so it scales with either.
	effectiveKeep := keepRecent
	if effectiveKeep <= 0 {
		effectiveKeep = 12
	}
	cfg := CompactionConfig{
		KeepRecent: keepRecent, // 0 → withDefaults applies 12
		Trigger:    effectiveKeep * foldTriggerPct() / 100,
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
		// Fact extraction respects the agent's Reference Memory setting. A
		// memory-disabled run (e.g. a phantom: dispatch with DisableInferred
		// forced on) still gets its history BOUNDED — folding the aging span
		// into a summary — but the folded span doesn't seed facts. Without
		// this gate, generalizing compaction to the dispatch path would
		// silently revive fact storage on memory-off agents.
		if !agent.DisableInferred {
			for _, f := range facts {
				if f = strings.TrimSpace(f); f != "" {
					// Worker chat enables supersession: a fact distilled from a
					// folded span that updates an earlier one replaces it rather
					// than coexisting as a contradiction.
					StoreMemoryFact(udb, factsNamespace(agent.ID), f, T.WorkerChat)
				}
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

// Stored-thread caps. compactOperatorHistory bounds the RUN view; these bound
// what's PERSISTED, so a long-lived Cortex / channel thread doesn't grow without
// limit (it's loaded whole every turn and re-read by the cortex poll). Older
// folded content stays recoverable via the recall index it was archived to.
const (
	storedHistoryCap        = 150 // verbatim messages kept in the stored thread
	storedHistoryCapNoEmbed = 500 // bigger when embeddings are off — recall is then substring-only, so retain more verbatim
	storedHistorySlack      = 32  // only trim once over cap+slack, so it batches instead of dropping one per turn
)

// trimStoredHistory bounds the STORED thread (distinct from compactOperatorHistory's
// run-view). With compaction ON it drops only leading messages already folded into
// the summary AND archived to recall (capped at the fold cursor), decrementing the
// cursor in lockstep so it keeps indexing the now-shorter array — the consistency
// the run-view depends on (and the exact desync the earlier bug was). With
// compaction OFF (forget-old) there's no summary/archive, so it keeps the last
// `keep` verbatim and the rest is genuinely forgotten — consistent with that
// agent's choice. No-op below the cap. Returns the slice to persist.
func (T *OrchestrateApp) trimStoredHistory(udb Database, agent AgentRecord, sessID string, msgs []ChatMessage) []ChatMessage {
	keep := storedHistoryCap
	if !GetEmbeddingConfig().Enabled {
		keep = storedHistoryCapNoEmbed
	}
	if len(msgs) <= keep+storedHistorySlack {
		return msgs
	}
	if agent.DisableCompaction {
		Log("[operator.compact] %s:%s storage capped to %d (compaction off; older forgotten)", agent.ID, sessID, keep)
		return msgs[len(msgs)-keep:]
	}
	st := loadCompactState(udb, agent.ID, sessID)
	if st.SummarizedThrough <= 0 {
		return msgs // nothing folded yet → nothing safe to drop
	}
	drop := len(msgs) - keep
	if drop > st.SummarizedThrough {
		drop = st.SummarizedThrough // never drop a message that isn't in the summary + archive
	}
	if drop <= 0 {
		return msgs
	}
	st.SummarizedThrough -= drop
	saveCompactState(udb, agent.ID, sessID, st)
	Log("[operator.compact] %s:%s storage trimmed %d folded leading msgs (kept %d + summary; older in recall)", agent.ID, sessID, drop, len(msgs)-drop)
	return msgs[drop:]
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
