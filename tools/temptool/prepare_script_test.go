package temptool

import (
	"strings"
	"testing"
)

// TestPrepareScriptBodyNoOpWithoutScript — a blank script_body must echo the
// caller's command_template back untouched, so authoring surfaces can call the
// seam unconditionally without special-casing the no-script path.
func TestPrepareScriptBodyNoOpWithoutScript(t *testing.T) {
	cmd, name, canonical, err := PrepareScriptBody(nil, "t", "echo hi", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "echo hi" {
		t.Fatalf("command_template = %q; want it echoed back unchanged", cmd)
	}
	if name != "" || canonical != "" {
		t.Fatalf("script names should be empty with no script_body; got %q / %q", name, canonical)
	}
}

// TestPrepareScriptBodyRejectsPathySriptName — script_name is a filename, not a
// path; a separator must be refused before anything touches disk. Checked with
// a nil session to prove the guard runs ahead of workspace minting.
func TestPrepareScriptBodyRejectsPathyScriptName(t *testing.T) {
	if _, _, _, err := PrepareScriptBody(nil, "t", "", "print('x')", "../escape.py", nil); err == nil {
		t.Fatal("expected an error for a script_name containing a path separator")
	}
}

// TestInferCommandTemplateByExtension pins the inference that add_tool was
// missing entirely — the gap that made Builder's first authoring attempt fail
// with "command_template is required" on a perfectly good script_body.
func TestInferCommandTemplateByExtension(t *testing.T) {
	cases := []struct {
		name       string
		scriptName string
		body       string
		wantSubstr string
	}{
		{"python", "script.py", "print('x')", "python3"},
		{"bash", "run.sh", "echo x", "bash"},
		{"shebang wins", "run.sh", "#!/usr/bin/env python3\nprint('x')", "{workspace_dir}/run.sh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inferCommandTemplate(tc.scriptName, tc.body, nil)
			if got == "" {
				t.Fatalf("inferCommandTemplate returned empty for %q", tc.scriptName)
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("inferCommandTemplate(%q) = %q; want it to contain %q", tc.scriptName, got, tc.wantSubstr)
			}
			if !strings.Contains(got, "{workspace_dir}") {
				t.Fatalf("inferred template %q must reference {workspace_dir}", got)
			}
		})
	}
}

// TestInferCommandTemplateUnknownExtension — an unrecognized extension must
// yield no template rather than a wrong guess, so the caller surfaces the
// "command_template is required" error instead of shipping a broken tool.
func TestInferCommandTemplateUnknownExtension(t *testing.T) {
	if got := inferCommandTemplate("data.xyz", "blah", nil); got != "" {
		t.Fatalf("inferCommandTemplate for an unknown extension = %q; want empty", got)
	}
}
