package command

// A declared command's parameters fill values and nothing else: no undeclared
// or loader-changing environment variables, no chained placeholder expansion,
// no leading-dash option in a whole-argument slot.

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

func TestAToolCallCannotSteerTheApprovedBinary(t *testing.T) {
	declared := map[string]core.ToolParam{"file": {Type: "string"}, "PATH": {Type: "string"}}
	env := strings.Join(argsToEnv(map[string]any{
		"file": "notes.txt", "PATH": "/tmp/evil", "LD_PRELOAD": "/tmp/x.so", "extra": "1",
	}, declared), " ")
	if env != "file=notes.txt" {
		t.Errorf("only the declared, harmless parameter should be exported, got %q", env)
	}
	if got := substituteArgs("{a}-{b}", map[string]any{"a": "{b}", "b": "x"}); got != "{b}-x" {
		t.Errorf("a value re-expanded another placeholder: %q", got)
	}
	tool := newCommandTool("open", Spec{Command: "/usr/bin/true", Args: []string{"{file}"}, Params: declared})
	if _, err := tool.Handler()(context.Background(), map[string]any{"file": "--output=/etc/passwd"}); err == nil {
		t.Error("a leading-dash value filled a whole argument")
	}
}
