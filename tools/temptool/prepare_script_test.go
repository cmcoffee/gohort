package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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

// TestPrepareScriptBodyRefusesDefaultAPI pins the guard at the shared seam: a
// script, or a bare command, that calls the author's own tools through
// default_api is refused before anything touches disk (nil session), on the
// add_tool path as much as tool_def's. add_tool used to accept the import and
// fail only at the verification run.
func TestPrepareScriptBodyRefusesDefaultAPI(t *testing.T) {
	cases := map[string][2]string{
		"script body":  {"", "import os\nfrom default_api import bulletins\nprint(bulletins(action='create'))\n"},
		"command only": {"python3 -c \"print(default_api.bulletins(action='list'))\"", ""},
	}
	for name, c := range cases {
		_, _, _, err := PrepareScriptBody(nil, "post_board", c[0], c[1], "post.py", nil)
		if err == nil {
			t.Fatalf("%s: expected a refusal", name)
		}
		for _, want := range []string{"default_api", "allow_poster"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: error should mention %q, got: %v", name, want, err)
			}
		}
	}
}

// A saved tool whose script can never run is flagged on get, so a reader does
// not copy it as a working pattern; a sound tool gets no note.
func TestGetWarnsWhenScriptCannotRun(t *testing.T) {
	broken := TempTool{Name: "post_news", CommandTemplate: "python3 {workspace_dir}/script.py",
		ScriptBody: "from default_api import bulletins\nprint(bulletins(action='create', name='news'))\n"}
	note := cannotRunNote(broken)
	for _, want := range []string{"WARNING", "cannot run", "do not copy", "default_api", "allow_poster", "update"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note should say %q, got: %s", want, note)
		}
	}
	if n := cannotRunNote(TempTool{Name: "ok", ScriptBody: "from gohort import fetch_url\nprint(fetch_url('https://x'))\n"}); n != "" {
		t.Fatalf("a sound script must get no note, got: %s", n)
	}
	if tag := cannotRunTag(broken); !strings.Contains(tag, "CANNOT RUN") {
		t.Fatalf("the list tag should say CANNOT RUN, got %q", tag)
	}
	if tag := cannotRunTag(TempTool{Name: "ok", CommandTemplate: "echo hi"}); tag != "" {
		t.Fatalf("a sound tool must get no list tag, got %q", tag)
	}
}

// A template that is only the script's filename is inferred as an empty one:
// Builder wrote command_template="script.py" beside a script_body, the shell
// said "command not found", and three placeholder guesses followed.
func TestABareScriptNameIsInferredLikeAnEmptyTemplate(t *testing.T) {
	for _, cmd := range []string{"script.py", "./script.py", " script.py "} {
		got, _, _, err := PrepareScriptBody(nil, "echo_hi", cmd, "print('hi')", "script.py", map[string]any{})
		if err != nil && !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("%q: %v", cmd, err)
		}
		if err == nil && (!strings.Contains(got, "python3") || !strings.Contains(got, "{workspace_dir}/script.py")) {
			t.Fatalf("%q became %q; want the interpreter and the workspace path", cmd, got)
		}
	}
}
