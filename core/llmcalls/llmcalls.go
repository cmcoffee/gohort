// Package llmcalls keeps what the model calls made under a context said about
// themselves: how each one stopped, and how many tokens it wrote and spent
// thinking. A check test watches each case with it, so a case that got an
// empty or cut-off reply says why ("stopped at its output limit after 600
// tokens, about 580 of them thinking") instead of only that the reply could
// not be read.
//
// A leaf of core: core notes each call at the one place every reply passes
// (RecordUsage), and core sits at its export ceiling.
package llmcalls

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Call is one model call, as its reply described it.
type Call struct {
	Model     string // the model that answered
	Tier      string // "worker" or "lead"
	Stop      string // the provider's stop reason ("max_tokens", "end_turn", ...)
	Input     int    // input tokens
	Output    int    // output tokens, thinking included
	Reasoning int    // of Output, about how many were thinking
}

type watch struct {
	mu    sync.Mutex
	calls []Call
}

type key struct{}

// Watch is ctx with the model calls made under it kept, and the way to read
// them back.
func Watch(ctx context.Context) (context.Context, func() []Call) {
	w := &watch{}
	return context.WithValue(ctx, key{}, w), func() []Call {
		w.mu.Lock()
		defer w.mu.Unlock()
		return append([]Call(nil), w.calls...)
	}
}

// Note keeps c for whoever watches ctx; nothing when nobody does.
func Note(ctx context.Context, c Call) {
	if ctx == nil {
		return
	}
	w, _ := ctx.Value(key{}).(*watch)
	if w == nil {
		return
	}
	w.mu.Lock()
	w.calls = append(w.calls, c)
	w.mu.Unlock()
}

// Describe is calls in words, one call per clause: how it stopped and what it
// wrote. "" for none.
func Describe(calls []Call) string {
	var parts []string
	for _, c := range calls {
		parts = append(parts, describeOne(c))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	for i := range parts {
		parts[i] = fmt.Sprintf("%d) %s", i+1, parts[i])
	}
	return fmt.Sprintf("%d calls: %s", len(parts), strings.Join(parts, "; "))
}

func describeOne(c Call) string {
	var how string
	switch strings.ToLower(c.Stop) {
	case "max_tokens", "length":
		how = "stopped at its output limit"
	case "end_turn", "stop", "stop_sequence":
		how = "finished"
	case "tool_use", "tool_calls":
		how = "stopped to call a tool"
	case "refusal":
		how = "declined"
	case "":
		how = "ended (no stop reason given)"
	default:
		how = "ended (" + c.Stop + ")"
	}
	s := fmt.Sprintf("%s after %d output tokens", how, c.Output)
	if c.Reasoning > 0 {
		s += fmt.Sprintf(", about %d of them thinking", c.Reasoning)
	}
	s += fmt.Sprintf(", %d in", c.Input)
	if c.Tier != "" {
		// The tier, not the model: the report's header names the model, and
		// some model ids (a Bedrock profile's) carry an account number.
		s += " (" + c.Tier + ")"
	}
	return s
}
