// Package outbound is the record of what agents send out of the deployment
// that someone should see: a search query naming private work, a source
// asked something it should not be, a request to a site nobody trusted. It
// keeps those as events an administrator reads under Oversight, beside the
// rules that decide them, and it holds the one check every outgoing query
// is put through (CheckQuery).
//
// A leaf: it imports nothing of gohort's, so core, the tools and the apps
// can all record here. The database arrives through SetStore at startup.
//
// What is recorded is the exception, not the traffic: a query that was
// fine, or a write to a host the owner trusted, is not an event. A run of
// events from one agent is how a misconfigured agent shows itself, and a
// deliberate one leaves the same trail.
package outbound

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// Store is the slice of the database this package needs; core's Database
// satisfies it.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

const (
	eventsTable   = "outbound_events"
	settingsTable = "outbound_settings"
	// maxEvents is how many are kept: the newest, the rest dropped as new
	// ones arrive. Enough for weeks of exceptions; a flood of them is itself
	// the thing to look at, and the oldest are the least useful.
	maxEvents = 5000
)

var (
	storeMu sync.Mutex
	store   Store
	written int // events recorded since the last trim
)

// SetStore is where events and settings are kept. Until it is called,
// events are logged and not kept.
func SetStore(s Store) {
	storeMu.Lock()
	store = s
	storeMu.Unlock()
}

