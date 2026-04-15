package core

import (
	"context"
	"strings"
)

// Tunables for StreamGuardHandler. Values chosen empirically to catch
// genuine loops without false-firing on normal LLM output. A typical
// streamed response is 4-12 KB; a loop produces tens of KB of repeated
// phrases or arbitrarily long babble.
const (
	// StreamGuardMaxChars caps total streamed output per call. 50 KB
	// leaves comfortable headroom for long-but-legitimate responses
	// while catching runaway generation.
	StreamGuardMaxChars = 50 * 1024

	// StreamGuardRepeatSegLen is the length of the tail segment sampled
	// when checking for repetition. 40 chars is long enough to avoid
	// false positives on common short phrases and short enough to catch
	// real loops.
	StreamGuardRepeatSegLen = 40

	// StreamGuardRepeatThreshold is how many times the same tail segment
	// must appear inside the recent window to trigger abort. 5
	// occurrences of an exact 40-char string is well beyond coincidence.
	StreamGuardRepeatThreshold = 5
)

// StreamGuardHandler wraps an LLM stream chunk handler with two aborts:
//
//  1. HARD CHAR CAP: if total streamed output exceeds StreamGuardMaxChars,
//     cancel the stream. Stops runaway generation regardless of content.
//  2. REPETITION DETECTION: if the trailing StreamGuardRepeatSegLen
//     bytes appear StreamGuardRepeatThreshold or more times within the
//     most recent window, cancel the stream. Stops the "model got stuck
//     in a repeating phrase" loop without waiting for the char cap.
//
// On abort the guard calls `cancel` (which should be the cancel for the
// context passed to ChatStream) and stops forwarding chunks to `inner`.
// The wrapping caller observes the abort as a context-canceled error.
// `onAbort` is optional; use it to emit a status event or log entry
// describing why the stream ended. Pass nil to suppress.
//
// Usage:
//
//	streamCtx, cancel := context.WithCancel(parentCtx)
//	defer cancel()
//	guarded := StreamGuardHandler(myChunkHandler, cancel, func(reason string) {
//	    emit(StatusEvent{Msg: "Stream aborted: " + reason})
//	})
//	resp, err := llm.ChatStream(streamCtx, messages, guarded, opts...)
func StreamGuardHandler(inner func(chunk string), cancel context.CancelFunc, onAbort func(reason string)) func(chunk string) {
	var (
		total   strings.Builder
		aborted bool
	)
	return func(chunk string) {
		if aborted {
			return
		}
		total.WriteString(chunk)
		if inner != nil {
			inner(chunk)
		}

		out := total.String()

		// Hard cap.
		if total.Len() > StreamGuardMaxChars {
			aborted = true
			if onAbort != nil {
				onAbort("stream exceeded max chars")
			}
			Log("[stream-guard] aborted at %d chars (cap %d)", total.Len(), StreamGuardMaxChars)
			cancel()
			return
		}

		// Repetition detector. Need at least threshold * segment-length
		// bytes from the tail to sample.
		windowBytes := StreamGuardRepeatSegLen * StreamGuardRepeatThreshold
		if total.Len() < windowBytes {
			return
		}
		tail := out[total.Len()-windowBytes:]
		seg := out[total.Len()-StreamGuardRepeatSegLen:]
		if strings.Count(tail, seg) >= StreamGuardRepeatThreshold {
			aborted = true
			if onAbort != nil {
				onAbort("repetition loop detected")
			}
			Log("[stream-guard] aborted on repetition loop (segment %q x%d in last %d chars)",
				seg, StreamGuardRepeatThreshold, windowBytes)
			cancel()
			return
		}
	}
}
