package core

import "testing"

// TestSharedPersistentTempTools covers the deployment-wide shared pool: a tool
// is private to its owner until marked Shared, then surfaces in the shared pool
// (one per name: a second owner's same-named publish is refused), and drops back
// out when unshared.
func TestSharedPersistentTempTools(t *testing.T) {
	db := OpenCache()

	for _, tt := range []struct{ user, name string }{
		{"alice", "weather"}, {"alice", "deploy"}, {"bob", "weather"},
	} {
		if err := AdminPersistTempTool(db, tt.user, TempTool{Name: tt.name}); err != nil {
			t.Fatalf("persist %s/%s: %v", tt.user, tt.name, err)
		}
	}

	// Nothing shared yet.
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Fatalf("no tools shared yet, got %d", len(got))
	}

	// Sharing a tool that isn't in the pool errors.
	if err := SetPersistentTempToolShared(db, "alice", "nope", true); err == nil {
		t.Error("sharing a missing tool should error")
	}

	// Share alice's weather + deploy. Bob's weather is REFUSED: the deployment
	// publishes one tool per name, because two would leave every lookup by name
	// to pick one, and whichever it picked is whose code an adopter runs.
	for _, tt := range []struct{ user, name string }{
		{"alice", "weather"}, {"alice", "deploy"},
	} {
		if err := SetPersistentTempToolShared(db, tt.user, tt.name, true); err != nil {
			t.Fatalf("share %s/%s: %v", tt.user, tt.name, err)
		}
	}
	if err := SetPersistentTempToolShared(db, "bob", "weather", true); err == nil {
		t.Fatal("a second published tool called weather was allowed")
	}
	got := LoadSharedPersistentTempTools(db)
	names := map[string]int{}
	for _, p := range got {
		if !p.Shared {
			t.Errorf("shared pool returned a non-shared tool %q", p.Tool.Name)
		}
		names[p.Tool.Name]++
	}
	if names["weather"] != 1 || names["deploy"] != 1 || len(got) != 2 {
		t.Fatalf("shared pool = %v (want one weather + one deploy)", names)
	}

	// Once alice unpublishes weather, bob may publish his.
	if err := SetPersistentTempToolShared(db, "alice", "weather", false); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, "bob", "weather", true); err != nil {
		t.Fatalf("bob could not publish weather once the name was free: %v", err)
	}
	if got := LoadSharedPersistentTempTools(db); len(got) != 2 {
		t.Errorf("bob shares weather + alice shares deploy → 2, got %d", len(got))
	}
	// Unshare the rest → empty pool.
	_ = SetPersistentTempToolShared(db, "bob", "weather", false)
	_ = SetPersistentTempToolShared(db, "alice", "deploy", false)
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Errorf("after unsharing all, expected empty pool, got %d", len(got))
	}
}
