package core

// Which tools break if their workspace moves. The survey has to be right in
// both directions: a tool whose script rides in the record travels and must
// NOT be listed, and a tool that only ever had its script on disk must be.

import (
	"strings"
	"testing"
)

func TestWorkspaceBindingClassification(t *testing.T) {
	cases := []struct {
		name   string
		tool   TempTool
		bound  bool
		reason string
	}{
		{
			name:  "script in record travels",
			tool:  TempTool{Name: "a", CommandTemplate: "python3 {workspace_dir}/run.py", ScriptBody: "print(1)"},
			bound: false,
		},
		{
			name:   "script only on disk is bound",
			tool:   TempTool{Name: "b", CommandTemplate: "python3 {workspace_dir}/run.py"},
			bound:  true,
			reason: "no script in record",
		},
		{
			// The single ScriptBody slot cannot represent two files, so the
			// second one stays on disk however the record looks.
			name:   "multi-file reference stays disk-resident",
			tool:   TempTool{Name: "c", CommandTemplate: "python3 {workspace_dir}/main.py --lib {workspace_dir}/helper.py", ScriptBody: "print(1)"},
			bound:  true,
			reason: "multi-file or sub-path reference",
		},
		{
			name:   "sub-path reference cannot be captured",
			tool:   TempTool{Name: "d", CommandTemplate: "python3 {workspace_dir}/lib/main.py", ScriptBody: "print(1)"},
			bound:  true,
			reason: "multi-file or sub-path reference",
		},
		{
			// An output path is not the tool's code. A tool that writes a chart
			// into its workspace is not bound to that directory.
			name:  "scratch output path is not a binding",
			tool:  TempTool{Name: "e", CommandTemplate: "chartgen --out {workspace_dir}/chart.png"},
			bound: false,
		},
		{
			name:  "no workspace reference at all",
			tool:  TempTool{Name: "f", CommandTemplate: "curl https://example.com"},
			bound: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, bound := workspaceBinding(c.tool)
			if bound != c.bound {
				t.Fatalf("bound = %v, want %v (%+v)", bound, c.bound, got)
			}
			if bound && got.Reason != c.reason {
				t.Errorf("reason = %q, want %q", got.Reason, c.reason)
			}
		})
	}
}

func TestFormatNamesTheToolAndWhy(t *testing.T) {
	out := FormatWorkspaceBoundTools([]WorkspaceBoundTool{{
		Owner: "alice", Name: "get_market_data", Reason: "no script in record",
		Refs: []string{"get_market_data.py"}, Template: "python3 {workspace_dir}/get_market_data.py",
	}})
	for _, want := range []string{"get_market_data", "alice", "no script in record", "get_market_data.py"} {
		if !strings.Contains(out, want) {
			t.Errorf("survey is missing %q:\n%s", want, out)
		}
	}
	if FormatWorkspaceBoundTools(nil) != "" {
		t.Error("an empty survey must render empty so callers can print it unconditionally")
	}
}
