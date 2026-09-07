package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// PromptDigest is what one turn's prompt was made of, recorded per turn.
//
// The bug this exists to catch is intermittent and invisible. A cortex thread
// arrived at ~203k tokens with a 64s prefill and 0% cache on a turn that made
// ONE call, and every message-shaped diagnostic read healthy while it happened,
// because the weight was in persisted tool results rather than in anything a
// message count could see. Two layers reported the same quantity 30x apart for
// weeks. A number next to the window, recorded every turn, turns that kind of
// archaeology into a glance.
//
// SIZES AND KEYS, NOT TEXT. The full prompt is 46KB+ on an ordinary chat turn;
// a ledger of those is a storage problem and a mild disclosure one. What is
// stored is the shape: how big each part was, which clauses were live, and how
// close the whole thing came to the window. Capturing the text itself is a
// separate, per-agent, off-by-default thing and is deliberately not here.
//
// Token counts are estimates (EstimateTokens is chars/4) EXCEPT InputTokens,
// which is what the provider actually charged. When the two disagree badly, the
// provider is right and the estimator is the thing to fix.
type PromptDigest struct {
	SystemBytes   int      `json:"system_bytes"`
	SystemTokens  int      `json:"system_tokens"`
	ClauseKeys    []string `json:"clause_keys,omitempty"` // framework blocks live this turn
	ToolCount     int      `json:"tool_count"`
	ToolTokens    int      `json:"tool_tokens"` // measured from the serialized schemas, which is what is sent
	Messages      int      `json:"messages"`
	HistoryChars  int      `json:"history_chars"`
	HistoryTokens int      `json:"history_tokens"` // counts tool-result bodies, where a standing thread's weight lives
	Window        int      `json:"window"`         // the model's context size, 0 when none was declared
	Budget        int      `json:"budget"`         // what compaction allowed history, 0 when compaction is off
	InputTokens   int      `json:"input_tokens,omitempty"`
	Prefilled     int      `json:"prefilled,omitempty"` // prompt tokens the provider re-prefilled (the rest was cached)
	PrefillMS     float64  `json:"prefill_ms,omitempty"`
	Headroom      int      `json:"headroom,omitempty"` // window minus the estimated prompt; negative means over
	Tight         bool     `json:"tight,omitempty"`    // estimated prompt is within a tenth of the window

	// Estimated is the whole prompt as the loop measured it before the call.
	// A stored FIELD rather than a method over the parts: this is a record of
	// one moment, and a total recomputed later from fields somebody may have
	// migrated is a different number wearing the same name.
	Estimated int `json:"estimated"`

	// Text is the turn's prompt as text, present only when the agent has
	// capture switched on. json:"-" so it can never ride a metadata surface by
	// accident; RecordRun also strips it explicitly into the encrypted side
	// table beside Raw, which is the rule this must not be the exception to.
	Text string `json:"-"`
}

// capturePromptText renders the turn's prompt as text.
//
// The system prompt and the conversation, NOT the tool schemas. The schemas are
// the largest part of a modern prompt and the least informative — one live turn
// was 196KB of which 153KB was schemas — and the digest already counts them
// while the catalog log line already names every tool. What no other record
// holds is the TEXT: which clause was live, what the history actually said, and
// whether something reached this turn that belongs to another conversation.
// That last question is the one that took a night of inference to answer badly.
func capturePromptText(systemPrompt string, tools []AgentToolDef, msgs []Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== SYSTEM PROMPT (%d bytes) ===\n%s\n", len(systemPrompt), systemPrompt)
	names := make([]string, 0, len(tools))
	for _, td := range tools {
		names = append(names, td.Tool.Name)
	}
	fmt.Fprintf(&b, "\n=== TOOLS (%d; schemas not captured) ===\n%s\n", len(names), strings.Join(names, ", "))
	fmt.Fprintf(&b, "\n=== HISTORY (%d message(s)) ===\n", len(msgs))
	for i, m := range msgs {
		fmt.Fprintf(&b, "\n--- [%d] %s ---\n%s\n", i+1, m.Role, m.Content)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "    (tool call: %s)\n", tc.Name)
		}
		if n := len(m.Images); n > 0 {
			fmt.Fprintf(&b, "    (%d image(s), bytes not captured)\n", n)
		}
	}
	return b.String()
}