// Actor is who an outgoing call was made for: the agent, its owner, and the
// person the turn answers.
type Actor struct {
	Agent     string `json:"agent,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Owner     string `json:"owner,omitempty"`
	User      string `json:"user,omitempty"`
}

// Who is the actor as one line for a person: "Helper (agent of alice) for bob".
func (a Actor) Who() string {
	name := a.AgentName
	if name == "" {
		name = a.Agent
	}
	var b strings.Builder
	if name != "" {
		b.WriteString(name)
		if a.Owner != "" {
			b.WriteString(" (agent of " + a.Owner + ")")
		}
	}
	if a.User != "" && a.User != a.Owner {
		if b.Len() > 0 {
			b.WriteString(" for ")
		}
		b.WriteString(a.User)
	}
	if b.Len() == 0 {
		return "the deployment itself"
	}
	return b.String()
}

type actorKey struct{}

// WithActor returns ctx naming who its outgoing calls are made for.
func WithActor(ctx context.Context, a Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorOf is who ctx's calls are made for, the zero Actor for nobody named.
func ActorOf(ctx context.Context) Actor {
	if ctx == nil {
		return Actor{}
	}
	a, _ := ctx.Value(actorKey{}).(Actor)
	return a
}

// Kinds of outgoing call an event is about.
const (
	KindSearch = "search" // a web search query
	KindSource = "source" // a query to a search connector or research source
	KindWrite  = "write"  // a request that sends data (POST, PUT, ...)
	KindTunnel = "tunnel" // an HTTPS connection through a run's proxy
)

// What happened to it.
const (
	ActionWatched  = "watched"  // flagged and let through: the check is watching
	ActionRefused  = "refused"  // stopped
	ActionDeclined = "declined" // the person asked said no
	ActionAllowed  = "allowed"  // the person asked said yes
)

// Event is one outgoing call someone should see.
type Event struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Dest   string    `json:"dest"`   // the host or service it was going to
	Detail string    `json:"detail"` // the query, or the request line
	Reason string    `json:"reason"` // why it is an event
	Action string    `json:"action"`
	Actor
}

var seq struct {
	sync.Mutex
	last int64
}

// eventID sorts by time: a nanosecond stamp, nudged forward when two land
// in the same one.
func eventID(at time.Time) string {
	seq.Lock()
	defer seq.Unlock()
	n := at.UnixNano()
	if n <= seq.last {
		n = seq.last + 1
	}
	seq.last = n
	return fmt.Sprintf("%020d", n)
}

// Record keeps e (stamped and given an ID) and logs it.
func Record(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.ID = eventID(e.At)
	if r := []rune(e.Detail); len(r) > 500 {
		e.Detail = string(r[:500]) + "..."
	}
	nfo.Log("[outbound] %s %s to %s by %s: %s", e.Action, e.Kind, e.Dest, e.Who(), e.Reason)
	storeMu.Lock()
	defer storeMu.Unlock()
	if store == nil {
		return
	}
	store.Set(eventsTable, e.ID, e)
	written++
	if written >= 100 {
		written = 0
		trim(store)
	}
}

// trim drops the oldest events past maxEvents. Every hundred records, not
// every one: listing the keys is the cost.
func trim(s Store) {
	keys := s.Keys(eventsTable)
	if len(keys) <= maxEvents {
		return
	}
	sort.Strings(keys)
	for _, k := range keys[:len(keys)-maxEvents] {
		s.Unset(eventsTable, k)
	}
}

// Events are the kept events, newest first, at most limit (0 for all) of
// those keep says to (nil keeps every one).
func Events(limit int, keep func(Event) bool) []Event {
	storeMu.Lock()
	s := store
	storeMu.Unlock()
	if s == nil {
		return nil
	}
	keys := s.Keys(eventsTable)
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var out []Event
	for _, k := range keys {
		var e Event
		if !s.Get(eventsTable, k, &e) {
			continue
		}
		if keep != nil && !keep(e) {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Clear drops every kept event.
func Clear() {
	storeMu.Lock()
	defer storeMu.Unlock()
	if store == nil {
		return
	}
	for _, k := range store.Keys(eventsTable) {
		store.Unset(eventsTable, k)
	}
}

// Modes of a check: Watch records what it would stop and stops nothing, so
// it can be judged on real traffic before it is trusted to; Refuse stops it.
const (
	ModeOff    = "off"
	ModeWatch  = "watch"
	ModeRefuse = "refuse"
)

const queryModeKey = "query_check"

// QueryMode is what the check on outgoing queries does: Watch until an
// administrator sets otherwise.
func QueryMode() string {
	storeMu.Lock()
	s := store
	storeMu.Unlock()
	var m string
	if s != nil {
		s.Get(settingsTable, queryModeKey, &m)
	}
	switch m {
	case ModeOff, ModeRefuse:
		return m
	}
	return ModeWatch
}

// SetQueryMode sets it; anything not a mode reads as Watch.
func SetQueryMode(m string) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if store != nil {
		store.Set(settingsTable, queryModeKey, m)
	}
}

// Query is one query about to leave for a service.
type Query struct {
	Kind string // KindSearch or KindSource
	Dest string // where it is going, as a person would name it
	Text string
	// Internal reports whether Dest is inside the deployment's own network,
	// where the query is not leaving at all. Asked only when the query would
	// otherwise be an event, since answering may take a DNS lookup.
	Internal func() bool
}

// CheckQuery puts q through the check: "" to send it, or the refusal the
// caller returns instead of sending. A query that names what ctx's work
// marks private, or carries a ticket ID or an internal address, is an event:
// watched (sent) or refused, as QueryMode says.
func CheckQuery(ctx context.Context, q Query) string {
	why := Leak(q.Text, PrivateTerms(ctx))
	if why == "" {
		return ""
	}
	mode := QueryMode()
	if mode == ModeOff {
		return ""
	}
	if q.Internal != nil && q.Internal() {
		return ""
	}
	e := Event{Kind: q.Kind, Dest: q.Dest, Detail: q.Text, Reason: "the query " + why, Actor: ActorOf(ctx)}
	if mode != ModeRefuse {
		e.Action = ActionWatched
		Record(e)
		return ""
	}
	e.Action = ActionRefused
	Record(e)
	return fmt.Sprintf("NOT SENT: this query %s, and it would go to %s, outside this deployment. Search for the general, public concept instead (the technology, library, error or technique), without this work's own names, ticket IDs or hosts.", why, q.Dest)
}
