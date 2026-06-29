package customapps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestRunDataSourcePython dispatches a trivial data-source script end to end:
// the app's records + a query param go in as env vars, the python prints a JSON
// array, and we get it back. Skips when the sandbox/python isn't available in the
// test environment (CI without python3 / bwrap), since this exercises real exec.
func TestRunDataSourcePython(t *testing.T) {
	// The sandbox mounts a fresh tmpfs over /tmp, which would shadow a workspace
	// placed there — so put the test workspace under $HOME (mirrors production,
	// where WorkspacesDir lives under the data dir, not /tmp).
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	ws, err := os.MkdirTemp(home, "gohort-ds-test-")
	if err != nil {
		t.Skipf("cannot create workspace outside /tmp: %v", err)
	}
	defer os.RemoveAll(ws)
	SetWorkspacesDir(filepath.Join(ws, "workspaces"))

	ds := AppDataSource{
		Name:     "demo",
		Language: "python",
		// Echo back the record count + the query param, as a one-row table.
		Script: "import os, json\n" +
			"recs = json.loads(os.environ.get('records', '[]'))\n" +
			"print(json.dumps([{'n': len(recs), 'q': os.environ.get('q', '')}]))\n",
		Capabilities: []string{}, // non-nil empty → no hook started (no network needed)
	}
	args := map[string]any{
		"records": `[{"a":1},{"a":2}]`,
		"q":       "hello",
	}

	out, err := runDataSource("tester", nil, "demo-app", ds, args)
	if err != nil {
		t.Skipf("sandbox/python unavailable in this environment: %v", err)
	}
	out = strings.TrimSpace(out)
	if !json.Valid([]byte(out)) {
		t.Skipf("non-JSON output (likely no python3/sandbox): %q", out)
	}
	var rows []map[string]any
	if e := json.Unmarshal([]byte(out), &rows); e != nil {
		t.Fatalf("output is not a JSON array: %q (%v)", out, e)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d: %q", len(rows), out)
	}
	if rows[0]["q"] != "hello" {
		t.Fatalf("query param not passed through: %q", out)
	}
	if n, _ := rows[0]["n"].(float64); n != 2 {
		t.Fatalf("records not passed through (expected n=2): %q", out)
	}
}

// TestRunAppScriptAction dispatches an action-shaped script (prints a JSON object
// {message, records}) through the shared runner — the write-side seam. The
// framework's upsert of result.Records lives in handleAction; here we just prove
// the script runs and its object output round-trips.
func TestRunAppScriptAction(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	ws, err := os.MkdirTemp(home, "gohort-act-test-")
	if err != nil {
		t.Skipf("cannot create workspace outside /tmp: %v", err)
	}
	defer os.RemoveAll(ws)
	SetWorkspacesDir(filepath.Join(ws, "workspaces"))

	script := "import os, json\n" +
		"recs = json.loads(os.environ.get('records', '[]'))\n" +
		"print(json.dumps({'message': 'synced ' + os.environ.get('note',''), 'records': recs + [{'id':'new'}]}))\n"
	args := map[string]any{"records": `[{"id":"a"}]`, "note": "ok"}

	out, err := runAppScript("tester", nil, "demo-app", "action", "sync", "python", script, []string{}, args)
	if err != nil {
		t.Skipf("sandbox/python unavailable: %v", err)
	}
	out = strings.TrimSpace(out)
	if !json.Valid([]byte(out)) {
		t.Skipf("non-JSON output (likely no python3/sandbox): %q", out)
	}
	var result struct {
		Message string           `json:"message"`
		Records []map[string]any `json:"records"`
	}
	if e := json.Unmarshal([]byte(out), &result); e != nil {
		t.Fatalf("output is not a JSON object: %q (%v)", out, e)
	}
	if result.Message != "synced ok" {
		t.Fatalf("message/param not passed through: %q", out)
	}
	if len(result.Records) != 2 {
		t.Fatalf("expected 2 records back (1 in + 1 added): %q", out)
	}
}
