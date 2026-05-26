// Memory consolidation — the synthesis auto-ingest path has been
// removed. Reference Memory is now PULL-ONLY + explicit-save: the
// LLM calls memory_save when it consciously wants to record a
// finding; memory_search retrieves them on demand. Auto-ingest of
// every successful synthesis was producing low-signal chunks that
// compounded drift on retrieval and polluted future turns when the
// derived chunks auto-injected.
//
// The consolidate() helper stays as a no-op so callers in runner.go
// don't have to be touched in lockstep. stripBulletPrefix is kept
// because gapcheck.go still uses it as a general string helper.

package orchestrate

import (
	"strings"
)

// consolidate is now a no-op. Kept so existing runner.go call sites
// don't need to be touched. The auto-ingest of synthesis findings
// has been retired in favor of explicit memory_save + memory_search.
func (t *chatTurn) consolidate(userMsg string, steps []PlanStep, synthesis string) {
	_ = userMsg
	_ = steps
	_ = synthesis
}

// stripBulletPrefix removes "-", "*", "•", or "1. "/"1) " style
// leaders. Idempotent on already-clean lines. Kept here because
// gapcheck.go still uses it; the consolidator's own parser path is
// gone, but the helper is general.
func stripBulletPrefix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, "•") {
		return strings.TrimSpace(s[len("•"):])
	}
	if r := s[0]; r == '-' || r == '*' {
		return strings.TrimSpace(s[1:])
	}
	// "1. " or "1) " style
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '.' || s[i] == ')') {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
