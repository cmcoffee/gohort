// Package activity is the deployment's activity log: who signed in, who
// failed to, and the changes an administrator would want a record of (an
// account added or removed, a credential saved, a peer paired, a request
// approved, a setting changed). It answers "who did that, and when" after
// the fact, which the live views cannot: they show only what is running now.
//
// Events are kept for a set number of days (RetentionDays, an admin setting)
// and swept from the write path at most once an hour, so the log needs no
// maintenance pass of its own and an idle deployment does no work for it.
package activity

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the part of a database the log uses.
type Store interface {
	Set(table, key string, value interface{})
	Get(table, key string, output interface{}) bool
	Unset(table, key string)
	Keys(table string) []string
}

// Table holds the events, keyed by when they happened so the keys sort in
// time order.
const Table = "activity_log"

// The kinds of event. A kind names the subject, not the verb: the verb is in
// the event's Detail.
const (
	SignIn       = "sign_in"
	SignInFailed = "sign_in_failed"
	SignOut      = "sign_out"
	SignUp       = "sign_up"
	Account      = "account"
	Credential   = "credential"
	Peer         = "peer"
	Approval     = "approval"
	Settings     = "settings"
)

// Event is one thing that happened.
type Event struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// User is who acted, or for a failed sign-in the name that was tried.
	User string `json:"user,omitempty"`
	// IP and Browser are where the request came from, when there was one.
	IP      string `json:"ip,omitempty"`
	Browser string `json:"browser,omitempty"`
	// Detail says what happened, in words an administrator reads.
	Detail string `json:"detail"`
}

// RetentionDays is how long an event is kept. The framework points it at the
// admin setting; a value below one keeps a day.
var RetentionDays = func() int { return 90 }

// keyTime is the key's time prefix: fixed width and UTC, so the keys sort in
// time order as plain strings.
const keyTime = "2006-01-02T15:04:05.000000000Z"

var (
	pruneMu   sync.Mutex
	lastPrune time.Time
)

// pruneEvery is how often the write path sweeps old events. A failed sign-in
// flood writes many events quickly, and sweeping each time would read the
// whole table on every one of them.
const pruneEvery = time.Hour

// Record adds an event. Detail and Browser are cut to a readable length:
// both can carry what a stranger sent (a browser string, a name tried at the
// sign-in page), and the log is not where a large one should land.
func Record(db Store, e Event) {
	if db == nil || strings.TrimSpace(e.Kind) == "" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.User = clip(e.User, 120)
	e.Detail = clip(e.Detail, 400)
	e.Browser = clip(e.Browser, 200)
	e.ID = e.At.UTC().Format(keyTime) + "-" + randomSuffix()
	db.Set(Table, e.ID, e)
	maybePrune(db, e.At)
}

// Filter narrows a listing. Empty fields match everything.
type Filter struct {
	Kind  string
	User  string
	Limit int // 0 = every event kept
}

// List returns the events newest first.
func List(db Store, f Filter) []Event {
	if db == nil {
		return nil
	}
	keys := db.Keys(Table)
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var out []Event
	for _, k := range keys {
		var e Event
		if !db.Get(Table, k, &e) {
			continue
		}
		if f.Kind != "" && e.Kind != f.Kind {
			continue
		}
		if f.User != "" && !strings.EqualFold(e.User, f.User) {
			continue
		}
		out = append(out, e)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}

// Prune removes the events older than the retention window and reports how
// many it removed.
func Prune(db Store, now time.Time) int {
	if db == nil {
		return 0
	}
	days := RetentionDays()
	if days < 1 {
		days = 1
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(keyTime)
	n := 0
	for _, k := range db.Keys(Table) {
		// Keys sort in time order, so a key below the cutoff is older than it.
		if len(k) >= len(keyTime) && k[:len(keyTime)] < cutoff {
			db.Unset(Table, k)
			n++
		}
	}
	return n
}

func maybePrune(db Store, now time.Time) {
	pruneMu.Lock()
	if now.Sub(lastPrune) < pruneEvery {
		pruneMu.Unlock()
		return
	}
	lastPrune = now
	pruneMu.Unlock()
	Prune(db, now)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
