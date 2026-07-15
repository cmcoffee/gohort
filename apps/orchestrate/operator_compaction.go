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
	"sync"

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
	}
	// Fold OFF the hot path: when the unsummarized tail has outgrown the
	// trigger, the fold pipeline (worker summarize + span archive + fact
	// stores) runs in the BACKGROUND and this turn renders off the pre-fold
	// view — previously it all ran synchronously before the turn's first
	// token, so the fold turn ate seconds of latency. The prompt floats one
	// fold-cycle higher until the fold lands (the next turn picks up the new
	// summary); single-flight per thread, so a busy session schedules at most
	// one.
	through := st.SummarizedThrough
	if through < 0 {
		through = 0
	}
	if len(cm)-through > cfg.Trigger {
		T.maybeFoldOperatorHistory(udb, agent, sessID, cfg)
	}

	block, _ := CompactedView(cm, st, cfg)
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

// operatorFoldInFlight single-flights the background fold per (agent, session).
// trimStoredHistory also consults it: a fold computes its cursor against the
// thread it reloaded, so a concurrent trim rebasing indices under it would
// leave the saved cursor too high — and messages the cursor claims are folded
// (but aren't) eventually get trimmed away UN-archived. Trim defers instead
// (it batches anyway; next turn retries).
var (
	operatorFoldMu       sync.Mutex
	operatorFoldInFlight = map[string]bool{}
)

func operatorFoldKey(agentID, sessID string) string { return agentID + ":" + sessID }

func operatorFoldBusy(agentID, sessID string) bool {
	operatorFoldMu.Lock()
	defer operatorFoldMu.Unlock()
	return operatorFoldInFlight[operatorFoldKey(agentID, sessID)]
}

// maybeFoldOperatorHistory launches the background fold for one thread,
// single-flight. Mirrors maybeSweepFacts / maybeExtractGraph: best-effort,
// never blocks the caller, panics contained.
func (T *OrchestrateApp) maybeFoldOperatorHistory(udb Database, agent AgentRecord, sessID string, cfg CompactionConfig) {
	key := operatorFoldKey(agent.ID, sessID)
	operatorFoldMu.Lock()
	if operatorFoldInFlight[key] {
		operatorFoldMu.Unlock()
		return
	}
	operatorFoldInFlight[key] = true
	operatorFoldMu.Unlock()
	go func() {
		defer func() {
			operatorFoldMu.Lock()
			delete(operatorFoldInFlight, key)
			operatorFoldMu.Unlock()
			if r := recover(); r != nil {
				Log("[operator.compact] background fold panic for %s: %v", key, r)
			}
		}()
		T.foldOperatorHistory(udb, agent, sessID, cfg)
	}()
}

