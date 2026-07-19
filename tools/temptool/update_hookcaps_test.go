package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestUpdateShellPreservesHookCapabilities is the regression test for the
// "method fetch_via not granted after an update" bug: a shell tool that
// declares hook_capabilities=["fetch_via:<cred>"] on create must keep that
// grant when it's edited. The update schema has no hook_capabilities field,
// so preservation depends entirely on tempToolToCreateArgs round-tripping it.
// Before the fix, update reconstructed the create-args WITHOUT the caps and
// the create path default-on'd only [fetch log browse_page], silently
// stripping fetch_via:<cred> — the next dispatch failed "not granted".
func TestUpdateShellPreservesHookCapabilities(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		WorkspaceDir:  t.TempDir(),
		DB:            &DBase{Store: kvlite.MemStore()},
	}

	create := map[string]any{
		"action":            "create",
		"name":              "ts3_list",
		"description":       "list clients via the ts3 api",
		"mode":              "shell",
		"command_template":  "python3 {workspace_dir}/run.py",
		"script_body":       "from gohort import fetch_via\nprint(fetch_via('ts3_api', 'https://x.test/clients'))\n",
		"script_name":       "run.py",
		"hook_capabilities": []any{"fetch_via:ts3_api"},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Sanity: the freshly-created record carries the declared grant plus the
	// auto-added defaults.
	if got, ok := loadExistingToolRecord(sess, "ts3_list"); !ok {
		t.Fatalf("tool not found after create")
	} else if !hasCap(got.HookCapabilities, "fetch_via:ts3_api") {
		t.Fatalf("create dropped fetch_via:ts3_api; caps=%v", got.HookCapabilities)
	}

	// Edit the description ONLY — the bug's trigger. No hook_capabilities field
	// exists on the update schema, so this is the exact path that used to wipe
	// the grant.
	upd := map[string]any{
		"name":        "ts3_list",
		"description": "list clients via the ts3 api (edited)",
	}
	if _, err := updateGrouped(upd, sess); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, ok := loadExistingToolRecord(sess, "ts3_list")
	if !ok {
		t.Fatalf("tool not found after update")
	}
	if got.Description != "list clients via the ts3 api (edited)" {
		t.Errorf("description not updated: %q", got.Description)
	}
	if !hasCap(got.HookCapabilities, "fetch_via:ts3_api") {
		t.Errorf("update STRIPPED fetch_via:ts3_api — caps=%v (this is the regressed bug)", got.HookCapabilities)
	}
	// The default-ons must survive too, and there must be no duplicates.
	for _, want := range []string{"fetch", "log", "browse_page"} {
		if !hasCap(got.HookCapabilities, want) {
			t.Errorf("update dropped default-on cap %q — caps=%v", want, got.HookCapabilities)
		}
	}
	if n := count(got.HookCapabilities, "fetch_via:ts3_api"); n != 1 {
		t.Errorf("fetch_via:ts3_api appears %d times, want 1 — caps=%v", n, got.HookCapabilities)
	}
}

// TestUpdateShellPreservesRawNetworkAndState confirms the sibling fields that
// were lost the same way (no update-schema parameter, dropped by the old
// tempToolToCreateArgs) also round-trip through an unrelated edit.
func TestUpdateShellPreservesRawNetworkAndState(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		WorkspaceDir:  t.TempDir(),
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	create := map[string]any{
		"action":           "create",
		"name":             "statful",
		"description":      "a stateful raw-net tool",
		"mode":             "shell",
		"command_template": "python3 {workspace_dir}/run.py",
		"script_body":      "print('hi')\n",
		"script_name":      "run.py",
		"raw_network":      true,
		"state_path":       "state",
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	upd := map[string]any{"name": "statful", "description": "edited"}
	if _, err := updateGrouped(upd, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok := loadExistingToolRecord(sess, "statful")
	if !ok {
		t.Fatalf("tool not found after update")
	}
	if !got.RawNetwork {
		t.Errorf("update dropped raw_network")
	}
	if got.StatePath != "state" {
		t.Errorf("update dropped state_path; got %q", got.StatePath)
	}
}

// TestBuildEnvArgsNeverNil pins the invariant behind the WiWee crash: a
// param-less hook-enabled tool (dispatched with empty args) must still get a
// WRITABLE env map, because the dispatcher writes GOHORT_HOOK_PATH into it
// (temptool.go:1509). A nil map there panics "assignment to entry in nil map"
// — the exact panic that took down ts3_list_clients (no params + a hook).
func TestBuildEnvArgsNeverNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"nil args", nil},
		{"empty args", map[string]any{}},
		{"one arg", map[string]any{"who": "bob"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := buildEnvArgs(tc.args)
			if env == nil {
				t.Fatalf("buildEnvArgs(%v) returned a nil map — the dispatcher's GOHORT_HOOK_PATH write would panic", tc.args)
			}
			// The write that panicked at temptool.go:1509 must be safe now.
			env["GOHORT_HOOK_PATH"] = "/tmp/x.sock"
		})
	}
}

func hasCap(caps []string, want string) bool {
	return count(caps, want) > 0
}

func count(caps []string, want string) int {
	n := 0
	for _, c := range caps {
		if c == want {
			n++
		}
	}
	return n
}
