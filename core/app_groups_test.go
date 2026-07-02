package core

import (
	"sort"
	"testing"
)

func sortedEq(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAppGroupsResolution(t *testing.T) {
	db := OpenCache()

	writers, err := SaveAppGroup(db, AppGroup{Name: "Writers", Apps: []string{"/guides", "/techwriter"}})
	if err != nil {
		t.Fatalf("save Writers: %v", err)
	}
	ops, err := SaveAppGroup(db, AppGroup{Name: "Ops", Apps: []string{"/servitor", "/guides"}})
	if err != nil {
		t.Fatalf("save Ops: %v", err)
	}

	// ExpandAppGroups unions + dedupes across groups (/guides appears in both).
	if got := ExpandAppGroups(db, []string{writers.ID, ops.ID}); !sortedEq(got, []string{"/guides", "/techwriter", "/servitor"}) {
		t.Errorf("ExpandAppGroups = %v, want the deduped union", got)
	}

	// A name is required.
	if _, err := SaveAppGroup(db, AppGroup{Name: "  "}); err == nil {
		t.Error("empty name should error")
	}

	// Explicit apps + a group: union of both.
	u := AuthUser{Apps: []string{"/chat"}, Groups: []string{writers.ID}}
	if got := AuthResolveUserApps(db, u); !sortedEq(got, []string{"/chat", "/guides", "/techwriter"}) {
		t.Errorf("resolve(apps+group) = %v", got)
	}

	// Neither apps nor groups → deployment defaults.
	AuthSetDefaultApps(db, []string{"/home"})
	if got := AuthResolveUserApps(db, AuthUser{}); !sortedEq(got, []string{"/home"}) {
		t.Errorf("resolve(empty) should fall back to defaults, got %v", got)
	}

	// Having a group (even an unknown one) opts the user OUT of defaults; an
	// unknown id just resolves to nothing.
	if got := AuthResolveUserApps(db, AuthUser{Groups: []string{"does-not-exist"}}); len(got) != 0 {
		t.Errorf("unknown group should resolve to no apps (not defaults), got %v", got)
	}

	// Deleting a group makes it resolve to nothing; the other group is unaffected.
	if err := DeleteAppGroup(db, writers.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := AuthResolveUserApps(db, AuthUser{Groups: []string{writers.ID, ops.ID}}); !sortedEq(got, []string{"/servitor", "/guides"}) {
		t.Errorf("after delete, resolve = %v, want only Ops apps", got)
	}
}
