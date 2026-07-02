package core

import "testing"

// TestSharedPersistentTempTools covers the deployment-wide shared pool: a tool
// is private to its owner until marked Shared, then surfaces in the shared pool
// (deduped by name across owners), and drops back out when unshared.
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

	// Share alice's weather + deploy, and bob's weather. The shared pool dedupes
	// by name, so "weather" appears once even though two owners share it.
	for _, tt := range []struct{ user, name string }{
		{"alice", "weather"}, {"alice", "deploy"}, {"bob", "weather"},
	} {
		if err := SetPersistentTempToolShared(db, tt.user, tt.name, true); err != nil {
			t.Fatalf("share %s/%s: %v", tt.user, tt.name, err)
		}
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

	// Unsharing alice's weather still leaves bob's weather shared.
	if err := SetPersistentTempToolShared(db, "alice", "weather", false); err != nil {
		t.Fatal(err)
	}
	if got := LoadSharedPersistentTempTools(db); len(got) != 2 {
		t.Errorf("bob still shares weather + alice shares deploy → 2, got %d", len(got))
	}
	// Unshare the rest → empty pool.
	_ = SetPersistentTempToolShared(db, "bob", "weather", false)
	_ = SetPersistentTempToolShared(db, "alice", "deploy", false)
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Errorf("after unsharing all, expected empty pool, got %d", len(got))
	}
}
