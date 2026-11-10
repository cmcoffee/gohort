package imagefetch

// A chain of passes: the held edits that build one picture, each started by
// the wake of the one before. The attempt limit (image_attempts.go) counts by
// the source pictures' content, and every pass of a chain builds on the last
// one's result, so its sources are new each time and the limit never filled:
// a chain whose check kept failing ran pass after pass, about a minute and
// thirty thousand prompt tokens each, until somebody stopped it. And each
// pass redraws the whole picture, so building on a failed one compounds what
// it lost.
//
// So the chain is counted as one thing, per conversation: how many passes it
// has made, and how many in a row failed their check. It stops at a budget
// for the number of people in it, or after two failures in a row, and the
// pass after a failure is pointed at the last picture that passed, or at the
// picture the person asked to change when none has. A pass the person asked
// for (a turn answering their message, not a wake) starts a new count.
//
// Delivered passes count too. A delivered edit whose check failed woke a turn
// that tried again, building on the failed result, and that turn's result woke
// the next: the same loop with nothing held, which a count of held passes
// alone, ended by every delivery, never saw.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{Key: "tune_image_chain_passes", Category: "Images",
		Label:  "Passes in one chain of edits (most)",
		Help:   "The most edit passes one chain may make before it stops and delivers where it got to.",
		Detail: "A chain is the held passes that build one picture, each started when the last one finished: several people put into a scene one at a time, say. It also stops sooner, at two passes per person in it plus one, and after two passes in a row that fail their check. A new request from the person starts a new count.",
		Kind:   KindInt, Default: chainPassesDefault, Min: 2, Max: 40})
}

const (
	chainPassesDefault = 12
	// chainFailuresInARow stops a chain whose passes keep failing: a third
	// try at what failed twice almost never comes out different.
	chainFailuresInARow = 2
	// chainKeptFor forgets a chain left alone this long.
	chainKeptFor = 30 * time.Minute
)

type passChain struct {
	passes, failed int
	origin         string // the picture the person asked to change, by id
	lastGood       string // the last pass that passed its check, by id
	stopped        string // why it stopped; "" while it runs
	last           time.Time
}

var chains = struct {
	sync.Mutex
	m map[string]*passChain
}{m: map[string]*passChain{}}

// chainKey is the person and the conversation, "" when there is none.
func chainKey(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	convo := firstNonBlank(sess.DeliverySessionID, sess.ChatSessionID)
	if convo == "" {
		return ""
	}
	return sess.Username + "\x1f" + convo
}

// chainLimit is the most passes a chain of edits with refs may make: two per
// person in it (the pictures beside the scene) plus one, within the
// deployment's ceiling.
func chainLimit(refs int) int {
	ceiling := TuneInt("tune_image_chain_passes")
	if ceiling < 2 {
		ceiling = chainPassesDefault
	}
	people := refs - 1
	if people < 1 {
		people = 1
	}
	return min(2*people+1, ceiling)
}

// chainBegin is asked before a pass renders: a refusal when the chain has
// stopped or now must. A pass from a turn answering the person starts a new
// count.
func chainBegin(sess *ToolSession, sources []string, now time.Time) error {
	refs := len(sources)
	key := chainKey(sess)
	if key == "" {
		return nil
	}
	chains.Lock()
	defer chains.Unlock()
	for k, c := range chains.m {
		if now.Sub(c.last) > chainKeptFor {
			delete(chains.m, k)
		}
	}
	c := chains.m[key]
	if c == nil || strings.TrimSpace(sess.Request) != "" {
		origin := ""
		if len(sources) > 0 {
			origin = lastingRef(sess, sources[0])
		}
		chains.m[key] = &passChain{last: now, origin: origin}
		return nil
	}
	c.last = now
	switch limit := chainLimit(refs); {
	case c.stopped != "":
	case c.passes >= limit:
		c.stopped = fmt.Sprintf("it has made %d passes, the most a chain with this many people gets", c.passes)
	case c.failed >= chainFailuresInARow:
		c.stopped = fmt.Sprintf("its last %d passes did not pass their check", c.failed)
	default:
		return nil
	}
	send := "the latest pass"
	if c.origin != "" {
		send = "the latest pass, or " + c.origin + " (the picture they asked to change) if every pass came out worse"
	}
	if c.lastGood != "" {
		send = c.lastGood + ", the last pass that passed its check"
	}
	return fmt.Errorf("no picture was made: this chain of passes has stopped, because %s. Start NO further pass. "+
		"Send %s as where it got to, and tell the person plainly who or what did not come through. "+
		"If they want to go on, they can ask, and their request starts a new count", c.stopped, send)
}

// chainRecord counts a pass that rendered, held or delivered: whether its
// check ran and failed, and the id it was saved as. It returns the picture a
// failed pass's retry should build on: the last that passed, else the one the
// person asked to change ("" when neither is known).
func chainRecord(sess *ToolSession, checked, failed bool, saved string, now time.Time) string {
	key := chainKey(sess)
	if key == "" {
		return ""
	}
	chains.Lock()
	defer chains.Unlock()
	c := chains.m[key]
	if c == nil {
		c = &passChain{}
		chains.m[key] = c
	}
	c.passes++
	c.last = now
	switch {
	case checked && failed:
		c.failed++
	case saved != "":
		c.failed = 0
		c.lastGood = saved
	}
	if c.lastGood != "" {
		return c.lastGood
	}
	return c.origin
}

// chainRetryNote points the pass after a failed one at the picture to build
// on, so a retry does not build on what it is retrying.
func chainRetryNote(base string) string {
	if base == "" {
		return ""
	}
	return " If you try again, edit " + base + " as picture, not this result: every pass redraws the whole picture, so building on a failed one keeps what it lost and loses more."
}

// lastingRef is ref as a name that still works in a later turn: a photo sent
// with this message (media#N, which names nothing once the turn is over) by
// its stable ring id; anything else as given. A retry pointed at media#1 from
// the wake its result started was refused, and the agent built on its own
// failed result instead.
func lastingRef(sess *ToolSession, ref string) string {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), "media#") {
		return ref
	}
	if data, ok := sourceData(sess, ref); ok {
		if id := sess.LastingImageID(data); id != "" {
			return id
		}
	}
	return ref
}
