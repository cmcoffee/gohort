package core

import (
	"context"
	"strings"
)

// Tunables for StreamGuardHandler. Values chosen empirically to catch
// genuine loops without false-firing on normal LLM output. A typical
// streamed response is 4-12 KB; a loop produces tens of KB of repeated
// phrases or arbitrarily long babble.
// StreamGuardMaxChars caps total streamed output per call. 50 KB
// leaves comfortable headroom for long-but-legitimate responses
// while catching runaway generation.
func StreamGuardMaxChars() int { return TuneInt("tune_stream_guard_max_chars") }

// StreamGuardRepeatSegLen is the length of the tail segment sampled
// when checking for repetition. 40 chars is long enough to avoid
// false positives on common short phrases and short enough to catch
// real loops.
func StreamGuardRepeatSegLen() int { return TuneInt("tune_stream_guard_repeat_seg_len") }

// StreamGuardRepeatThreshold is how many times the same tail segment
// must appear inside the recent window to trigger abort. 5
// occurrences of an exact 40-char string is well beyond coincidence.
func StreamGuardRepeatThreshold() int { return TuneInt("tune_stream_guard_repeat_threshold") }

func init() {
	RegisterTunable(TunableSpec{Key: "tune_stream_guard_max_chars", Category: "Limits", Label: "Stream guard max chars", Help: "Hard cap on total streamed output per LLM call before the stream is aborted.", Kind: KindInt, Default: 51200, Min: 8192, Max: 524288})
	RegisterTunable(TunableSpec{Key: "tune_stream_guard_repeat_seg_len", Category: "Limits", Label: "Stream guard repeat segment length", Help: "Length of the tail segment sampled when detecting a repetition loop.", Kind: KindInt, Default: 40, Min: 10, Max: 200})
	RegisterTunable(TunableSpec{Key: "tune_stream_guard_repeat_threshold", Category: "Limits", Label: "Stream guard repeat threshold", Help: "How many times the sampled tail segment must repeat in-window to abort the stream.", Kind: KindInt, Default: 5, Min: 2, Max: 25})
}

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
		if maxChars := StreamGuardMaxChars(); total.Len() > maxChars {
			aborted = true
			if onAbort != nil {
				onAbort("stream exceeded max chars")
			}
			Log("[stream-guard] aborted at %d chars (cap %d)", total.Len(), maxChars)
			cancel()
			return
		}

		// Repetition detector. Need at least threshold * segment-length
		// bytes from the tail to sample.
		segLen := StreamGuardRepeatSegLen()
		threshold := StreamGuardRepeatThreshold()
		windowBytes := segLen * threshold
		if total.Len() < windowBytes {
			return
		}
		tail := out[total.Len()-windowBytes:]
		seg := out[total.Len()-segLen:]
		if strings.Count(tail, seg) >= threshold {
			aborted = true
			if onAbort != nil {
				onAbort("repetition loop detected")
			}
			Log("[stream-guard] aborted on repetition loop (segment %q x%d in last %d chars)",
				seg, threshold, windowBytes)
			cancel()
			return
		}
	}
}
