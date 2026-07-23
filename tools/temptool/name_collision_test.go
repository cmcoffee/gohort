package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestCatalogNamesOf(t *testing.T) {
	tb := &TempTool{
		Name: "vapi_calls",
		Mode: TempToolModeToolbox,
		Actions: []TempToolAction{
			{Name: "place_call"}, {Name: "end_call"},
		},
	}
	got := strings.Join(catalogNamesOf(tb), ",")
	want := "vapi_calls,vapi_calls_place_call,vapi_calls_end_call"
	if got != want {
		t.Errorf("toolbox names: got %q want %q", got, want)
	}
	// A plain api tool contributes only its own name.
	api := &TempTool{Name: "get_weather", Mode: TempToolModeAPI}
	if got := strings.Join(catalogNamesOf(api), ","); got != "get_weather" {
		t.Errorf("api names: got %q", got)
	}
}

func TestCheckCatalogNameCollision(t *testing.T) {
	sess := &ToolSession{}
	if err := sess.AppendTempTool(&TempTool{
		Name: "vapi_calls", Mode: TempToolModeToolbox,
		Actions: []TempToolAction{{Name: "place_call"}},
	}); err != nil {
		t.Fatalf("seed toolbox: %v", err)
	}

	// The live bug: a standalone tool named after an existing toolbox action.
	err := CheckCatalogNameCollision(sess, "vapi_calls_place_call", nil)
	if err == nil {
		t.Fatal("expected a collision against the toolbox's expanded action name")
	}
	if !strings.Contains(err.Error(), "vapi_calls") {
		t.Errorf("error should name the owning toolbox: %v", err)
	}

	// A name nobody publishes is fine.
	if err := CheckCatalogNameCollision(sess, "vapi_calls_get_recording", nil); err != nil {
		t.Errorf("unexpected collision: %v", err)
	}

	// Re-authoring the SAME record overwrites — the iteration path, not a collision.
	if err := CheckCatalogNameCollision(sess, "vapi_calls", []TempToolAction{{Name: "place_call"}}); err != nil {
		t.Errorf("same-name re-author must be allowed: %v", err)
	}

	// The other direction: a NEW toolbox whose action would mint an existing
	// standalone tool's name.
	if err := sess.AppendTempTool(&TempTool{Name: "vapi_admin_list_keys", Mode: TempToolModeAPI}); err != nil {
		t.Fatalf("seed api tool: %v", err)
	}
	err = CheckCatalogNameCollision(sess, "vapi_admin", []TempToolAction{{Name: "list_keys"}})
	if err == nil {
		t.Fatal("expected a collision: toolbox action would mint an existing tool name")
	}
}
