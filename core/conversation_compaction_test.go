package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func mkMsgs(n int) []Message {
	out := make([]Message, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out[i] = Message{Role: role, Content: fmt.Sprintf("m%d", i)}
	}
	return out
}

// fakeFold records what it was asked to fold and returns a deterministic
// summary + one fact.
func fakeFold(ctx context.Context, aging []Message, prior string) (string, []string, error) {
	s := prior
	if s != "" {
		s += " | "
	}
	s += fmt.Sprintf("folded %d (%s..%s)", len(aging), aging[0].Content, aging[len(aging)-1].Content)
	return s, []string{"fact:" + aging[0].Content}, nil
}

// Below the trigger: nothing folds, the whole thread is the verbatim tail.
func TestCompactBelowTrigger(t *testing.T) {
	cfg := CompactionConfig{KeepRecent: 3, Trigger: 6}
	msgs := mkMsgs(5)
	st, facts, changed, err := CompactConversation(context.Background(), msgs, CompactState{}, cfg, fakeFold)
	if err != nil || changed || facts != nil {
		t.Fatalf("expected no-op below trigger, got changed=%v facts=%v err=%v", changed, facts, err)
	}
	block, recent := CompactedView(msgs, st, cfg)
	if block != "" {
		t.Fatalf("expected no summary block, got %q", block)
	}
	if len(recent) != 5 {
		t.Fatalf("expected all 5 messages verbatim, got %d", len(recent))
	}
}

// Past the trigger: fold down to the tail, set the summary + marker, surface facts.
func TestCompactFolds(t *testing.T) {
	cfg := CompactionConfig{KeepRecent: 3, Trigger: 6}
	msgs := mkMsgs(10) // unsummarized tail = 10 > trigger 6
	st, facts, changed, err := CompactConversation(context.Background(), msgs, CompactState{}, cfg, fakeFold)
	if err != nil || !changed {
		t.Fatalf("expected a fold, got changed=%v err=%v", changed, err)
	}
	// Folds msgs[0:7] (len-KeepRecent), keeps the last 3 verbatim.
	if st.SummarizedThrough != 7 {
		t.Fatalf("expected SummarizedThrough=7, got %d", st.SummarizedThrough)
	}
	if !strings.Contains(st.Summary, "folded 7") {
		t.Fatalf("summary not set as expected: %q", st.Summary)
	}
	if len(facts) != 1 || facts[0] != "fact:m0" {
		t.Fatalf("expected one fact fact:m0, got %v", facts)
	}
	block, recent := CompactedView(msgs, st, cfg)
	if block == "" || !strings.Contains(block, "folded 7") {
		t.Fatalf("expected summary block with folded content, got %q", block)
	}
	if len(recent) != 3 || recent[0].Content != "m7" {
		t.Fatalf("expected verbatim tail m7..m9, got %d starting %q", len(recent), recent[0].Content)
	}
}

// A second fold EXTENDS the existing summary and advances the marker; the
// already-folded span is never re-folded.
func TestCompactExtendsAndDoesNotRefold(t *testing.T) {
	cfg := CompactionConfig{KeepRecent: 3, Trigger: 6}
	msgs := mkMsgs(10)
	st, _, _, _ := CompactConversation(context.Background(), msgs, CompactState{}, cfg, fakeFold)
	// Grow the thread; the next fold should only cover the NEW aging span.
	msgs2 := mkMsgs(18)
	st2, _, changed, _ := CompactConversation(context.Background(), msgs2, st, cfg, fakeFold)
	if !changed {
		t.Fatal("expected a second fold")
	}
	if st2.SummarizedThrough != 15 {
		t.Fatalf("expected SummarizedThrough=15, got %d", st2.SummarizedThrough)
	}
	// The new fold span starts at the prior marker (7), not 0 — no re-fold.
	if !strings.Contains(st2.Summary, "m7..m14") {
		t.Fatalf("second fold should cover m7..m14 only: %q", st2.Summary)
	}
	if strings.Count(st2.Summary, "folded") != 2 {
		t.Fatalf("summary should be extended (2 folds), got %q", st2.Summary)
	}
}

// MaxSummaryChars trims the front, preserving the latest narrative.
func TestCompactTrimsSummary(t *testing.T) {
	cfg := CompactionConfig{KeepRecent: 1, Trigger: 2, MaxSummaryChars: 40}
	msgs := mkMsgs(10)
	bigFold := func(ctx context.Context, aging []Message, prior string) (string, []string, error) {
		return strings.Repeat("x", 100), nil, nil
	}
	st, _, changed, _ := CompactConversation(context.Background(), msgs, CompactState{}, cfg, bigFold)
	if !changed {
		t.Fatal("expected a fold")
	}
	if len(st.Summary) > 40+len("[...older summary trimmed...]\n") {
		t.Fatalf("summary not trimmed: %d chars", len(st.Summary))
	}
	if !strings.HasPrefix(st.Summary, "[...older summary trimmed...]") {
		t.Fatalf("expected trim marker prefix, got %q", st.Summary[:30])
	}
}

// FoldSeq advances once per completed fold — the monotonic key archivers use
// for span ids (message indices rebase on storage trims, so they recur).
func TestCompactFoldSeqAdvances(t *testing.T) {
	cfg := CompactionConfig{KeepRecent: 2, Trigger: 4}
	var seen []int
	st := CompactState{}
	cfg.OnFold = func(folded []Message, firstIndex int) error {
		seen = append(seen, st.FoldSeq) // what an archiver would key by
		return nil
	}
	st1, _, changed, err := CompactConversation(context.Background(), mkMsgs(8), st, cfg, fakeFold)
	if err != nil || !changed || st1.FoldSeq != 1 {
		t.Fatalf("first fold: changed=%v FoldSeq=%d err=%v", changed, st1.FoldSeq, err)
	}
	st = st1
	st2, _, changed, err := CompactConversation(context.Background(), mkMsgs(14), st, cfg, fakeFold)
	if err != nil || !changed || st2.FoldSeq != 2 {
		t.Fatalf("second fold: changed=%v FoldSeq=%d err=%v", changed, st2.FoldSeq, err)
	}
	if len(seen) != 2 || seen[0] != 0 || seen[1] != 1 {
		t.Fatalf("OnFold should observe pre-fold seqs [0 1], got %v", seen)
	}
}

// A failed archive aborts the fold: state (cursor, summary, seq) must not
// advance, so trimStoredHistory can never drop messages that aren't archived.
func TestCompactOnFoldErrorAbortsFold(t *testing.T) {
	cfg := CompactionConfig{KeepRecent: 2, Trigger: 4}
	cfg.OnFold = func(folded []Message, firstIndex int) error {
		return fmt.Errorf("archive store down")
	}
	prior := CompactState{Summary: "old", SummarizedThrough: 0, FoldSeq: 3}
	st, facts, changed, err := CompactConversation(context.Background(), mkMsgs(10), prior, cfg, fakeFold)
	if err == nil {
		t.Fatal("expected the archive error to surface")
	}
	if changed || facts != nil {
		t.Fatalf("failed archive must not report a change: changed=%v facts=%v", changed, facts)
	}
	if st != prior {
		t.Fatalf("state advanced past an unarchived span: %+v", st)
	}
}
