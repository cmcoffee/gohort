package anvil

// Stopping when a change is undone while the assistant is still working.
//
// A revert in the middle of a turn used to reach nobody: the file went back,
// and the assistant carried on from what it believed was there. Its next edit
// to that file missed, and the miss shows the file's real lines, so it could
// simply make the undone change again; edits to other files that leaned on
// it failed the project's check, which it then "fixed" by putting the change
// back. An undo means the person disagrees with something, and the one thing
// the assistant cannot do is guess what.
//
// So a revert (one change, or Undo from a message) halts the session that
// made it: the running turn is told on its next decision, and every tool
// that changes something refuses for the rest of that turn, so all it can do
// is say what it had done and ask. The person's next message, sent or typed
// while the turn runs, lifts the halt; what they say there decides the rest.
//
// In memory: a halt matters only to a turn running now or the next message,
// and a restart ends both.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// haltKeep is how long a halt waits for the next message before it is
// forgotten.
const haltKeep = 6 * time.Hour

type halt struct {
	paths map[string]bool
	at    time.Time
}

var (
	haltMu sync.Mutex
	halts  = map[string]*halt{}
)

// haltForRevert halts the session that made e, now reverted, and tells its
// running turn, when one is.
func haltForRevert(e EditEntry) {
	if e.SessionID == "" {
		return
	}
	haltMu.Lock()
	h := halts[e.SessionID]
	if h == nil {
		h = &halt{paths: map[string]bool{}}
		halts[e.SessionID] = h
	}
	h.paths[e.Path] = true
	h.at = time.Now()
	paths := h.list()
	haltMu.Unlock()
	if q := LookupInjectionQueue(e.SessionID); q != nil {
		q.Push(haltMessage(paths))
	}
}

// haltedPaths are the paths whose changes were reverted under sessionID
// since its last message; nil when it is not halted.
func haltedPaths(sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	haltMu.Lock()
	defer haltMu.Unlock()
	for id, h := range halts {
		if time.Since(h.at) > haltKeep {
			delete(halts, id)
		}
	}
	if h := halts[sessionID]; h != nil {
		return h.list()
	}
	return nil
}

// liftHalt is the person's next message to sessionID: they have said how
// to go on.
func liftHalt(sessionID string) {
	if sessionID == "" {
		return
	}
	haltMu.Lock()
	delete(halts, sessionID)
	haltMu.Unlock()
}

func (h *halt) list() []string {
	out := make([]string, 0, len(h.paths))
	for p := range h.paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// haltMessage is what the turn is told, as a note and as each refusal.
func haltMessage(paths []string) string {
	return fmt.Sprintf("Your change to %s was reverted while you were working: someone disagreed with it. "+
		"Change nothing more this turn and do not make that change again. Say what you had done and what was left, and ask how they want to go on.",
		strings.Join(paths, ", "))
}

// haltable makes def refuse while its session is halted. Applied to every
// tool that changes something; reading, searching and the plan stay open.
func (c *toolCtx) haltable(def AgentToolDef) AgentToolDef {
	h := def.Handler
	if h == nil {
		return def
	}
	def.Handler = func(ctx context.Context, args map[string]any) (string, error) {
		if paths := haltedPaths(c.session()); len(paths) > 0 {
			return "", fmt.Errorf("%s", haltMessage(paths))
		}
		return h(ctx, args)
	}
	return def
}
