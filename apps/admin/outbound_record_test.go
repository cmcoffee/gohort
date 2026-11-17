package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/outbound"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Outbound record through the real routes: only an administrator reads
// it, sets the query check or clears it; the check takes only its three
// modes; the events read with who they were for.
func TestTheOutboundRecordThroughItsRoutes(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	outbound.SetStore(RootDB)
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot; outbound.SetStore(nil) })

	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	outbound.Record(outbound.Event{Kind: outbound.KindSearch, Dest: "Serper", Detail: "kestrel control backoff",
		Reason: "the query names \"Kestrel Control\"", Action: outbound.ActionWatched,
		Actor: outbound.Actor{AgentName: "Coder", Owner: "alice"}})

	if code, _ := do("alice", "GET", "/api/outbound-events", ""); code < 400 {
		t.Fatalf("a user who is not an administrator read the record: %d", code)
	}
	code, body := do("root", "GET", "/api/outbound-events", "")
	if code != 200 || !strings.Contains(body, `"who":"Coder (agent of alice)"`) || !strings.Contains(body, `"kind":"Web search"`) {
		t.Fatalf("the record: %d %s", code, body)
	}

	if code, _ := do("alice", "POST", "/api/outbound-settings", `{"query_check":"off"}`); code < 400 || outbound.QueryMode() != outbound.ModeWatch {
		t.Fatalf("a user who is not an administrator turned the check off: %d", code)
	}
	if code, _ := do("root", "POST", "/api/outbound-settings", `{"query_check":"sometimes"}`); code != 400 {
		t.Errorf("a mode that is not one was taken: %d", code)
	}
	if code, _ := do("root", "POST", "/api/outbound-settings", `{"query_check":"refuse"}`); code != 200 || outbound.QueryMode() != outbound.ModeRefuse {
		t.Errorf("Refuse was not set: %d %s", code, outbound.QueryMode())
	}

	if code, _ := do("alice", "POST", "/api/outbound-events/clear", `{}`); code < 400 || len(outbound.Events(0, nil)) != 1 {
		t.Fatalf("a user who is not an administrator cleared the record: %d", code)
	}
	if code, _ := do("root", "POST", "/api/outbound-events/clear", `{}`); code != 200 || len(outbound.Events(0, nil)) != 0 {
		t.Errorf("Clear: %d, %d left", code, len(outbound.Events(0, nil)))
	}
}