// --- collecting a digest from several frames up --------------------------------
//
// OnPromptDigest serves the caller that BUILDS the loop config. The other
// caller is the one several frames up that never sees it: an event-monitor
// wake calls an opaque registered waker and writes the run record itself, and
// a standing fire reaches its prompt through a shared dispatch that already
// returns four values. Neither can be given the hook without threading a
// parameter through code that has no other reason to know about it.
//
// So the loop writes the digest onto the context as well, and a caller that
// wants it wraps its ctx before handing it down. That reaches every path that
// runs an agent loop at all — including ones written later, which is the part
// a per-call-site hook could never promise.

type promptDigestKeyT struct{}

var promptDigestKey promptDigestKeyT

type promptDigestSink struct {
	mu sync.Mutex
	d  PromptDigest
	// set records whether anything has been written, so a first prompt that
	// genuinely measured zero still counts as the answer.
	set bool
}

// WithPromptDigest returns a context that collects the next turn's prompt
// digest, and a reader for it. The reader is safe to call before, during or
// after the turn; it reports the zero digest until one is recorded.
//
// The FIRST digest under a given context wins. A fanout stage runs several
// dispatches concurrently under one context, and the run being described is
// the one that opened it.
func WithPromptDigest(ctx context.Context) (context.Context, func() PromptDigest) {
	s := &promptDigestSink{}
	return context.WithValue(ctx, promptDigestKey, s), func() PromptDigest {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.d
	}
}

// recordPromptDigest writes to the collector on ctx, if a caller installed one.
func recordPromptDigest(ctx context.Context, d PromptDigest) {
	if ctx == nil {
		return
	}
	s, _ := ctx.Value(promptDigestKey).(*promptDigestSink)
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.set {
		s.d, s.set = d, true
	}
	s.mu.Unlock()
}

// emitPromptDigest logs the turn's prompt shape and hands it to the app.
//
// The log line is always written: it is the thing to read FIRST when a turn is
// slow or a reply is strange, and it costs one line. The tight case is louder
// because a prompt sitting on the window is the failure that took a week to
// find — the surface that would have made it a glance is a number printed next
// to the window it is about to exceed.
func emitPromptDigest(ctx context.Context, cfg AgentLoopConfig, d PromptDigest) {
	line := fmt.Sprintf("prompt~%d tokens = system %d + tools %d (%d) + history %d (%d msgs), window %d, budget %d, %d clause(s)",
		d.Estimated, d.SystemTokens, d.ToolTokens, d.ToolCount, d.HistoryTokens, d.Messages, d.Window, d.Budget, len(d.ClauseKeys))
	if d.InputTokens > 0 {
		line += fmt.Sprintf(", provider charged %d", d.InputTokens)
	}
	if d.Tight {
		Log("[agent_loop] HEADROOM: %s — %d tokens from the window. A prompt this close to the wall fails intermittently rather than cleanly.", line, d.Headroom)
	} else {
		Debug("[agent_loop] %s", line)
	}
	if cfg.OnPromptDigest != nil {
		cfg.OnPromptDigest(d)
	}
	// And the context collector, for the callers that never see the config.
	recordPromptDigest(ctx, d)
}

