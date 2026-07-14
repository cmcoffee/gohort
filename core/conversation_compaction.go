// Conversation compaction: rolling-summary + memory eviction for an ongoing
// conversation that would otherwise overflow the context window. Generalized
// from apps/phantom's compaction so any always-on surface (the Operator, and
// eventually phantom) shares one engine.
//
// The model:
//   - Keep a verbatim TAIL (the most recent KeepRecent messages) sent as-is.
//   - When the unsummarized tail grows past Trigger, FOLD the aging span
//     (everything except the tail) into a running Summary via a caller-
//     supplied fold function (a worker-LLM call), and advance a marker so the
//     same span is never folded twice.
//   - The fold also surfaces durable FACTS (memories) the caller stores in the
//     agent's memory layer — exact recall, complementing the lossy summary.
//   - Each turn, the model sees: the Summary block (prepended to the system
//     prompt) + the verbatim tail. Bounded context over an unbounded thread.
//
// Lossy by design: the summary is for narrative continuity; anything that must
// be recalled exactly goes to memory (the facts the fold returns).

package core

import (
	"context"
	"strings"
)

// CompactionConfig tunes when and how aggressively to fold. Zero values get
// sensible defaults via withDefaults.
type CompactionConfig struct {
	KeepRecent      int // verbatim tail kept after a fold (default 12)
	Trigger         int // fold when the unsummarized tail exceeds this (default KeepRecent*3)
	MaxSummaryChars int // cap on the running summary; oldest trimmed (default 4000)

	// OnFold, when set, is called with the raw span being folded into the
	// summary and the absolute index of its first message, right after a
	// successful fold. Lets a caller ARCHIVE the aging content elsewhere — a
	// searchable history index — so it stays recoverable after it leaves both
	// the verbatim window and the (lossy) summary. A non-nil error aborts the
	// fold: the state does not advance, so the same span is re-folded (and the
	// archive retried) on a later turn. Without that, a failed archive would
	// still advance the cursor and trimStoredHistory-style callers would later
	// hard-drop messages that were never actually archived. Optional; nil = no
	// archiving.
	OnFold func(folded []Message, firstIndex int) error
}

func (c CompactionConfig) withDefaults() CompactionConfig {
	if c.KeepRecent <= 0 {
		c.KeepRecent = 12
	}
	if c.Trigger <= 0 {
		c.Trigger = c.KeepRecent * 3
	}
	if c.Trigger <= c.KeepRecent {
		c.Trigger = c.KeepRecent + 1
	}
	if c.MaxSummaryChars <= 0 {
		c.MaxSummaryChars = 4000
	}
	return c
}

// CompactState is the persisted compaction state for one conversation: the
// running summary and how many leading messages have been folded into it.
type CompactState struct {
	Summary           string `json:"summary,omitempty"`
	SummarizedThrough int    `json:"summarized_through,omitempty"` // count of leading messages already folded
	// FoldSeq counts completed folds, monotonic for the life of the
	// conversation. Archivers key folded spans by it rather than by message
	// index: indices REBASE when the stored thread is trimmed, so an
	// index-keyed span id recurs over a long-lived thread and (via the
	// replace-on-reingest rule) would silently overwrite an older archived
	// span.
	FoldSeq int `json:"fold_seq,omitempty"`
}

// FoldFunc folds the aging span into the running summary and optionally
// surfaces durable facts to remember. priorSummary is the existing summary —
// EXTEND it, don't restart. Returns the new summary + any facts.
type FoldFunc func(ctx context.Context, aging []Message, priorSummary string) (summary string, facts []string, err error)

// CompactedView returns what to actually send the model: a summary block to
// prepend to the system prompt (empty when there's no summary yet) and the
// verbatim tail (messages not yet folded into the summary).
func CompactedView(msgs []Message, st CompactState, cfg CompactionConfig) (summaryBlock string, recent []Message) {
	cfg = cfg.withDefaults()
	through := clampThrough(st.SummarizedThrough, len(msgs))
	recent = msgs[through:]
	if s := strings.TrimSpace(st.Summary); s != "" {
		summaryBlock = "\n\n[Prior conversation context — a summary of older exchanges that have aged out of the verbatim window below. Use it for continuity; treat it as recall, not a transcript. The messages below are the most recent.]\n" + s + "\n"
	}
	return summaryBlock, recent
}

// CompactConversation folds the aging span into the running summary when the
// unsummarized tail has grown past the trigger. Returns the new state, any
// durable facts the fold surfaced (for the caller to store in memory), and
// whether anything changed. Cheap no-op when below threshold or fold is nil.
func CompactConversation(ctx context.Context, msgs []Message, st CompactState, cfg CompactionConfig, fold FoldFunc) (CompactState, []string, bool, error) {
	cfg = cfg.withDefaults()
	through := clampThrough(st.SummarizedThrough, len(msgs))
	if fold == nil || len(msgs)-through <= cfg.Trigger {
		return st, nil, false, nil
	}
	// Fold everything except the most recent KeepRecent into the summary.
	foldEnd := len(msgs) - cfg.KeepRecent
	if foldEnd <= through {
		return st, nil, false, nil
	}
	summary, facts, err := fold(ctx, msgs[through:foldEnd], st.Summary)
	if err != nil {
		return st, nil, false, err
	}
	if summary = strings.TrimSpace(summary); summary == "" {
		return st, nil, false, nil
	}
	// Trim runaway summaries, preserving the END (latest narrative).
	if len(summary) > cfg.MaxSummaryChars {
		summary = "[...older summary trimmed...]\n" + summary[len(summary)-cfg.MaxSummaryChars:]
	}
	// Hand the raw folded span to any archiver before it's lost to the summary.
	// An archive failure aborts the fold (state unadvanced) so the span is
	// retried rather than trimmed away unarchived.
	if cfg.OnFold != nil {
		if aerr := cfg.OnFold(msgs[through:foldEnd], through); aerr != nil {
			return st, nil, false, aerr
		}
	}
	return CompactState{Summary: summary, SummarizedThrough: foldEnd, FoldSeq: st.FoldSeq + 1}, facts, true, nil
}

func clampThrough(through, n int) int {
	if through < 0 {
		return 0
	}
	if through > n {
		return n
	}
	return through
}
