package filestore

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A two-phase response is one whole argv element of the admin's binary, so a
// response starting with '-' reached it as an option rather than a value.
func TestCommandInputCannotBeAnOption(t *testing.T) {
	app, st, argvLog, _ := actionFixture(t, true)
	r := httptest.NewRequest("POST", "/api/commands/run?slug="+st.Slug+"&within=bundle-1&command=decrypt",
		strings.NewReader(`{"input":"--output=/tmp/elsewhere"}`))
	w := httptest.NewRecorder()
	app.handleCommand(w, asAdmin(t, r, "user1"))
	if w.Code != 400 {
		t.Fatalf("a leading-dash response should be refused, got %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Fatal("the command ran with an option-shaped response")
	}
}

// The same rule for an agent calling a mapped action: a value that fills a
// whole argument of the command line cannot start with '-'.
func TestMappedActionValueCannotBeAnOption(t *testing.T) {
	tools := []*TempTool{{Name: "cap", Mode: TempToolModeToolbox, Actions: []TempToolAction{
		{Name: "unpack", CommandTemplate: "/opt/bin/cap unpack '{file}' --level={level}"},
	}}}
	ran := false
	h := refuseOptionArgs(tools, "cap", func(ctx context.Context, args map[string]any) (string, error) {
		ran = true
		return "ok", nil
	})
	if _, err := h(context.Background(), map[string]any{"action": "unpack", "file": "-rf"}); err == nil || ran {
		t.Fatalf("a leading-dash value in a whole-argument slot ran (err=%v)", err)
	}
	// Inside a larger argument it is only a value.
	if _, err := h(context.Background(), map[string]any{"action": "unpack", "file": "a.bin", "level": "-1"}); err != nil || !ran {
		t.Fatalf("an embedded value was refused: %v", err)
	}
	ran = false
	exp := refuseOptionArgs(tools, "cap_unpack", func(ctx context.Context, args map[string]any) (string, error) {
		ran = true
		return "ok", nil
	})
	if _, err := exp(context.Background(), map[string]any{"file": "--help"}); err == nil || ran {
		t.Fatal("the expanded action form let an option through")
	}
}