// buildPromptDigest measures the prompt about to be sent.
//
// Everything here is read from the SAME values the call will use: the assembled
// system prompt, the compacted history, the tool set for this round, and the
// budget compactHistory just reported. Nothing is recomputed from a formula
// kept beside the original, because that is precisely how the two layers of one
// turn came to disagree about history size by a factor of thirty.
func buildPromptDigest(systemPrompt string, clauseKeys []string, tools []AgentToolDef, msgs []Message, window, budget int) PromptDigest {
	d := PromptDigest{
		SystemBytes:  len(systemPrompt),
		SystemTokens: EstimateTokens(systemPrompt),
		ClauseKeys:   clauseKeys,
		ToolCount:    len(tools),
		Messages:     len(msgs),
		Window:       window,
		Budget:       budget,
	}
	// The serialized schema is what the provider is sent; names and
	// descriptions alone understate a catalog whose weight is in parameters.
	for _, td := range tools {
		if b, err := json.Marshal(td.Tool); err == nil {
			d.ToolTokens += len(b) / 4
		}
	}
	for i := range msgs {
		d.HistoryChars += len(msgs[i].Content)
		for j := range msgs[i].ToolResults {
			d.HistoryChars += len(msgs[i].ToolResults[j].Content)
		}
	}
	d.HistoryTokens = EstimateMessagesTokens(msgs)
	d.Estimated = d.SystemTokens + d.ToolTokens + d.HistoryTokens
	if window > 0 {
		d.Headroom = window - d.Estimated
		// A tenth of the window. The measured failure was 523,749 chars against
		// a 131,072-token budget: sitting ON the line, which is why it was
		// intermittent and why nobody could catch it in the act. Anything this
		// close is worth saying out loud whether or not this particular turn
		// survived it.
		d.Tight = d.Headroom < window/10
	}
	return d
}

// promptReuseNote renders the prompt-cache read for the round breadcrumb, or
// nothing at all when the backend doesn't report one. It rides the existing
// "LLM returned" line rather than adding a line of its own: the question it
// answers — did this round re-prefill the whole prompt? — is only meaningful
// next to how long the round took, and the pair is what turns a latency
// report into a diagnosis.
func promptReuseNote(resp *Response) string {
	if resp == nil || resp.PromptTokensPrefilled <= 0 || resp.InputTokens <= 0 {
		return ""
	}
	cached := resp.InputTokens - resp.PromptTokensPrefilled
	if cached < 0 {
		cached = 0
	}
	return fmt.Sprintf(", prefilled=%d/%d prompt tokens (%d%% cached, %.0fms)",
		resp.PromptTokensPrefilled, resp.InputTokens, cached*100/resp.InputTokens, resp.PrefillMS)
}

// truncForLog shortens s to n chars for log preview, replacing newlines
// so the line stays one row.
// noToolDiagLine renders the zero-tool-turn diagnostic. Split out from the loop
// so the masking rule can be tested: a session that carries credentials must
// yield a length and nothing else, and that is not a property to leave to the
// next person editing a format string.
func noToolDiagLine(round int, asked, reply string, masked bool) string {
	reply = strings.TrimSpace(reply)
	if masked {
		return fmt.Sprintf("[agent_loop] NOTOOL-DIAG round %d: no tool ran this turn; asked=[masked: %d chars] reply=[masked: %d chars]",
			round, len(strings.TrimSpace(asked)), len(reply))
	}
	return fmt.Sprintf("[agent_loop] NOTOOL-DIAG round %d: no tool ran this turn; asked=%q reply=%q",
		round, truncForLog(asked, 200), truncForLog(reply, 1000))
}

func truncForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	// Cut on a RUNE boundary. Slicing bytes splits any multi-byte character
	// straddling the limit and writes invalid UTF-8 into the log — which used to
	// be theoretical, when this only ever truncated tool names and short
	// snippets, and stopped being when it started carrying reply text. The
	// replies that prompted it end in "🏚️👔".
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// logPromptFloor breaks the first round's prompt into its parts.
//
// "Why does 'Hello' cost 34k tokens?" was unanswerable: the floor is assembled
// from a system prompt, a tool catalog, and history, and nothing reported
// their relative sizes — so tuning it meant guessing which one to cut. Tool
// SCHEMAS are usually the bulk and the least obvious, because each one carries
// a full description and parameter spec whether or not the turn uses it.
//
// Round 1 only: later rounds add tool results, and the point here is the FLOOR
// a turn starts from. Bytes, not tokens — no tokenizer is in reach at this
// layer, and ~4 bytes/token is close enough to tell a 20k problem from a 2k
// one.
func logPromptFloor(cfg AgentLoopConfig, systemPrompt string, history []Message) {
	if r := promptSizeReport(cfg, systemPrompt, history); r != "" {
		Debug("[agent_loop] prompt floor: %s", r)
	}
}

