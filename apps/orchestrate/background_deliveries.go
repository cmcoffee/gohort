package orchestrate

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A background result delivered while the turn that started it is still
// running.
//
// A finished task that carries files never joins the live turn: that turn
// built its session before the files existed, so joining would deliver the
// text and strand the picture (see wakeSessionWithNote). It gets its own wake
// turn, which sends it. Correct, and the live turn never heard about it.
// Observed: a cover image started in the background went out at 22:54:03 with
// "Cover's done"; two seconds later the turn that asked for it wrote its own
// reply about the cover, its delivery check counted only what THAT turn had
// sent (nothing), ruled the cover a phantom, and pushed the model to send it
// again. The re-send errored, and the turn closed with "it'll land on its own
// and I'll attach it then", about a picture the chat already had. Later turns
// read that sentence back as the state of things.
//
// So a delivery is written down here, per session, and a running turn on that
// session reads it: what went out since the turn began counts as delivered for
// its checks, and the model is told once, between rounds, so it neither sends
// it again nor promises it.

// backgroundDelivery is one background result that went out to its
// conversation.
type backgroundDelivery struct {
	at    time.Time
	files int    // pictures, videos and files sent with it
	said  string // the message it went out with, clipped
}

// backgroundDeliveryTTL is how long a delivery is remembered: longer than any
// turn that could have started the work and still be running.
const backgroundDeliveryTTL = 30 * time.Minute

var (
	bgDeliveryMu sync.Mutex
	bgDeliveries = map[string][]backgroundDelivery{}
)

// recordBackgroundDelivery notes that a background result reached sessionID's
// conversation with files attached.
func recordBackgroundDelivery(sessionID string, files int, said string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || files <= 0 {
		return
	}
	now := time.Now()
	bgDeliveryMu.Lock()
	defer bgDeliveryMu.Unlock()
	kept := bgDeliveries[sessionID][:0]
	for _, d := range bgDeliveries[sessionID] {
		if now.Sub(d.at) < backgroundDeliveryTTL {
			kept = append(kept, d)
		}
	}
	bgDeliveries[sessionID] = append(kept, backgroundDelivery{at: now, files: files, said: clip(said, 160)})
	for id, list := range bgDeliveries {
		if len(list) == 0 || now.Sub(list[len(list)-1].at) >= backgroundDeliveryTTL {
			delete(bgDeliveries, id)
		}
	}
}

// backgroundDeliveryWatch is one running turn's view of the ledger: what was
// delivered on its session since it began.
type backgroundDeliveryWatch struct {
	sessionID string
	since     time.Time
	mu        sync.Mutex
	told      int // deliveries already reported to the model
}

// watchBackgroundDeliveries starts watching sessionID from now. Nil-safe to
// use: a watch on no session reports nothing.
func watchBackgroundDeliveries(sessionID string) *backgroundDeliveryWatch {
	return &backgroundDeliveryWatch{sessionID: strings.TrimSpace(sessionID), since: time.Now()}
}

// list is what was delivered on this session since the turn began.
func (w *backgroundDeliveryWatch) list() []backgroundDelivery {
	if w == nil || w.sessionID == "" {
		return nil
	}
	bgDeliveryMu.Lock()
	defer bgDeliveryMu.Unlock()
	var out []backgroundDelivery
	for _, d := range bgDeliveries[w.sessionID] {
		if !d.at.Before(w.since) {
			out = append(out, d)
		}
	}
	return out
}

// files is how many files background results delivered during this turn. The
// turn's delivery checks add it to their own count: a picture the chat
// already received is not a phantom because a different turn sent it.
func (w *backgroundDeliveryWatch) files() int {
	n := 0
	for _, d := range w.list() {
		n += d.files
	}
	return n
}

// notices returns a note for each delivery the model has not been told about
// yet, and nil when there is none (the loop's pre-finalize drain stops on nil).
func (w *backgroundDeliveryWatch) notices() []Message {
	if w == nil {
		return nil
	}
	all := w.list()
	w.mu.Lock()
	fresh := all[min(w.told, len(all)):]
	w.told = len(all)
	w.mu.Unlock()
	if len(fresh) == 0 {
		return nil
	}
	out := make([]Message, 0, len(fresh))
	for _, d := range fresh {
		out = append(out, Message{Role: "user", Content: backgroundDeliveryNotice(d)})
	}
	return out
}

// backgroundDeliveryNotice tells a running turn that work it started has
// already reached the conversation.
func backgroundDeliveryNotice(d backgroundDelivery) string {
	what := "1 file"
	if d.files != 1 {
		what = fmt.Sprintf("%d files", d.files)
	}
	msg := fmt.Sprintf("[FRAMEWORK NOTE: not from the user] While this turn was running, background work you started finished and was delivered to the conversation at %s with %s attached", d.at.Format("15:04:05"), what)
	if d.said != "" {
		msg += fmt.Sprintf(", in a message that said: %q", d.said)
	}
	return msg + ". It is already sent. Do not send it again and do not say it is still coming; refer to it as delivered if it comes up."
}

// drainWithDeliveries adds a turn's background-delivery notices to an
// injection drain. Nil when both are empty, as InjectionDrain requires: the
// loop calls it again before finalizing and stops on nil.
func drainWithDeliveries(drain func() []Message, w *backgroundDeliveryWatch) func() []Message {
	return func() []Message {
		var out []Message
		if drain != nil {
			out = drain()
		}
		if n := w.notices(); len(n) > 0 {
			out = append(out, n...)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
}
