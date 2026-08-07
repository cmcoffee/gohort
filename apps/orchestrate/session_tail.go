// Serving the end of a long thread instead of all of it.
//
// A cortex channel is append-only and never ends, so its session record grows
// without bound. Opening one meant shipping every message ever exchanged and
// building a DOM node for each — seconds of blank panel on a thread that has
// been running for months, for a view whose first screen shows the last dozen.
//
// The trim is display-only. Nothing here touches storage, the agent's history,
// or what a turn is given as context — those read the record directly and still
// see all of it. This decides only what the browser is handed on open.
//
// THE INDEX IS THE WHOLE PROBLEM. The client sends a message's index back to
// scrub or truncate at, and that index has to name the message in STORAGE.
// Serving a tail makes every position in the delivered array wrong by the
// number dropped, so the offset travels with it and the client adds it back.
// The offset shipped a release early, while it was always zero, so this change
// is the one that starts using it rather than the one that introduces it.
package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_chat_tail_messages",
		Category: "Limits",
		Label:    "Messages loaded when opening a thread",
		Help:     "How many of a conversation's most recent messages are sent to the browser when you open it. Older ones stay stored and stay in the agent's context — this only bounds what the page renders at once, so a long-running channel thread opens quickly instead of building thousands of bubbles. \"Load earlier\" fetches more. 0 loads the whole thread every time.",
		Kind:     KindInt,
		Default:  80,
		Min:      0,
		Max:      100000,
	})
}

// sessionTailLimit is the default number of messages served on open.
func sessionTailLimit() int { return TuneInt("tune_chat_tail_messages") }

// tailMessages returns the last `limit` messages and how many were dropped
// from the front.
//
// limit <= 0 means everything, which is both the "off" setting and what a
// short thread gets anyway — so the common case costs one comparison and
// returns the caller's own slice untouched.
func tailMessages(msgs []ChatMessage, limit int) ([]ChatMessage, int) {
	if limit <= 0 || len(msgs) <= limit {
		return msgs, 0
	}
	off := len(msgs) - limit
	return msgs[off:], off
}

// resolveTailLimit reads the client's requested limit, falling back to the
// configured default.
//
// A client asking for MORE than the default is honoured: that is the "load
// earlier" button, which doubles its request each press and eventually asks for
// the lot. A limit is a rendering budget, not a permission — every message it
// could ask for is one this same user could already read.
func resolveTailLimit(requested string) int {
	n, err := atoiStrict(requested)
	if err != nil {
		return sessionTailLimit()
	}
	if n <= 0 {
		// An explicit 0 means "all of it", and has to survive a nonzero
		// default — otherwise the load-earlier button can never reach the top
		// of a thread longer than the doubling ever reaches.
		return 0
	}
	return n
}

// atoiStrict parses a decimal count, rejecting anything that is not one. Its
// own function so an empty or malformed query param falls to the default
// rather than to zero, which here means the opposite of "unset".
func atoiStrict(s string) (int, error) {
	if s == "" {
		return 0, errNotANumber
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotANumber
		}
		n = n*10 + int(r-'0')
		if n > 100000000 {
			return 0, errNotANumber
		}
	}
	return n, nil
}

type tailError string

func (e tailError) Error() string { return string(e) }

const errNotANumber = tailError("not a number")
