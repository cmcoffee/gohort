package core

import (
	"testing"
)

// liveEntryAppPath decides whether a live entry can be gated at all: a
// non-empty result is a mount prefix the access check runs against, and ""
// means "no way back into an app", which the live pill reads as Monitor-only.
// Getting that wrong either leaks a deep link past the access check or sends
// a reachable session to the Monitor, so the derivation is pinned here.
func TestLiveEntryAppPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry LiveEntry
		want  string
	}{
		{"path wins over url", LiveEntry{Path: "/servitor", URL: "/other/?reconnect=x"}, "/servitor"},
		{"bare path", LiveEntry{Path: "/servitor"}, "/servitor"},
		{"url with reconnect query", LiveEntry{URL: "/servitor/?reconnect=abc"}, "/servitor"},
		{"url without trailing slash", LiveEntry{URL: "/servitor"}, "/servitor"},
		{"nested url gates on the mount prefix", LiveEntry{URL: "/servitor/sub/page?reconnect=x"}, "/servitor"},
		{"url with fragment", LiveEntry{URL: "/servitor/#top"}, "/servitor"},

		// No route back. Agent runs from the runs registry and queued tasks
		// carry a label and a status but no owning page — the Monitor case.
		{"empty entry", LiveEntry{}, ""},
		{"label only", LiveEntry{ID: "r1", Label: "some agent run", App: "Scout"}, ""},
		{"root url", LiveEntry{URL: "/"}, ""},

		// Not gateable: an off-host URL can't be resolved to a local app, so
		// it must not be treated as one and silently pass the access check.
		{"absolute url", LiveEntry{URL: "https://example.com/servitor/"}, ""},
		{"relative url", LiveEntry{URL: "servitor/?reconnect=x"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveEntryAppPath(tc.entry); got != tc.want {
				t.Errorf("liveEntryAppPath(%+v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// ResolveHref is what both the live pill and the Monitor table navigate to,
// so a wrong result here is a dead link on two surfaces at once.
func TestLiveEntryResolveHref(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry LiveEntry
		want  string
	}{
		{"explicit url wins", LiveEntry{ID: "s1", Path: "/servitor", URL: "/servitor/?reconnect=s1"}, "/servitor/?reconnect=s1"},
		{"path plus id", LiveEntry{ID: "s1", Path: "/servitor"}, "/servitor/?reconnect=s1"},
		{"path with trailing slash", LiveEntry{ID: "s1", Path: "/servitor/"}, "/servitor/?reconnect=s1"},
		{"id is escaped", LiveEntry{ID: "a b&c", Path: "/servitor"}, "/servitor/?reconnect=a+b%26c"},

		// Nothing to link to — the pill falls back to the live view and the
		// Monitor leaves the row inert.
		{"path without id", LiveEntry{Path: "/servitor"}, ""},
		{"id without path", LiveEntry{ID: "s1"}, ""},
		{"empty", LiveEntry{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.ResolveHref(); got != tc.want {
				t.Errorf("ResolveHref(%+v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// A live entry may name a second place for its owner to go — the conversation
// the work is running in — beside the place everyone else may look. The
// substitution is the owner's alone, and an entry with no owner has none.
func TestLiveEntryOwnerDestination(t *testing.T) {
	base := LiveEntry{ID: "r1", Owner: "craig", URL: "/monitor?run=r1", OwnerURL: "/orchestrate/?agent=a1&session=s1"}

	e := base
	e.applyOwnerDestination("craig")
	if e.URL != "/orchestrate/?agent=a1&session=s1" {
		t.Errorf("the owner goes to the conversation, got %q", e.URL)
	}
	for _, viewer := range []string{"dana", ""} {
		e = base
		e.applyOwnerDestination(viewer)
		if e.URL != "/monitor?run=r1" {
			t.Errorf("viewer %q must keep the shared destination, got %q", viewer, e.URL)
		}
	}
	e = base
	e.Owner = ""
	e.applyOwnerDestination("craig")
	if e.URL != "/monitor?run=r1" {
		t.Errorf("an entry with no owner has no owner's destination, got %q", e.URL)
	}
	e = base
	e.OwnerURL = ""
	e.applyOwnerDestination("craig")
	if e.URL != "/monitor?run=r1" {
		t.Errorf("no OwnerURL means the owner goes where everyone goes, got %q", e.URL)
	}
}
