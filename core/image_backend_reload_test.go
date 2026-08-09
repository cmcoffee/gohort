// "Any save should reload the entire image generation."
//
// It didn't. Materialize registered the row that changed and nothing else,
// and Teardown was a no-op on the registry — so an unapproved, deleted or
// renamed connector kept a live backend closure for the rest of the process.
// ImageBackendRegistered went on saying yes, which is what the admin's
// image-provider picker keys off, and only a restart cleared it.
package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func reloadTestDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	t.Cleanup(func() {
		RootDB = prev
		restImageMu.Lock()
		for n := range ownedImageBackends {
			delete(ownedImageBackends, n)
			UnregisterImageBackend(n)
		}
		restImageMu.Unlock()
	})
	return db
}

func comfyConnector(t *testing.T, name, baseURL string) Connector {
	t.Helper()
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	raw, _, err := comfyBuildSpec(tpl, map[string]any{
		"base_url": baseURL, "workflow_type": ComfyTypeGenerate, "credential": "no_auth",
	})
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	return Connector{Name: name, Kind: RestImageConnectorKind, Spec: json.RawMessage(raw), Approved: true}
}

// Unapproving a connector must take its backend out of the registry, not leave
// it on offer until the process restarts.
func TestTearingDownAConnectorDropsItsBackend(t *testing.T) {
	db := reloadTestDB(t)
	c := comfyConnector(t, "comfyui", "http://box:8188")
	db.Set(connectorsTable, c.Name, c)

	if err := (restImageHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ImageBackendRegistered("comfyui") {
		t.Fatal("an approved connector must have a live backend")
	}

	c.Approved = false
	db.Set(connectorsTable, c.Name, c)
	if err := (restImageHandler{}).Teardown(c); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if ImageBackendRegistered("comfyui") {
		t.Error("an unapproved connector must not stay registered as a backend")
	}
}

// The sweep is the "reload everything" part: saving ANY connector reconciles
// the whole registry, so a name that went away behind the app's back — a
// rename, a delete, an unapprove that skipped Teardown — is cleaned up by the
// next save rather than surviving to the next restart.
func TestASaveReconcilesEveryBackendNotJustItsOwn(t *testing.T) {
	db := reloadTestDB(t)
	gone := comfyConnector(t, "old_name", "http://box:8188")
	db.Set(connectorsTable, gone.Name, gone)
	if err := (restImageHandler{}).Materialize(gone); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ImageBackendRegistered("old_name") {
		t.Fatal("setup: backend should be live")
	}

	// The row disappears without Teardown running — what a rename does.
	db.Unset(connectorsTable, "old_name")

	// Saving an UNRELATED connector must still clean it up.
	other := comfyConnector(t, "new_name", "http://box:8188")
	db.Set(connectorsTable, other.Name, other)
	if err := (restImageHandler{}).Materialize(other); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if ImageBackendRegistered("old_name") {
		t.Error("the orphaned backend should have been swept on the next save")
	}
	if !ImageBackendRegistered("new_name") {
		t.Error("the saved connector must be registered")
	}
}

// The sweep only removes names rest_image put there — it must not clobber a
// backend registered by anything else.
func TestTheSweepLeavesForeignBackendsAlone(t *testing.T) {
	db := reloadTestDB(t)
	RegisterImageBackend("not_a_connector", func(_ context.Context, _ string, _ bool) (*ImageGenResult, error) {
		return nil, nil
	})
	t.Cleanup(func() { UnregisterImageBackend("not_a_connector") })

	c := comfyConnector(t, "comfyui", "http://box:8188")
	db.Set(connectorsTable, c.Name, c)
	if err := (restImageHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ImageBackendRegistered("not_a_connector") {
		t.Error("a backend this handler did not register must survive the sweep")
	}
}

// Re-saving with a new server keeps exactly one backend under the same name,
// pointed at the edit — the whole point of reloading on save.
func TestReSavingRefreshesRatherThanDuplicates(t *testing.T) {
	db := reloadTestDB(t)
	c := comfyConnector(t, "comfyui", "http://oldbox:8188")
	db.Set(connectorsTable, c.Name, c)
	if err := (restImageHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	moved := comfyConnector(t, "comfyui", "http://newbox:9000")
	db.Set(connectorsTable, moved.Name, moved)
	if err := (restImageHandler{}).Materialize(moved); err != nil {
		t.Fatalf("re-materialize: %v", err)
	}

	// One entry under this name, not a second registration alongside the old
	// one. Asserted against the handler's own set rather than the whole
	// registry, which other tests in this package also write to.
	restImageMu.Lock()
	owned := len(ownedImageBackends)
	mine := ownedImageBackends["comfyui"]
	restImageMu.Unlock()
	if !mine || owned != 1 {
		t.Errorf("handler-owned backends = %d (comfyui present: %v), want exactly comfyui", owned, mine)
	}
	stored, ok := GetConnector(db, "comfyui")
	if !ok {
		t.Fatal("connector missing")
	}
	s, err := (restImageHandler{}).parse(stored)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.SubmitURL == "" || !strings.Contains(s.SubmitURL, "newbox") {
		t.Errorf("the live spec still points at the old server: %q", s.SubmitURL)
	}
}