// foldOperatorHistory runs one fold cycle for a thread: reload the stored
// messages FRESH (the turn that scheduled this is still appending — appended
// tail messages sit beyond the fold window and are harmless; leading indices
// can't move because trim defers while this runs), summarize the aging span,
// archive it, persist the new cursor, and store the surfaced facts.
func (T *OrchestrateApp) foldOperatorHistory(udb Database, agent AgentRecord, sessID string, cfg CompactionConfig) {
	sess, ok := loadChatSession(udb, agent.ID, sessID)
	if !ok || len(sess.Messages) == 0 {
		return
	}
	cm := make([]Message, len(sess.Messages))
	for i, m := range sess.Messages {
		cm[i] = Message{Role: m.Role, Content: m.Content}
	}
	st := loadCompactState(udb, agent.ID, sessID)
	if st.SummarizedThrough > len(cm) {
		st.SummarizedThrough = 0 // same corrupted-state recovery as the view path
	}
	// Archive each folded span into a searchable history index so aged
	// content stays recoverable via recall_history / expand_history,
	// instead of surviving only as the lossy running summary. A failed
	// archive aborts the fold (error return) so the cursor never advances
	// past content that isn't actually in the index — trimStoredHistory
	// would otherwise hard-drop it later on the strength of that cursor.
	// Spans are keyed by the fold counter, not firstIndex: indices rebase
	// when the stored thread is trimmed, and a recurring index would
	// overwrite an older span on re-ingest.
	cfg.OnFold = func(folded []Message, firstIndex int) error {
		return T.archiveOperatorSpan(udb, agent.ID, sessID, st.FoldSeq, folded, firstIndex)
	}
	fold := func(ctx context.Context, aging []Message, prior string) (string, []string, error) {
		return T.operatorFold(ctx, aging, prior)
	}
	newSt, facts, changed, err := CompactConversation(context.Background(), cm, st, cfg, fold)
	if err != nil {
		Log("[operator.compact] fold failed for %s:%s (cursor held, will retry): %v", agent.ID, sessID, err)
		return
	}
	if !changed {
		return
	}
	saveCompactState(udb, agent.ID, sessID, newSt)
	// Fact extraction respects BOTH memory toggles: DisableInferred (the
	// fold facts are model-inferred — a phantom: dispatch with it forced
	// on still gets its history bounded, just no fact seeding) AND
	// DisableExplicit (the write lands in the always-in-prompt Explicit
	// store, which this gate previously ignored — the least-governed
	// writer into the most expensive store).
	if !agent.DisableInferred && !agent.DisableExplicit {
		for _, f := range facts {
			if f = strings.TrimSpace(f); f != "" {
				// Full policy, same as the model's own store_fact: worker
				// chat enables supersession (a distilled fact that updates
				// an earlier one replaces it), Mode engages the relevance
				// gate in chatbot mode, and Source=observed keeps these
				// model-authored notes OUT of the grounding corpus.
				StoreMemoryFactP(udb, factsNamespace(agent.ID), f, FactWritePolicy{
					Mode:   agent.MemoryMode,
					Chat:   T.WorkerChat,
					Source: MemSourceObserved,
				})
			}
		}
	}
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
	if operatorFoldBusy(agent.ID, sessID) {
		// A background fold is computing its cursor against the thread as it
		// reloaded it — rebasing indices under it would desync the cursor and
		// eventually trim UN-archived messages. Trim batches anyway; defer to
		// a later turn.
		return msgs
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

	sys := "You maintain a running summary of an ongoing operator conversation. EXTEND the existing summary with the new exchanges (do not restart). Preserve: decisions, standing jobs created or changed, facts about the user's systems, and open threads. Drop chitchat and exact wording. Keep the summary under ~500 tokens.\n\nGROUNDING (critical): summarize ONLY what is explicitly present in the exchanges above. Never add a specific (a name, place, topic, title, number, or date) that does not appear verbatim in them, and never infer one from surrounding context. If an exchange only notes that media was attached (for example a '[N image(s) attached]' line, or one that says 'Depicts: ...'), carry across ONLY what that line literally says and do NOT guess or name the image's subject yourself. A shorter summary that omits an uncertain detail is correct; an invented detail that later reads back as established fact is the failure to avoid. This matters because the summary you write is injected into every later turn as trusted context, so a guess here becomes a hallucination the agent repeats.\n\nThen add a final line beginning with 'FACTS:' listing 0-5 short durable facts worth remembering verbatim, separated by semicolons (or 'FACTS: none'). The same grounding rule applies to FACTS: never record a detail you cannot point to verbatim in the exchanges."
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
//
// foldSeq keys the span (reportID "<source>#f<seq>"): monotonic per thread, so
// no two spans ever share an id. firstIndex is DISPLAY-only (the title's
// message range) — it rebases when trimStoredHistory drops leading rows, and
// keying by it made a later fold at a recurring index silently overwrite an
// older archived span (IngestReportTitled replaces on same reportID). A
// non-nil return means the span is NOT in the index; the caller must not
// advance the fold cursor past it.
func (T *OrchestrateApp) archiveOperatorSpan(udb Database, agentID, sessID string, foldSeq int, folded []Message, firstIndex int) error {
	if udb == nil || len(folded) == 0 {
		return nil
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
		return nil
	}
	source := operatorLCMSource(agentID, sessID)
	reportID := fmt.Sprintf("%s#f%d", source, foldSeq)
	title := fmt.Sprintf("operator history — messages %d–%d", firstIndex, firstIndex+len(folded)-1)
	// Shared core primitive: redacts secret-shaped lines, then ingests.
	if err := IngestRecallSpan(context.Background(), udb, source, reportID, title, body, "lcm"); err != nil {
		Log("[operator.lcm] archive FAILED for span %s: %v", reportID, err)
		return err
	}
	Log("[operator.lcm] archived span %s (%d msgs)", reportID, len(folded))
	// Batch entity extraction: the fold is the completeness backstop for graph
	// population — every turn's stated relationships get extracted here when they
	// age out, catching anything the per-turn pass skipped under its cooldown.
	// Async + gated; idempotent with the per-turn writes.
	extractGraphFromFold(udb, factsNamespace(agentID), folded, T.WorkerChat)
	return nil
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
