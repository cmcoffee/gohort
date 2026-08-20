// A session id is not a capability.
//
// Every /api/live payload carries the ids of every running session, because the
// live ribbon is global and untenanted on purpose. The events behind an id are
// not public in the same way: they are the research question asked, the debate
// topic, the command run on somebody's box. MayView is the check that keeps
// those two facts apart, and these tests pin it — including the parts that are
// deliberately unhelpful, like refusing an untagged session to everyone.
package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

type ownEvent struct {
	Text string `json:"text"`
}

// ownershipMap builds a map holding one session per (id, owner) pair given.
func ownershipMap(t *testing.T, sessions map[string]string) *LiveSessionMap[ownEvent] {
	t.Helper()
	m := NewLiveSessionMap[ownEvent](0)
	for id, owner := range sessions {
		s := m.Register(id, "how do I tell my boss I am leaving", func() {})
		if owner != "" {
			s.SetOwner(owner)
		}
		m.AppendEvent(id, ownEvent{Text: "secret transcript"}, false)
	}
	return m
}

// withUsers points AuthDB at a store holding one user, so MayView takes the
// multi-tenant branch rather than the "auth disabled" one. Restores the
// previous hook on cleanup.
func withUsers(t *testing.T) {
	t.Helper()
	prev := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "craig", "pw", true)
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
}

func TestMayViewOwnerOnly(t *testing.T) {
	withUsers(t)
	m := ownershipMap(t, map[string]string{"s1": "craig"})

	if !m.MayView("craig", "s1") {
		t.Error("the owner must be able to view their own session")
	}
	if m.MayView("dana", "s1") {
		t.Error("another user must not view it")
	}
	if m.MayView("", "s1") {
		t.Error("an unauthenticated viewer must not view it")
	}
}

func TestMayViewUntaggedSessionIsNobodys(t *testing.T) {
	// Fail closed, the direction MaskedLabel already chose: a provider that has
	// not been taught to call SetOwner must not hand a transcript to everyone
	// just because it named no one.
	withUsers(t)
	m := ownershipMap(t, map[string]string{"orphan": ""})

	for _, viewer := range []string{"craig", "dana", ""} {
		if m.MayView(viewer, "orphan") {
			t.Errorf("viewer %q reached an untagged session", viewer)
		}
	}
}

func TestMayViewMissingSessionAnswersLikeNotYours(t *testing.T) {
	// One answer for "no such session" and "not yours", so probing cannot tell
	// whether somebody else is running something.
	withUsers(t)
	m := ownershipMap(t, map[string]string{"s1": "craig"})

	if m.MayView("craig", "nope") {
		t.Error("a missing session must not be viewable")
	}
}

func TestMayViewSingleTenantDeploymentAllowsAll(t *testing.T) {
	// No users configured means no one to be protected from, which is what
	// AuthMiddleware and RequestIsAdmin already assume in that state.
	prev := AuthDB
	AuthDB = nil
	t.Cleanup(func() { AuthDB = prev })

	m := NewLiveSessionMap[ownEvent](0)
	m.Register("s1", "topic", func() {})
	if !m.MayView("", "s1") {
		t.Error("with auth disabled every session must stay reachable")
	}
}

func TestOwnerOfDistinguishesMissingFromUntagged(t *testing.T) {
	m := ownershipMap(t, map[string]string{"tagged": "craig", "orphan": ""})

	if owner, ok := m.OwnerOf("tagged"); !ok || owner != "craig" {
		t.Errorf("tagged session: got (%q, %v)", owner, ok)
	}
	if owner, ok := m.OwnerOf("orphan"); !ok || owner != "" {
		t.Errorf("untagged session must exist with an empty owner, got (%q, %v)", owner, ok)
	}
	if _, ok := m.OwnerOf("nope"); ok {
		t.Error("a missing session must report not-found")
	}
}

// --- the handlers ------------------------------------------------------------

// asUser builds a request carrying a valid session cookie for username.
func asUser(t *testing.T, target, username string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if username != "" {
		token := AuthCreateSession(AuthDB(), username)
		t.Cleanup(func() { AuthDestroySession(AuthDB(), token) })
		r.AddCookie(&http.Cookie{Name: auth_cookie_name, Value: token})
	}
	return r
}

func TestHandleEventsRefusesAnotherUsersSession(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	m := ownershipMap(t, map[string]string{"s1": "craig"})
	h := m.HandleEvents()

	w := httptest.NewRecorder()
	h(w, asUser(t, "/api/events?id=s1", "dana"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("dana must get 404, got %d", w.Code)
	}
	if b := w.Body.String(); strings.Contains(b, "secret transcript") {
		t.Fatal("the transcript leaked in the refusal body")
	}

	w = httptest.NewRecorder()
	h(w, asUser(t, "/api/events?id=s1", "craig"))
	if w.Code != http.StatusOK {
		t.Fatalf("the owner must get their own events, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "secret transcript") {
		t.Error("the owner did not receive their own events")
	}
}

func TestHandleCancelRefusesAnotherUsersSession(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)

	m := NewLiveSessionMap[ownEvent](0)
	_, cancel := context.WithCancel(context.Background())
	cancelled := false
	m.Register("s1", "topic", func() { cancelled = true; cancel() }).SetOwner("craig")

	w := httptest.NewRecorder()
	m.HandleCancel("test")(w, asUser(t, "/api/cancel?id=s1", "dana"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("dana must get 404, got %d", w.Code)
	}
	if cancelled {
		t.Fatal("dana cancelled craig's run")
	}

	w = httptest.NewRecorder()
	m.HandleCancel("test")(w, asUser(t, "/api/cancel?id=s1", "craig"))
	if w.Code != http.StatusOK {
		t.Fatalf("the owner must be able to cancel, got %d", w.Code)
	}
	if !cancelled {
		t.Error("the owner's cancel did not take effect")
	}
}

func TestHandleLiveMasksOtherUsersLabels(t *testing.T) {
	// The per-app ribbon must not be a way around the masking the global
	// /api/live applies to the very same entries.
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	m := ownershipMap(t, map[string]string{"s1": "craig"})

	w := httptest.NewRecorder()
	m.HandleLive()(w, asUser(t, "/api/live", "dana"))

	var entries []LiveEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the entry should still be listed, just masked")
	}
	for _, e := range entries {
		if strings.Contains(e.Label, "boss") {
			t.Errorf("dana read craig's label: %q", e.Label)
		}
	}

	w = httptest.NewRecorder()
	m.HandleLive()(w, asUser(t, "/api/live", "craig"))
	_ = json.Unmarshal(w.Body.Bytes(), &entries)
	found := false
	for _, e := range entries {
		if strings.Contains(e.Label, "boss") {
			found = true
		}
	}
	if !found {
		t.Error("the owner must still see their own label")
	}
}