// promptSizeReport says where a round's bytes actually are.
//
// It exists because "the prompt is too large" is not a diagnosis. A session
// that failed at two million tokens against a 262k window was investigated
// twice from the code — once wrongly blaming history compaction, once wrongly
// blaming a single oversized tool result — because nothing in the failure said
// which part was big. A number per component turns the next occurrence into a
// reading rather than a guess.
//
// The counts include TOOL RESULTS, which the original floor log omitted: it
// summed only Message.Content, so a history dominated by tool output reported
// as small. That omission is exactly the shape of thing that hides for a long
// time — the number was there, it was just quietly wrong.
func promptSizeReport(cfg AgentLoopConfig, systemPrompt string, history []Message) string {
	sysBytes := len(systemPrompt)

	histBytes, resultBytes := 0, 0
	biggestMsg, biggestMsgAt, biggestMsgRole := 0, -1, ""
	for i, m := range history {
		n := len(m.Content) + len(m.Reasoning)
		for _, tr := range m.ToolResults {
			n += len(tr.Content)
			resultBytes += len(tr.Content)
		}
		histBytes += n
		if n > biggestMsg {
			biggestMsg, biggestMsgAt, biggestMsgRole = n, i, m.Role
		}
	}

	toolBytes, biggest, biggestName := 0, 0, ""
	for _, t := range cfg.Tools {
		n := len(t.Tool.Name) + len(t.Tool.Description)
		for pn, p := range t.Tool.Parameters {
			n += len(pn) + len(p.Description) + len(p.Type)
		}
		toolBytes += n
		if n > biggest {
			biggest, biggestName = n, t.Tool.Name
		}
	}

	total := sysBytes + histBytes + toolBytes
	if total == 0 {
		return ""
	}
	pct := func(n int) int { return n * 100 / total }
	return fmt.Sprintf("%d bytes total (~%dk tokens) = system %d (%d%%) + %d tool schemas %d (%d%%) + history %d (%d%%, of which %d is tool results); "+
		"largest single message #%d (%s) at %d bytes; largest tool schema %q at %d bytes",
		total, total/4000,
		sysBytes, pct(sysBytes),
		len(cfg.Tools), toolBytes, pct(toolBytes),
		histBytes, pct(histBytes), resultBytes,
		biggestMsgAt, biggestMsgRole, biggestMsg,
		biggestName, biggest)
}

// promptSizeHeadline is the one-clause version for a user-facing error: which
// component holds the bulk, so the message names a place to look instead of
// only reporting that there is a problem.
func promptSizeHeadline(cfg AgentLoopConfig, systemPrompt string, history []Message) string {
	sysBytes := len(systemPrompt)
	histBytes := 0
	for _, m := range history {
		histBytes += len(m.Content) + len(m.Reasoning)
		for _, tr := range m.ToolResults {
			histBytes += len(tr.Content)
		}
	}
	toolBytes := 0
	for _, t := range cfg.Tools {
		toolBytes += len(t.Tool.Name) + len(t.Tool.Description)
		for pn, p := range t.Tool.Parameters {
			toolBytes += len(pn) + len(p.Description) + len(p.Type)
		}
	}
	switch {
	case sysBytes >= histBytes && sysBytes >= toolBytes:
		return fmt.Sprintf("most of it is the system prompt (~%dk tokens), which compaction cannot shrink — the agent's own instructions, memory or attached sources are the place to look", sysBytes/4000)
	case toolBytes >= histBytes:
		return fmt.Sprintf("most of it is tool definitions (~%dk tokens across %d tools), which compaction cannot shrink — narrow the agent's tool list", toolBytes/4000, len(cfg.Tools))
	default:
		return fmt.Sprintf("most of it is conversation history (~%dk tokens)", histBytes/4000)
	}
}
