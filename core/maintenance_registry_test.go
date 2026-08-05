package core

// Every maintenance action the deployment ships must actually be in the
// registry after package init.
//
// This exists because one silently wasn't. The source-hook engine moved to a
// leaf package and kept registering its cache sweep through a hook that core's
// init assigns — but a leaf initializes BEFORE the package importing it, so the
// hook was still nil, the nil-guard returned quietly, and the action
// disappeared from the admin surface. Nothing failed; it was simply gone.

import "testing"

func TestShippedMaintenanceActionsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"sweep_expired_caches":         false,
		"survey_workspace_bound_tools": false,
	}
	for _, m := range ListMaintenanceFuncs() {
		if _, ok := want[m.Key]; ok {
			want[m.Key] = true
		}
		if m.Label == "" || m.Desc == "" {
			t.Errorf("maintenance %q has no label or description — the admin surface renders both", m.Key)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("maintenance action %q is not registered; if it moved to a leaf package, register it from core — a leaf's init runs first and a hook assigned in core's init is still nil there", key)
		}
	}
}
