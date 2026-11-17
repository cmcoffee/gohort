package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// memStore is a Store in memory, values kept as JSON as the database keeps
// them encoded.
type memStore map[string]map[string][]byte

func (m memStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table][key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, out) == nil
}
func (m memStore) Set(table, key string, v interface{}) {
	if m[table] == nil {
		m[table] = map[string][]byte{}
	}
	m[table][key], _ = json.Marshal(v)
}
func (m memStore) Unset(table, key string) { delete(m[table], key) }
func (m memStore) Keys(table string) []string {
	var out []string
	for k := range m[table] {
		out = append(out, k)
	}
	return out
}

func withStore(t *testing.T) memStore {
	t.Helper()
	m := memStore{}
	SetStore(m)
	t.Cleanup(func() { SetStore(nil) })
	return m
}

// A query naming the work's private terms is an event: watched (sent) by
// default, refused when an administrator says so, and nothing when the
// check is off. A query that is fine, or one going somewhere inside the
// network, is no event at all.
func TestAQueryNamingPrivateWorkIsWatchedThenRefused(t *testing.T) {
	withStore(t)
	ctx := WithActor(WithPrivateTerms(context.Background(), "Kestrel Control"),
		Actor{Agent: "a1", AgentName: "Coder", Owner: "alice", User: "bob"})
	q := Query{Kind: KindSearch, Dest: "DuckDuckGo", Text: "kestrel control reconcile backoff"}

	if CheckQuery(ctx, Query{Kind: KindSearch, Dest: "DuckDuckGo", Text: "kubernetes reconcile backoff"}) != "" || len(Events(0, nil)) != 0 {
		t.Fatal("a general query was stopped or recorded")
	}
	if QueryMode() != ModeWatch {
		t.Fatalf("the check starts in %q, want watch", QueryMode())
	}
	if CheckQuery(ctx, q) != "" {
		t.Fatal("a watched query was refused")
	}
	ev := Events(0, nil)
	if len(ev) != 1 || ev[0].Action != ActionWatched || ev[0].Kind != KindSearch || ev[0].Dest != "DuckDuckGo" ||
		!strings.Contains(ev[0].Reason, `"Kestrel Control"`) || ev[0].Detail != q.Text || ev[0].Who() != "Coder (agent of alice) for bob" {
		t.Fatalf("the watched event: %+v", ev)
	}

	SetQueryMode(ModeRefuse)
	refusal := CheckQuery(ctx, q)
	if !strings.HasPrefix(refusal, "NOT SENT") || !strings.Contains(refusal, "general, public concept") {
		t.Fatalf("a refused query says: %q", refusal)
	}
	if ev := Events(0, nil); len(ev) != 2 || ev[0].Action != ActionRefused {
		t.Fatalf("the refusal was not recorded first: %+v", ev)
	}

	asked := false
	inside := Query{Kind: KindSource, Dest: "wiki", Text: q.Text, Internal: func() bool { asked = true; return true }}
	if CheckQuery(ctx, inside) != "" || len(Events(0, nil)) != 2 || !asked {
		t.Fatal("a query to a source inside the network was checked")
	}
	asked = false
	CheckQuery(ctx, Query{Kind: KindSource, Dest: "wiki", Text: "kubernetes", Internal: func() bool { asked = true; return true }})
	if asked {
		t.Error("where a clean query goes was looked up (a DNS lookup per search for nothing)")
	}

	SetQueryMode(ModeOff)
	if CheckQuery(ctx, q) != "" || len(Events(0, nil)) != 2 {
		t.Fatal("with the check off a query was stopped or recorded")
	}
	SetQueryMode("nonsense")
	if QueryMode() != ModeWatch {
		t.Error("a mode that is not one reads as something other than watch")
	}
}

// Events come back newest first, filtered, and the oldest go past the cap.
func TestTheRecordKeepsTheNewest(t *testing.T) {
	m := withStore(t)
	base := time.Now()
	for i := 0; i < maxEvents+150; i++ {
		Record(Event{At: base.Add(time.Duration(i) * time.Millisecond), Kind: KindWrite, Dest: fmt.Sprintf("h%d", i), Action: ActionRefused})
	}
	if n := len(m.Keys(eventsTable)); n > maxEvents+100 {
		t.Fatalf("%d kept, the cap is %d (trimmed every hundred)", n, maxEvents)
	}
	ev := Events(3, nil)
	if len(ev) != 3 || ev[0].Dest != fmt.Sprintf("h%d", maxEvents+149) {
		t.Fatalf("newest first: %+v", ev)
	}
	odd := Events(2, func(e Event) bool { return strings.HasSuffix(e.Dest, "1") })
	if len(odd) != 2 || !strings.HasSuffix(odd[0].Dest, "1") {
		t.Fatalf("filtered: %+v", odd)
	}
	Clear()
	if len(Events(0, nil)) != 0 {
		t.Fatal("Clear left events")
	}
}

func TestWhoNamesTheAgentAndThePerson(t *testing.T) {
	cases := map[Actor]string{
		{}:                                   "the deployment itself",
		{Agent: "a1"}:                        "a1",
		{AgentName: "Coder", Owner: "alice"}: "Coder (agent of alice)",
		{AgentName: "Coder", Owner: "alice", User: "alice"}: "Coder (agent of alice)",
		{User: "bob"}: "bob",
	}
	for a, want := range cases {
		if got := a.Who(); got != want {
			t.Errorf("%+v: %q, want %q", a, got, want)
		}
	}
}
