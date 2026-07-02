package core

import "testing"

// TestUpsertGraphEntityMerge: a second mention under a different alias lands
// on the SAME node (alias-based consolidation), folding aliases + attrs in.
func TestUpsertGraphEntityMerge(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"

	e1, new1 := UpsertGraphEntity(db, ns, "person", "Robin", nil, map[string]string{"title": "VP"})
	if !new1 || e1.ID == "" {
		t.Fatalf("first upsert should create: new=%v id=%q", new1, e1.ID)
	}
	// Same person, different surface form + an alias + a new attr.
	e2, new2 := UpsertGraphEntity(db, ns, "person", "Robin", []string{"Robin Vale"}, map[string]string{"email": "robin@acme.com"})
	if new2 {
		t.Fatalf("second upsert should merge, not create new")
	}
	if e2.ID != e1.ID {
		t.Fatalf("merge landed on a different node: %q vs %q", e2.ID, e1.ID)
	}
	if e2.Attrs["title"] != "VP" || e2.Attrs["email"] != "robin@acme.com" {
		t.Fatalf("attrs not merged: %+v", e2.Attrs)
	}
	// Now look up by the alias — must resolve to the same node.
	got, ok := FindGraphEntity(db, ns, "robin vale")
	if !ok || got.ID != e1.ID {
		t.Fatalf("alias lookup failed: ok=%v id=%q", ok, got.ID)
	}
	if ents, _ := GraphCounts(db, ns); ents != 1 {
		t.Fatalf("expected 1 entity after merge, got %d", ents)
	}
}

// TestLinkGraphEdgeReplace: replace=true is delete-on-update for a single-
// valued relation; replace=false lets siblings coexist (multi-valued).
func TestLinkGraphEdgeReplace(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	robin, _ := UpsertGraphEntity(db, ns, "person", "Robin", nil, nil)
	acme, _ := UpsertGraphEntity(db, ns, "org", "Acme", nil, nil)
	globex, _ := UpsertGraphEntity(db, ns, "org", "Globex", nil, nil)

	LinkGraphEdge(db, ns, robin.ID, "works at", acme.ID, "", false)
	// Single-valued correction — Acme should be gone.
	LinkGraphEdge(db, ns, robin.ID, "works at", globex.ID, "", true)
	out := GraphEdgesFrom(db, ns, robin.ID)
	if len(out) != 1 || out[0].To != globex.ID {
		t.Fatalf("replace should leave only Globex, got %+v", out)
	}

	// Multi-valued — two "knows" coexist.
	morgan, _ := UpsertGraphEntity(db, ns, "person", "Morgan", nil, nil)
	casey, _ := UpsertGraphEntity(db, ns, "person", "Casey", nil, nil)
	LinkGraphEdge(db, ns, robin.ID, "knows", morgan.ID, "", false)
	LinkGraphEdge(db, ns, robin.ID, "knows", casey.ID, "", false)
	knows := 0
	for _, e := range GraphEdgesFrom(db, ns, robin.ID) {
		if e.Rel == "knows" {
			knows++
		}
	}
	if knows != 2 {
		t.Fatalf("multi-valued knows should coexist, got %d", knows)
	}

	// Inbound lookup from Globex's side resolves back to Robin.
	in := GraphEdgesTo(db, ns, globex.ID)
	if len(in) != 1 || in[0].From != robin.ID {
		t.Fatalf("inbound edge lookup failed: %+v", in)
	}
}

// TestGraphNamespaceIsolation: entities/edges in one agent's namespace are
// invisible to another's.
func TestGraphNamespaceIsolation(t *testing.T) {
	db := memDB(t)
	UpsertGraphEntity(db, "agent:1", "person", "Robin", nil, nil)
	if _, ok := FindGraphEntity(db, "agent:2", "Robin"); ok {
		t.Fatalf("entity leaked across namespaces")
	}
	if ents, _ := GraphCounts(db, "agent:2"); ents != 0 {
		t.Fatalf("namespace 2 should be empty, got %d", ents)
	}
}

// TestGraphRelSlug: relation verbs are slugged so the pipe-delimited key
// stays unambiguous and re-stating the same triple updates rather than dups.
func TestGraphRelSlug(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	a, _ := UpsertGraphEntity(db, ns, "person", "A", nil, nil)
	b, _ := UpsertGraphEntity(db, ns, "person", "B", nil, nil)
	LinkGraphEdge(db, ns, a.ID, "Reports To", b.ID, "", false)
	LinkGraphEdge(db, ns, a.ID, "reports  to", b.ID, "", false) // same after slug
	out := GraphEdgesFrom(db, ns, a.ID)
	if len(out) != 1 {
		t.Fatalf("expected 1 edge after slug-collapsed restatement, got %d: %+v", len(out), out)
	}
	if out[0].Rel != "reports_to" {
		t.Fatalf("rel not slugged: %q", out[0].Rel)
	}
}
