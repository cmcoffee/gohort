package core

import "strings"
import "testing"

// A backend that reports nothing must add nothing to the breadcrumb — the
// whole local fleet is llama.cpp today, but the hosted providers are not, and
// a "0/0 prompt tokens (0% cached)" on every Anthropic round would read as a
// cache failure that never happened.
func TestPromptReuseNoteSilentWithoutServerTimings(t *testing.T) {
	for _, resp := range []*Response{
		nil,
		{InputTokens: 16000},
		{PromptTokensPrefilled: 40},
	} {
		if got := promptReuseNote(resp); got != "" {
			t.Fatalf("expected no note without timings, got %q", got)
		}
	}
}

// The reused case is the one worth reading: a stable prefix means the server
// prefills only the new tokens, and the note has to make that obvious at a
// glance rather than requiring the reader to do the subtraction.
func TestPromptReuseNoteReportsCachedShare(t *testing.T) {
	got := promptReuseNote(&Response{InputTokens: 16000, PromptTokensPrefilled: 160, PrefillMS: 210})
	if !strings.Contains(got, "160/16000") {
		t.Fatalf("missing prefilled/total: %q", got)
	}
	if !strings.Contains(got, "99% cached") {
		t.Fatalf("expected 99%% cached, got %q", got)
	}
	if !strings.Contains(got, "210ms") {
		t.Fatalf("missing prefill wall-time: %q", got)
	}
}

// A cold turn re-prefills everything. It must report 0% rather than dividing
// by a zero or going negative, because this is exactly the reading someone
// takes after a prefix-stability regression.
func TestPromptReuseNoteColdPrefillReportsZeroCached(t *testing.T) {
	got := promptReuseNote(&Response{InputTokens: 16000, PromptTokensPrefilled: 16000, PrefillMS: 2839})
	if !strings.Contains(got, "0% cached") {
		t.Fatalf("expected 0%% cached on a full prefill, got %q", got)
	}
}

// prompt_n above prompt_tokens is not physical, but a backend reporting the
// two from different counters could still emit it; the note must not print a
// negative cache share.
func TestPromptReuseNoteClampsOverReportedPrefill(t *testing.T) {
	got := promptReuseNote(&Response{InputTokens: 100, PromptTokensPrefilled: 140})
	if strings.Contains(got, "-") {
		t.Fatalf("negative cache share leaked: %q", got)
	}
}
