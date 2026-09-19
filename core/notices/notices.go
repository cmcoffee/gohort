// What an agent needs to tell its owner.
//
// One-way, which is why it is Notifications and not an inbox: an inbox is
// something you reply into, and gohort already has that in threads. Nothing
// here is answered. It is the record that something happened while nobody was
// looking, kept so that the looking can happen later.
//
// It exists because the alternative surfaces each answer a narrower question. A
// pending Authorization says the owner is BLOCKING something, and carries a
// badge that must keep meaning exactly that. A run record says how one fire
// went, which nobody reads unless they already suspect something. A cortex card
// reaches the agent's own thread, which is the last place to look for news that
// the agent could not do its job. None of them survive being the thing you
// check once a week.
//
// A subpackage rather than a file in core, for the reason core/pacing and
// core/notes are: core sits at its file ceiling (TestCoreStaysUnderItsCeiling),
// and every exported name there lands in the namespace of every file that
// dot-imports it.
//
// Storage only. WHO gets told, and over what transport, belongs to the app that
// knows what a phantom bridge is; this package knows what was said and how
// often.
package notices

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Table holds one Notice per (owner, fingerprint).
const Table = "core_notices"

// The kinds a notice can be. Kept small and named after what the READER has to
// decide, not after which code path wrote it: a kind that says "this is waiting
// on you" and one that says "this already stopped" are different messages, and
// everything else is detail inside the body.
const (
	// KindBlocked: something is waiting on the owner. It has a queue entry
	// somewhere and clearing it is an action they can take.
	KindBlocked = "blocked"
	// KindStopped: something did not happen, by a rule the owner already set.
	// Nothing is waiting; they may want to change the rule or the schedule.
	KindStopped = "stopped"
	// KindReport: the agent had something to say. No decision implied.
	KindReport = "report"
)

// Store is the slice of a key/value database this leaf uses. core's Database
// satisfies it structurally, so callers pass the handle they already have.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// Notice is one thing an agent told its owner, with every repeat of it folded
// in.
//
// Count is the whole reason this is not a log. A recurring task that fires
// hourly into a refusal produces the same sentence twenty-four times a day, and
// a surface that shows it twenty-four times is one the owner turns off inside a
// week, which means it is off when something new happens. So the same notice
// arriving again is the same row, with a bigger number and a newer Last.
type Notice struct {
	ID    string    `json:"id"`
	Owner string    `json:"owner"`
	Agent string    `json:"agent,omitempty"` // the agent it is about
	About string    `json:"about,omitempty"` // the task/schedule, when there is one
	Kind  string    `json:"kind"`
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	Read  bool      `json:"read,omitempty"`
}

// fingerprint is what makes two arrivals the same notice: same owner, same
// agent, same kind, same title. The BODY is deliberately out of it, because a
// body usually carries the particulars of one occurrence (a timestamp, an
// argument) and folding on it would defeat the folding.
func fingerprint(owner, agent, kind, title string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{owner, agent, kind, title}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func key(owner, id string) string { return owner + ":" + id }

// Record files a notice, folding it into an identical one already there.
//
// Returns the stored notice and whether this was the FIRST time it was said.
// The caller needs that distinction because forwarding it to somebody's phone
// belongs on the first occurrence and nowhere else: the twenty-fourth identical
// alert is not more informative than the first, it is just the one that makes
// them turn forwarding off.
//
// A repeat comes back UNREAD even if it had been read. It happened again, which
// is news about the world rather than about the message.
func Record(db Store, n Notice) (Notice, bool) {
	if db == nil || strings.TrimSpace(n.Owner) == "" || strings.TrimSpace(n.Title) == "" {
		return Notice{}, false
	}
	now := time.Now()
	if n.Kind == "" {
		n.Kind = KindReport
	}
	n.ID = fingerprint(n.Owner, n.Agent, n.Kind, n.Title)

	var prev Notice
	if db.Get(Table, key(n.Owner, n.ID), &prev) && prev.ID != "" {
		prev.Count++
		prev.Last = now
		prev.Read = false
		// The newest body wins: the particulars of the latest occurrence are
		// the ones worth having in front of you, and the older ones are exactly
		// what the count stands in for.
		if strings.TrimSpace(n.Body) != "" {
			prev.Body = n.Body
		}
		if strings.TrimSpace(n.About) != "" {
			prev.About = n.About
		}
		db.Set(Table, key(prev.Owner, prev.ID), prev)
		return prev, false
	}
	n.Count, n.First, n.Last, n.Read = 1, now, now, false
	db.Set(Table, key(n.Owner, n.ID), n)
	return n, true
}

// List returns an owner's notices, newest activity first, unread ahead of read.
//
// Unread first because the page answers "what do I not know yet"; within that,
// most recent, because an old unread notice that keeps recurring has a new Last
// and should not sink under a one-off from this morning.
func List(db Store, owner string) []Notice {
	if db == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	prefix := owner + ":"
	var out []Notice
	for _, k := range db.Keys(Table) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var n Notice
		if db.Get(Table, k, &n) && n.ID != "" {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Read != out[j].Read {
			return !out[i].Read
		}
		return out[i].Last.After(out[j].Last)
	})
	return out
}

// Unread is the badge. It counts NOTICES, not occurrences: a row that has
// happened forty times is one thing the owner has not looked at, and a badge
// reading 40 would be a number about the world rather than about them.
func Unread(db Store, owner string) int {
	n := 0
	for _, x := range List(db, owner) {
		if !x.Read {
			n++
		}
	}
	return n
}

// MarkRead marks one notice read. Marking read does NOT reset the count: the
// count is how often this has happened, which stays true after you have read it
// once, and a reader who clears it loses the one piece of evidence that says
// this is chronic rather than a blip.
func MarkRead(db Store, owner, id string) {
	setRead(db, owner, id, true)
}

// MarkAllRead is the one bulk action, because the alternative to offering it is
// an owner who stops opening the page.
func MarkAllRead(db Store, owner string) {
	for _, n := range List(db, owner) {
		if !n.Read {
			setRead(db, owner, n.ID, true)
		}
	}
}

func setRead(db Store, owner, id string, read bool) {
	if db == nil {
		return
	}
	var n Notice
	if !db.Get(Table, key(owner, id), &n) || n.ID == "" {
		return
	}
	n.Read = read
	db.Set(Table, key(owner, id), n)
}

// Remove deletes one notice. It comes back if the thing happens again, which is
// the correct behaviour for a condition and the reason Remove is not a way to
// silence anything.
func Remove(db Store, owner, id string) {
	if db != nil {
		db.Unset(Table, key(owner, id))
	}
}

// RemoveAll empties one owner's list.
//
// Safe to offer, for the same reason Remove is: nothing here is the only record
// of anything. A condition that is still true says so again on its next
// occurrence, and what this clears is the accumulated evidence that somebody
// has already seen. Without it the honest thing to do with a long list is
// nothing, and a list you cannot end is one you stop opening.
func RemoveAll(db Store, owner string) {
	if db == nil {
		return
	}
	for _, n := range List(db, owner) {
		db.Unset(Table, key(owner, n.ID))
	}
}
